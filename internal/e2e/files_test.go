package e2e

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

func TestFiles(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	panel := m.panel(t)
	api := apiClient{t: t, url: panel.URL}
	base := "/api/nodes/" + srv.NodeID + "/servers/" + srv.ServerID + "/files"
	q := func(p string) string { return "?path=" + url.QueryEscape(p) }

	// Upload a file into a new folder; existing files are only replaced on request.
	api.do("POST", base+"/directories", map[string]string{"path": "plugins/Example"}, http.StatusNoContent, nil)
	api.do("PUT", base+"/content"+q("plugins/Example/config.yml"), []byte("motd: hi\n"), http.StatusCreated, nil)
	api.do("PUT", base+"/content"+q("plugins/Example/config.yml"), []byte("lost\n"), http.StatusConflict, nil)
	api.do("PUT", base+"/content"+q("plugins/Example/config.yml")+"&overwrite=true", []byte("motd: hello\n"), http.StatusCreated, nil)
	api.do("PUT", base+"/content"+q("index.html"), []byte("<script>alert(1)</script>"), http.StatusCreated, nil)

	var list struct {
		Files []struct {
			Name      string `json:"name"`
			Directory bool   `json:"directory"`
			Size      int64  `json:"size"`
		} `json:"files"`
	}
	api.do("GET", base+q(""), nil, http.StatusOK, &list)
	if len(list.Files) != 2 || list.Files[0].Name != "plugins" || !list.Files[0].Directory || list.Files[1].Name != "index.html" {
		t.Fatalf("listing = %+v, want the plugins folder before index.html", list.Files)
	}

	// Downloads are attachments, so a file from a server can't run scripts in the panel.
	res, err := http.Get(panel.URL + base + "/content" + q("index.html"))
	check(t, err)
	res.Body.Close()
	if h := res.Header; h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Security-Policy") != "sandbox" ||
		h.Get("Content-Disposition") != "attachment; filename=index.html" {
		t.Fatalf("download headers = %v", h)
	}
	if got := api.do("GET", base+"/content"+q("plugins/Example/config.yml"), nil, http.StatusOK, nil); got != "motd: hello\n" {
		t.Fatalf("content = %q", got)
	}

	// A range of a file can be read, e.g. the end of a large log.
	api.do("PUT", base+"/content"+q("latest.log"), []byte("0123456789"), http.StatusCreated, nil)
	for header, want := range map[string]struct {
		status       int
		body, within string
	}{
		"bytes=-4":      {http.StatusPartialContent, "6789", "bytes 6-9/10"},
		"bytes=2-4":     {http.StatusPartialContent, "234", "bytes 2-4/10"},
		"bytes=8-":      {http.StatusPartialContent, "89", "bytes 8-9/10"},
		"bytes=20-":     {http.StatusRequestedRangeNotSatisfiable, "", "bytes */10"},
		"bytes=0-1,4-5": {http.StatusOK, "0123456789", ""},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, panel.URL+base+"/content"+q("latest.log"), nil)
		check(t, err)
		req.Header.Set("Range", header)
		res, err := http.DefaultClient.Do(req)
		check(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		check(t, err)
		if res.StatusCode != want.status || string(body) != want.body || res.Header.Get("Content-Range") != want.within {
			t.Errorf("read %s: status %d, %q, Content-Range %q", header, res.StatusCode, body, res.Header.Get("Content-Range"))
		}
	}

	// An editor saves only the version it opened, which the ETag names, unless told to overwrite.
	put := func(etag, content string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, panel.URL+base+"/content"+q("plugins/Example/config.yml")+"&overwrite=true", strings.NewReader(content))
		check(t, err)
		req.Header.Set("If-Match", etag)
		res, err := http.DefaultClient.Do(req)
		check(t, err)
		res.Body.Close()
		return res.StatusCode, res.Header.Get("ETag")
	}
	res, err = http.Get(panel.URL + base + "/content" + q("plugins/Example/config.yml"))
	check(t, err)
	res.Body.Close()
	opened := res.Header.Get("ETag")
	code, saved := put(opened, "motd: edited\n")
	if code != http.StatusCreated || saved == "" || saved == opened {
		t.Fatalf("save: status %d, ETag %q after %q", code, saved, opened)
	}
	if code, _ := put(opened, "motd: lost\n"); code != http.StatusPreconditionFailed {
		t.Fatalf("save of the version opened before: status %d", code)
	}
	if code, _ := put("garbage", "motd: lost\n"); code != http.StatusBadRequest {
		t.Fatalf("save with a made-up ETag: status %d", code)
	}
	if code, _ := put(saved, "motd: hello\n"); code != http.StatusCreated {
		t.Fatalf("save of the saved version: status %d", code)
	}

	// Rename, then download the folder as a ZIP archive.
	api.do("POST", base+"/move", map[string]string{"from": "plugins/Example/config.yml", "to": "plugins/Example/settings.yml"}, http.StatusNoContent, nil)
	archive := api.do("GET", base+"/archive"+q("plugins"), nil, http.StatusOK, nil)
	zr, err := zip.NewReader(bytes.NewReader([]byte(archive)), int64(len(archive)))
	check(t, err)
	f, err := zr.Open("Example/settings.yml")
	check(t, err)
	content, err := io.ReadAll(f)
	check(t, err)
	if string(content) != "motd: hello\n" {
		t.Fatalf("archived file = %q", content)
	}

	// Chosen files and folders download as one archive, and move into another folder, but
	// not into themselves.
	api.do("PUT", base+"/content"+q("plugins/Example/other.yml"), []byte("other\n"), http.StatusCreated, nil)
	archive = api.do("GET", base+"/archive"+q("plugins/Example")+"&name=settings.yml", nil, http.StatusOK, nil)
	zr, err = zip.NewReader(bytes.NewReader([]byte(archive)), int64(len(archive)))
	check(t, err)
	if len(zr.File) != 1 || zr.File[0].Name != "settings.yml" {
		t.Fatalf("archive of a chosen file = %v", zr.File)
	}
	api.do("GET", base+"/archive"+q("plugins")+"&name=missing", nil, http.StatusNotFound, nil)
	api.do("POST", base+"/move", map[string]string{"from": "index.html", "to": "plugins/index.html"}, http.StatusNoContent, nil)
	api.do("POST", base+"/move", map[string]string{"from": "plugins", "to": "plugins/Example/plugins"}, http.StatusBadRequest, nil)

	// Paths can't leave the server's directory, not even through a symbolic link.
	check(t, os.Symlink(t.TempDir(), filepath.Join(a.runtime.dir, srv.ServerID, "outside")))
	for _, p := range []string{"../" + srv.ServerID, "/../../etc/passwd", `..\x`, "outside/x"} {
		res, err := http.Get(panel.URL + base + "/content" + q(p))
		check(t, err)
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Errorf("read %q: status %d", p, res.StatusCode)
		}
	}
	api.do("PUT", base+"/content"+q("outside/x"), []byte("x"), http.StatusBadRequest, nil)

	api.do("DELETE", base+q("plugins"), nil, http.StatusNoContent, nil)
	api.do("DELETE", base+q("plugins"), nil, http.StatusNotFound, nil)
	api.do("DELETE", base+q(""), nil, http.StatusBadRequest, nil)
}

