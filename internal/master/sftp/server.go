// Package sftp serves the files of servers over SFTP, for editors, scripts and clients such as
// WinSCP. Users sign in with their username and an API token, and find the servers whose files
// the token may read in a folder of each node. Every request goes through the agent of the
// server's node like those of the file manager, see files.Service, so the agent's checks apply.
package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/files"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	// A client has a minute to connect and sign in, which may be typing the token, and three
	// attempts to do so in a connection.
	signInTimeout = time.Minute
	maxAuthTries  = 3
	// A user has up to maxConns connections at once, each with up to maxOpen files open.
	maxConns = 16
	maxOpen  = 64
)

// Users signs users in with API tokens, see auth.Handler.SignInWithToken.
type Users interface {
	SignInWithToken(ctx context.Context, username, secret, ip string) (auth.User, error)
}

// Access tells what a user may do, with an API token only what it allows.
type Access interface {
	GrantsOf(ctx context.Context, user auth.User) (access.Grants, error)
}

// Deps are what SFTP needs of the master.
type Deps struct {
	Nodes  Nodes
	Users  Users
	Access Access
	// Moving refuses changes to a server while it moves to another node, as they would be lost.
	Moving func(serverID string) error
}

// Server serves SFTP with the host key it identifies with.
type Server struct {
	Deps
	key     ssh.Signer
	files   *files.Service
	tree    *tree
	started time.Time // when the folders of nodes and servers were modified, for clients

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	users    map[int64]int // connections per user
	closed   bool
}

func NewServer(key ssh.Signer, deps Deps) *Server {
	return &Server{
		Deps: deps, key: key, files: files.NewService(deps.Nodes), tree: newTree(deps.Nodes), started: time.Now(),
		conns: map[net.Conn]struct{}{}, users: map[int64]int{},
	}
}

// HostKey returns the key that SFTP identifies the master with, which it creates in dir the
// first time: Ed25519, in OpenSSH's format, readable by the master only.
func HostKey(dir string) (ssh.Signer, error) {
	file := filepath.Join(dir, "host_key")
	data, err := os.ReadFile(file) //nolint:gosec // in the master's data directory
	if errors.Is(err, fs.ErrNotExist) {
		data, err = createHostKey(dir, file)
	}
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

func createHostKey(dir, file string) ([]byte, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(block)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return data, os.WriteFile(file, data, 0o600)
}

// Fingerprint is the fingerprint of a host key as clients show it: SHA256: and its base64.
func Fingerprint(key ssh.Signer) string { return ssh.FingerprintSHA256(key.PublicKey()) }

// Serve serves the connections of ln until the server closes.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.listener = ln
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ln.Close()
	}
	for {
		c, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil { // e.g. too many open files, which passes
			slog.Warn("SFTP can't accept a connection", "err", err)
			time.Sleep(time.Second)
			continue
		}
		go s.serve(c)
	}
}

// Close stops listening and ends all connections.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// serve serves a connection: a client that signs in in time with a token gets SFTP, nothing
// else.
func (s *Server) serve(c net.Conn) {
	defer c.Close()
	if !s.track(c, true) {
		return
	}
	defer s.track(c, false)
	sess := &session{srv: s, ip: hostOf(c.RemoteAddr())}
	conf := &ssh.ServerConfig{ServerVersion: "SSH-2.0-Noryx", MaxAuthTries: maxAuthTries, PasswordCallback: sess.signIn}
	conf.AddHostKey(s.key)
	if c.SetDeadline(time.Now().Add(signInTimeout)) != nil {
		return
	}
	conn, chans, reqs, err := ssh.NewServerConn(c, conf)
	if err != nil {
		return
	}
	defer conn.Close()
	if c.SetDeadline(time.Time{}) != nil {
		return
	}
	if !s.admit(sess.user.ID, 1) {
		slog.LogAttrs(context.Background(), slog.LevelWarn, "Too many SFTP connections", sess.attrs(logging.Auth, slog.Int("limit", maxConns))...)
		return
	}
	defer s.admit(sess.user.ID, -1)
	sess.conn = conn
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "Only SFTP is served here.")
			continue
		}
		channel, requests, err := ch.Accept()
		if err == nil {
			wg.Go(func() { sess.serve(channel, requests) })
		}
	}
	wg.Wait()
}

// track adds or removes a connection, which Close ends; it refuses new ones once closed.
func (s *Server) track(c net.Conn, add bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !add {
		delete(s.conns, c)
		return true
	}
	if !s.closed {
		s.conns[c] = struct{}{}
	}
	return !s.closed
}

// admit counts a connection of a user, or one less with -1. It refuses one more than maxConns.
func (s *Server) admit(userID int64, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[userID]+n > maxConns {
		return false
	}
	if s.users[userID] += n; s.users[userID] == 0 {
		delete(s.users, userID)
	}
	return true
}

// session is a connection of a user who signed in with an API token.
type session struct {
	srv  *Server
	ip   string
	conn ssh.Conn

	user   auth.User // as the user signed in
	secret string    // the token, checked again for every request
	end    sync.Once

	open  atomic.Int32 // files
	early atomic.Int64 // see writer
}

// signIn signs a user in with an API token as password.
func (s *session) signIn(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	defer cancel()
	user, err := s.srv.Users.SignInWithToken(ctx, meta.User(), string(password), s.ip)
	if err != nil {
		slog.LogAttrs(ctx, slog.LevelWarn, "Sign in with SFTP failed", logging.Auth, slog.String(logging.KeyUser, meta.User()[:min(len(meta.User()), 64)]),
			slog.String("ip", s.ip), slog.String("reason", httpapi.Message(err)))
		return nil, err
	}
	s.user, s.secret = user, string(password)
	slog.LogAttrs(ctx, slog.LevelInfo, "Sign in with SFTP", s.attrs(logging.Auth)...)
	return &ssh.Permissions{}, nil
}

// serve serves SFTP on a channel, once the client asks for it; nothing else is offered.
func (s *session) serve(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		var subsystem struct{ Name string }
		ok := req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &subsystem) == nil && subsystem.Name == "sftp"
		_ = req.Reply(ok, nil)
		if ok {
			go ssh.DiscardRequests(requests)
			_ = sftp.NewRequestServer(ch, sftp.Handlers{FileGet: s, FilePut: s, FileCmd: s, FileList: s}).Serve()
			return
		}
	}
}

// authorize checks the token again for a request, as it may have been revoked or its user's
// permissions may have changed since the user signed in, and returns what it allows now. A
// token that no longer works ends the connection.
func (s *session) authorize(ctx context.Context) (auth.User, access.Grants, error) {
	user, err := s.srv.Users.SignInWithToken(ctx, s.user.Username, s.secret, s.ip)
	if err != nil {
		var apiErr *httpapi.Error
		if errors.As(err, &apiErr) && apiErr.Status != http.StatusTooManyRequests {
			s.end.Do(func() {
				slog.LogAttrs(ctx, slog.LevelWarn, "SFTP connection ended", s.attrs(logging.Auth, slog.String("reason", apiErr.Message))...)
				s.conn.Close()
			})
		}
		return user, access.Grants{}, toStatus(err)
	}
	g, err := s.srv.Access.GrantsOf(ctx, user)
	return user, g, toStatus(err)
}

// attrs are the attributes of the session's entries in the log, besides attrs.
func (s *session) attrs(attrs ...slog.Attr) []slog.Attr {
	return append(append(attrs, slog.String("ip", s.ip)), s.user.LogAttrs()...)
}

func hostOf(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
