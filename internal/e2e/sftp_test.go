package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/network"
	mastersftp "github.com/QwikByte/noryx/internal/master/sftp"
)

// Users reach the files of the servers they may see over SFTP, with an API token as password
// and no more than the token allows them, through the agents like the file manager.
func TestSFTP(t *testing.T) {
	ctx := t.Context()
	m := startMaster(t)
	prev := slog.Default()
	slog.SetDefault(slog.New(m.logs.Handler(slog.LevelInfo)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	svc := m.services(t)
	panel := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(panel.Close)

	// SFTP is off until the settings name an address, and the master then listens there with
	// a host key of its own.
	route := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/sftp"
	api := apiClient{t: t, url: m.panel(t).URL}
	var conn struct {
		Port          int
		HostKey, Path string
	}
	if api.do("GET", route, nil, http.StatusOK, &conn); conn.Port != 0 {
		t.Fatalf("SFTP while off: %+v", conn)
	}
	free := listen(t)
	addr := free.Addr().String()
	check(t, free.Close())
	settings := svc.Settings.Get()
	settings.SFTPAddr = addr
	_, err := svc.Settings.Update(ctx, settings)
	check(t, err)
	dir := t.TempDir()
	key := must(mastersftp.HostKey(dir))
	if again := must(mastersftp.HostKey(dir)); mastersftp.Fingerprint(again) != mastersftp.Fingerprint(key) {
		t.Fatal("the host key changed")
	}
	if info, err := os.Stat(filepath.Join(dir, "host_key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("host key file: %v, %v", info, err)
	}
	ln := svc.Settings.ListenSFTP(func() (string, error) { return mastersftp.Fingerprint(key), nil })
	srv := masterapp.SFTP(svc, masterapp.SignIns(svc), key)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	api.do("GET", route, nil, http.StatusOK, &conn)
	if strconv.Itoa(conn.Port) != addr[strings.LastIndex(addr, ":")+1:] || conn.HostKey != ssh.FingerprintSHA256(key.PublicKey()) || conn.Path != "/node-1/Lobby" {
		t.Fatalf("connection = %+v", conn)
	}

	// Alice may change the files of the lobby, and sees no other server.
	const password = "a-long-enough-password"
	admin := must(svc.Users.CreateUser(ctx, "admin", password))
	check(t, svc.Access.MakeAdmin(ctx, admin.ID))
	alice := must(svc.Users.CreateUser(ctx, "alice", password))
	root := browser(t, panel)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": password}, http.StatusOK, nil)
	var editors access.Group
	root.do("POST", "/api/groups", map[string]any{
		"name": "Lobby editors", "permissions": []string{"files.write"}, "targets": []network.Ref{lobby},
	}, http.StatusCreated, &editors)
	alicePath := "/api/users/" + strconv.FormatInt(alice.ID, 10)
	root.do("PUT", alicePath, map[string]any{"groups": []string{editors.ID}}, http.StatusNoContent, nil)
	type apiToken struct{ id, secret string }
	token := func(userID int64, name string, perms ...string) apiToken {
		tk, secret, err := svc.Users.CreateToken(ctx, userID, password, "", auth.TokenInput{Name: name, Permissions: perms, ExpiresAt: time.Now().Add(time.Hour)})
		check(t, err)
		return apiToken{tk.ID, secret}
	}
	connect := func(user, password string) (*sftp.Client, error) {
		c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.Password(password)}, HostKeyCallback: ssh.FixedHostKey(key.PublicKey()), Timeout: 5 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		client, err := sftp.NewClient(c, sftp.UseConcurrentWrites(true))
		if err != nil {
			c.Close()
			return nil, err
		}
		t.Cleanup(func() { client.Close(); c.Close() })
		return client, nil
	}
	full, readOnly := token(alice.ID, "full"), token(alice.ID, "read", "files.read")
	c := must(connect("Alice", full.secret)) // usernames don't care about case
	writeFile := func(c *sftp.Client, p string, data []byte) error {
		f, err := c.Create(p)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return errors.Join(err, f.Close())
	}
	readFile := func(c *sftp.Client, p string) ([]byte, error) {
		f, err := c.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	names := func(c *sftp.Client, p string) []string {
		t.Helper()
		var list []string
		for _, f := range must(c.ReadDir(p)) {
			list = append(list, f.Name())
		}
		return list
	}
	if got := names(c, "/"); !slices.Equal(got, []string{"node-1"}) {
		t.Fatalf("/ = %v", got)
	}
	if got := names(c, "/node-1"); !slices.Equal(got, []string{"Lobby"}) {
		t.Fatalf("/node-1 = %v, want only the lobby", got)
	}
	if _, err := c.ReadDir("/node-1/Survival"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a server Alice may not see: %v", err)
	}
	if _, err := c.ReadDir("/node-1/" + survival.ServerID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a server Alice may not see by its ID: %v", err)
	}

	// Files are written and read through the agent, also in parts that arrive out of order.
	check(t, c.Mkdir("/node-1/Lobby/plugins"))
	content := make([]byte, 3<<20+12345)
	_, _ = rand.Read(content)
	f := must(c.Create("/node-1/Lobby/plugins/world.dat"))
	if _, err := io.Copy(f, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	check(t, f.Close())
	data := filepath.Join(a.runtime.dir, lobby.ServerID)
	if written, err := os.ReadFile(filepath.Join(data, "plugins", "world.dat")); err != nil || !bytes.Equal(written, content) {
		t.Fatalf("written file: %d bytes, %v", len(written), err)
	}
	f = must(c.Open("/node-1/" + lobby.ServerID + "/plugins/world.dat")) // IDs work too
	var read bytes.Buffer
	if _, err := io.Copy(&read, f); err != nil || !bytes.Equal(read.Bytes(), content) {
		t.Fatalf("read file: %d bytes, %v", read.Len(), err)
	}
	// A client that jumps around reads from where it asks.
	part := make([]byte, 100)
	for _, off := range []int64{2 << 20, 10, 3 << 20} {
		if n, err := f.ReadAt(part, off); n != len(part) || err != nil || !bytes.Equal(part, content[off:off+100]) {
			t.Fatalf("read at %d: %d, %v", off, n, err)
		}
	}
	check(t, f.Close())
	if info := must(c.Stat("/node-1/Lobby/plugins/world.dat")); info.Size() != int64(len(content)) || info.IsDir() {
		t.Fatalf("stat = %v %d", info.Name(), info.Size())
	}
	if info := must(c.Stat("/node-1/Lobby")); !info.IsDir() || info.Name() != "Lobby" {
		t.Fatalf("stat of the server = %v", info.Name())
	}

	// A write that doesn't start at the beginning, e.g. to resume an upload, leaves the file as
	// it was, and so does one without truncating an existing file.
	f = must(c.OpenFile("/node-1/Lobby/plugins/world.dat", os.O_WRONLY))
	_, err = f.WriteAt([]byte("resumed"), 1000)
	if err = errors.Join(err, f.Close()); err == nil {
		t.Fatal("resumed upload: no error")
	}
	f = must(c.OpenFile("/node-1/Lobby/plugins/world.dat", os.O_WRONLY))
	_, err = f.Write([]byte("changed"))
	if err = errors.Join(err, f.Close()); err == nil {
		t.Fatal("write without truncating: no error")
	}
	if kept, _ := os.ReadFile(filepath.Join(data, "plugins", "world.dat")); !bytes.Equal(kept, content) {
		t.Fatal("the file changed")
	}

	// Renaming never replaces a file, and removing needs an empty folder.
	check(t, c.Rename("/node-1/Lobby/plugins/world.dat", "/node-1/Lobby/plugins/renamed.dat"))
	check(t, writeFile(c, "/node-1/Lobby/plugins/other.dat", []byte("other")))
	if err := c.Rename("/node-1/Lobby/plugins/other.dat", "/node-1/Lobby/plugins/renamed.dat"); err == nil {
		t.Fatal("rename over a file: no error")
	}
	if err := c.Remove("/node-1/Lobby/plugins"); err == nil {
		t.Fatal("remove a folder as a file: no error")
	}
	if err := c.RemoveDirectory("/node-1/Lobby/plugins"); err == nil {
		t.Fatal("remove a folder that isn't empty: no error")
	}
	check(t, c.Remove("/node-1/Lobby/plugins/renamed.dat"))
	check(t, c.Remove("/node-1/Lobby/plugins/other.dat"))
	check(t, c.RemoveDirectory("/node-1/Lobby/plugins"))
	if _, err := os.Stat(filepath.Join(data, "plugins")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed folder: %v", err)
	}

	// Nothing is written outside the folders of servers, nor in a server of another or no node.
	for _, p := range []string{"/x", "/node-1/x", "/node-1/Lobby", "/node-1/Survival/x", "/node-2/Lobby/x", "/node-1/Lobby/../Survival/x"} {
		if err := writeFile(c, p, []byte("x")); err == nil {
			t.Errorf("write %s: no error", p)
		}
	}
	if err := c.Mkdir("/node-1/new"); err == nil {
		t.Error("folder next to the servers: no error")
	}
	if err := c.Symlink("/node-1/Lobby/server.properties", "/node-1/Lobby/link"); err == nil {
		t.Error("link: no error")
	}

	// Secrets stay hidden, as in the file manager.
	check(t, os.WriteFile(filepath.Join(data, ".rcon-cli.env"), []byte("password=s3cret\n"), 0o600))
	check(t, os.WriteFile(filepath.Join(data, "server.properties"), []byte("motd=hi\nrcon.password=s3cret\n"), 0o600))
	if got := names(c, "/node-1/Lobby"); slices.Contains(got, ".rcon-cli.env") || !slices.Contains(got, "server.properties") {
		t.Fatalf("listing = %v", got)
	}
	if _, err := c.Open("/node-1/Lobby/.rcon-cli.env"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("open the file of rcon-cli: %v", err)
	}
	if err := writeFile(c, "/node-1/Lobby/.rcon-cli.env", []byte("password=mine\n")); err == nil {
		t.Fatal("write the file of rcon-cli: no error")
	}
	if shown := must(readFile(c, "/node-1/Lobby/server.properties")); string(shown) != "motd=hi\nrcon.password=<hidden>\n" {
		t.Fatalf("server.properties = %q", shown)
	}
	check(t, writeFile(c, "/node-1/Lobby/server.properties", []byte("motd=changed\nrcon.password=<hidden>\n")))
	if saved, _ := os.ReadFile(filepath.Join(data, "server.properties")); string(saved) != "motd=changed\nrcon.password=s3cret\n" {
		t.Fatalf("saved server.properties = %q", saved)
	}

	// A token that may only read can't change anything.
	r := must(connect("alice", readOnly.secret))
	if got := must(readFile(r, "/node-1/Lobby/server.properties")); len(got) == 0 {
		t.Fatal("read with a token that may read: nothing")
	}
	for name, change := range map[string]func() error{
		"write":  func() error { return writeFile(r, "/node-1/Lobby/new.txt", []byte("x")) },
		"mkdir":  func() error { return r.Mkdir("/node-1/Lobby/new") },
		"rename": func() error { return r.Rename("/node-1/Lobby/server.properties", "/node-1/Lobby/moved") },
		"remove": func() error { return r.Remove("/node-1/Lobby/server.properties") },
	} {
		if err := change(); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s with a token that may only read: %v", name, err)
		}
	}

	// The administrator sees all servers, and files only move within one.
	adminToken := token(admin.ID, "admin")
	rootSFTP := must(connect("admin", adminToken.secret))
	if got := names(rootSFTP, "/node-1"); !slices.Equal(got, []string{"Lobby", "Survival"}) {
		t.Fatalf("/node-1 of the administrator = %v", got)
	}
	if err := rootSFTP.Rename("/node-1/Lobby/server.properties", "/node-1/Survival/server.properties"); err == nil {
		t.Fatal("move to another server: no error")
	}

	// Changes of Alice's permissions apply to her open connection right away.
	root.do("PUT", alicePath, map[string]any{"groups": []string{}}, http.StatusNoContent, nil)
	if _, err := c.ReadDir("/node-1/Lobby"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("listing after Alice lost her permissions: %v", err)
	}
	root.do("PUT", alicePath, map[string]any{"groups": []string{editors.ID}}, http.StatusNoContent, nil)
	check(t, writeFile(c, "/node-1/Lobby/again.txt", []byte("again")))

	// The log names who changed what with which token over SFTP, but never the token.
	var entries []logEntry
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		root.do("GET", "/api/logs?source=master", nil, http.StatusOK, &entries)
		if slices.ContainsFunc(entries, func(e logEntry) bool { return e.Attrs["path"] == "again.txt" }) {
			break
		}
	}
	upload := slices.IndexFunc(entries, func(e logEntry) bool {
		return e.Message == "Upload file" && e.Attrs["path"] == "plugins/world.dat" && e.Source == logs.FromMaster
	})
	if upload < 0 {
		t.Fatalf("no upload in %+v", entries)
	}
	if e := entries[upload]; e.User != "alice" || e.Attrs["token"] != "full" || e.Attrs["via"] != "sftp" || e.ServerID != lobby.ServerID || e.Category != "files" {
		t.Errorf("entry of the upload = %+v", e)
	}
	for _, message := range []string{"Sign in with SFTP", "Download file", "Move file", "Delete file", "Create folder", "Upload file denied"} {
		if !slices.ContainsFunc(entries, func(e logEntry) bool { return e.Message == message }) {
			t.Errorf("no entry %q", message)
		}
	}
	log, err := json.Marshal(entries)
	check(t, err)
	for _, secret := range []string{full.secret, readOnly.secret, adminToken.secret} {
		hash := sha256.Sum256([]byte(secret))
		if strings.Contains(string(log), secret[len(auth.TokenPrefix):]) || strings.Contains(string(log), hex.EncodeToString(hash[:])) {
			t.Fatal("the log holds a token or its hash")
		}
	}

	// Sign-ins need a valid token of the user: not the password, not another user's token,
	// not an expired or revoked one. A revoked token ends its connection.
	expired := token(alice.ID, "expired")
	_, err = m.db.ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).Unix(), expired.id)
	check(t, err)
	_, err = svc.Users.RevokeToken(ctx, alice.ID, readOnly.id)
	check(t, err)
	for name, try := range map[string][2]string{
		"password": {"alice", password}, "another user's token": {"admin", full.secret}, "expired token": {"alice", expired.secret}, "revoked token": {"alice", readOnly.secret},
	} {
		if _, err := connect(try[0], try[1]); err == nil {
			t.Errorf("sign in with the %s: no error", name)
		}
	}
	if _, err := r.ReadDir("/node-1/Lobby"); err == nil {
		t.Fatal("the connection of a revoked token goes on")
	}

	// Wrong tokens took the client's attempts, so that even a right one doesn't sign in now.
	if _, err := connect("alice", full.secret); err == nil {
		t.Fatal("sign in after too many attempts: no error")
	}
	tooMany := func(e logEntry) bool {
		return e.Message == "Sign in with SFTP failed" && strings.HasPrefix(e.Attrs["reason"], "Too many attempts")
	}
	for deadline := time.Now().Add(5 * time.Second); !slices.ContainsFunc(entries, tooMany); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no failed sign-in for too many attempts in %+v", entries)
		}
		root.do("GET", "/api/logs?source=master&category=auth", nil, http.StatusOK, &entries)
	}
}