// Users of the panel never see the secrets of a server, such as its RCON password or the
// forwarding secret of its network, but can still save the files that hold them.
func TestFileSecrets(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	api := apiClient{t: t, url: m.panel(t).URL}
	base := "/api/nodes/" + srv.NodeID + "/servers/" + srv.ServerID + "/files"
	q := func(p string) string { return "?path=" + url.QueryEscape(p) }
	data := filepath.Join(a.runtime.dir, srv.ServerID)
	check(t, os.MkdirAll(filepath.Join(data, "config"), 0o750))
	files := map[string]string{
		".rcon-cli.env":           "password=s3cret\n",
		"server.properties":       "motd=hi\nrcon.password=s3cret\n",
		"config/paper-global.yml": "proxies:\n  velocity:\n    secret: F0rward1ngS3cret\n",
	}
	for name, content := range files {
		check(t, os.WriteFile(filepath.Join(data, name), []byte(content), 0o600))
	}

	var list struct{ Files []struct{ Name string } }
	api.do("GET", base+q(""), nil, http.StatusOK, &list)
	if len(list.Files) != 2 || list.Files[0].Name != "config" || list.Files[1].Name != "server.properties" {
		t.Fatalf("listing = %+v, want the file of rcon-cli hidden", list.Files)
	}
	api.do("GET", base+"/content"+q(".rcon-cli.env"), nil, http.StatusForbidden, nil)
	api.do("PUT", base+"/content"+q(".rcon-cli.env")+"&overwrite=true", []byte("password=mine\n"), http.StatusForbidden, nil)
	shown := api.do("GET", base+"/content"+q("server.properties"), nil, http.StatusOK, nil)
	if shown != "motd=hi\nrcon.password=<hidden>\n" {
		t.Fatalf("server.properties = %q", shown)
	}

	// Saving the file as shown keeps the secret.
	api.do("PUT", base+"/content"+q("server.properties")+"&overwrite=true", []byte("motd=changed\nrcon.password=<hidden>\n"), http.StatusCreated, nil)
	if saved, _ := os.ReadFile(filepath.Join(data, "server.properties")); string(saved) != "motd=changed\nrcon.password=s3cret\n" {
		t.Fatalf("saved server.properties = %q", saved)
	}

	// Files with secrets can't be moved where they would show, and archives leave them out.
	for _, from := range []string{"server.properties", "config", "config/paper-global.yml"} {
		api.do("POST", base+"/move", map[string]string{"from": from, "to": "moved"}, http.StatusForbidden, nil)
	}
	archive := api.do("GET", base+"/archive"+q(""), nil, http.StatusOK, nil)
	zr, err := zip.NewReader(bytes.NewReader([]byte(archive)), int64(len(archive)))
	check(t, err)
	archived := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		check(t, err)
		content, err := io.ReadAll(r)
		check(t, err)
		archived[f.Name] = string(content)
	}
	if want := map[string]string{
		"config/": "", "server.properties": "motd=changed\nrcon.password=<hidden>\n",
		"config/paper-global.yml": "proxies:\n  velocity:\n    secret: <hidden>\n",
	}; !maps.Equal(archived, want) {
		t.Fatalf("archive = %q, want %q", archived, want)
	}

	// A config folder without secrets, e.g. of mods, moves.
	check(t, os.Remove(filepath.Join(data, "config", "paper-global.yml")))
	api.do("POST", base+"/move", map[string]string{"from": "config", "to": "moved"}, http.StatusNoContent, nil)
}

// Uploads and backups that would leave less than 1 GB free on the node are refused before
// they start.
func TestFreeSpace(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	srv := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	conn, err := m.nodes.Conn(t.Context(), a.node.ID)
	check(t, err)
	upload, err := noryxv1.NewFileServiceClient(conn).WriteFile(t.Context())
	check(t, err)
	check(t, upload.Send(&noryxv1.WriteFileRequest{Content: &noryxv1.WriteFileRequest_Header{Header: &noryxv1.WriteFileHeader{
		ServerId: srv.ServerID, Path: "world.zip", Size: 1 << 50,
	}}}))
	if _, err := upload.CloseAndRecv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("upload of 1 PB: %v", err)
	}

	// A sparse file of 8 TB takes no space, but its backup would.
	f, err := os.Create(filepath.Join(a.runtime.dir, srv.ServerID, "huge.dat"))
	check(t, err)
	check(t, errors.Join(f.Truncate(8<<40), f.Close()))
	api := apiClient{t: t, url: m.panel(t).URL}
	api.do("POST", "/api/nodes/"+srv.NodeID+"/servers/"+srv.ServerID+"/backups", map[string]any{"selection": map[string]any{"everything": true}},
		http.StatusRequestEntityTooLarge, nil)
	// Nor would its copy, which is refused before anything is copied.
	_, err = noryxv1.NewServerServiceClient(conn).DuplicateServer(t.Context(), &noryxv1.DuplicateServerRequest{Id: srv.ServerID, Name: "Copy", Port: 25566})
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(status.Convert(err).Message(), "GB stays free") {
		t.Fatalf("duplicate of 8 TB: %v", err)
	}
	if servers := must(a.runtime.List(t.Context())); len(servers) != 1 {
		t.Fatalf("servers after the refused copy: %+v", servers)
	}
}
