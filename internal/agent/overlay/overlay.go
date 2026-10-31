// Package overlay joins the node to the private network of the nodes: a WireGuard interface
// over which proxies reach the backends of other nodes, whose ports the node publishes only
// there and only for the node of their proxy. The master configures it, but only on nodes
// whose administrator allowed it with the local CLI, and the checks of the agent keep the
// master from pulling other traffic of the node into the tunnel or opening the node's own
// services to it. The interface outlives the agent, and noryx-agent overlay up restores it
// at boot, before Docker or Podman starts the backends.
package overlay

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// Files in the folder of the network, in the agent's data directory.
const (
	allowedFile = "allowed"
	keyFile     = "private.key"
	stateFile   = "state.json"
)

var errNotAllowed = status.Error(codes.PermissionDenied, "The node's administrator hasn't allowed it to join the private network. Run on the node: noryx-agent overlay allow")

// Service implements the OverlayService of the agent.
type Service struct {
	noryxv1.UnimplementedOverlayServiceServer
	dir    string
	kernel Kernel
	mu     sync.Mutex // one change at a time
	// A kernel that runs WireGuard always does. Otherwise, the check, which creates an
	// interface, is repeated at most every minute.
	supported   bool
	checked     time.Time
	unsupported error
	// tests limits the connections that TestOverlayPeer opens.
	tests *rate.Limiter
}

func NewService(dataDir string, kernel Kernel) *Service {
	return &Service{dir: Dir(dataDir), kernel: kernel, tests: rate.NewLimiter(testRate, testBurst)}
}

// DatastorePrefix keeps the datastores apart from the servers among the clients: a
// datastore is admitted as DatastorePrefix and its ID, a server by its ID.
const DatastorePrefix = "db-"

// Dir returns the folder of the network in the agent's data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, "overlay") }

func (s *Service) GetOverlay(context.Context, *noryxv1.GetOverlayRequest) (*noryxv1.GetOverlayResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &noryxv1.GetOverlayResponse{Allowed: allowed(s.dir)}
	if !res.Allowed {
		return res, nil
	}
	key, err := s.key()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	res.PublicKey = key.PublicKey().String()
	st, err := s.state()
	switch {
	case err != nil:
		return nil, status.Error(codes.Internal, err.Error())
	case st == nil:
		if err := s.check(); err != nil {
			res.Unsupported = err.Error()
		}
		return res, nil
	}
	res.Address, res.Published = st.Address.String(), published(st.Clients)
	res.Firewall, res.FirewallProblem = firewallState(s.kernel.Firewall(*st))
	peers, err := s.kernel.Peers()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for _, p := range peers {
		res.Peers = append(res.Peers, &noryxv1.OverlayPeerStatus{
			PublicKey: p.PublicKey, Endpoint: p.Endpoint, ReceivedBytes: uint64(p.Received), SentBytes: uint64(p.Sent), //nolint:gosec // never negative
			LatestHandshakeUnix: max(p.LatestHandshake.Unix(), 0),
		})
	}
	return res, nil
}

// published returns the ports that clients reach, in their order.
func published(clients map[string]Client) []*noryxv1.OverlayPublished {
	list := make([]*noryxv1.OverlayPublished, 0, len(clients))
	for id, c := range clients {
		p := &noryxv1.OverlayPublished{Port: uint32(c.Port), ServerId: id}
		if ds, ok := strings.CutPrefix(id, DatastorePrefix); ok {
			p.ServerId, p.DatastoreId = "", ds
		}
		for i, a := range c.addresses() {
			client := &noryxv1.OverlayClient{Address: a.String()}
			if i < len(c.Keys) {
				client.PublicKey = c.Keys[i]
			}
			p.Clients = append(p.Clients, client)
		}
		list = append(list, p)
	}
	slices.SortFunc(list, func(a, b *noryxv1.OverlayPublished) int { return cmp.Compare(a.GetPort(), b.GetPort()) })
	return list
}

// firewallState tells the master about the firewall rules, as Kernel.Firewall found them.
func firewallState(err error) (noryxv1.OverlayFirewall, string) {
	switch {
	case err == nil:
		return noryxv1.OverlayFirewall_OVERLAY_FIREWALL_IN_PLACE, ""
	case errors.Is(err, ErrNoTable):
		return noryxv1.OverlayFirewall_OVERLAY_FIREWALL_MISSING, ""
	}
	return noryxv1.OverlayFirewall_OVERLAY_FIREWALL_INCOMPLETE, err.Error()
}

func (s *Service) ConfigureOverlay(ctx context.Context, req *noryxv1.ConfigureOverlayRequest) (*noryxv1.ConfigureOverlayResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !allowed(s.dir) {
		return nil, errNotAllowed
	}
	next, err := parse(ctx, req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	key, err := s.key()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	host, err := s.kernel.HostNets()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := next.check(key.PublicKey().String(), host); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	previous, err := s.state()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if previous != nil {
		next.Clients = next.peerClients(previous.Clients)
	}
	return &noryxv1.ConfigureOverlayResponse{}, s.apply(next, key, previous)
}

func (s *Service) RotateOverlayKey(context.Context, *noryxv1.RotateOverlayKeyRequest) (*noryxv1.RotateOverlayKeyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !allowed(s.dir) {
		return nil, errNotAllowed
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err == nil {
		err = writeFile(filepath.Join(s.dir, keyFile), []byte(key.String()))
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	st, err := s.state()
	if err == nil && st != nil {
		err = s.apply(*st, key, st)
	}
	return &noryxv1.RotateOverlayKeyResponse{PublicKey: key.PublicKey().String()}, err
}

func (s *Service) LeaveOverlay(context.Context, *noryxv1.LeaveOverlayRequest) (*noryxv1.LeaveOverlayResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.kernel.Remove(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.Remove(filepath.Join(s.dir, stateFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &noryxv1.LeaveOverlayResponse{}, nil
}

// Admit lets clients, the addresses in the network of the nodes of a server's proxy or of the
// servers that use a datastore, reach the port it publishes in the network, as the only ones,
// as long as their peers have the keys they have now. The master sends these keys, in the
// order of clients, to make sure that they are the nodes it means. It returns the node's
// address in the network, at which the port is published.
func (s *Service) Admit(id string, port uint32, clients, keys []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state()
	if err != nil {
		return "", err
	}
	if st == nil || len(clients) == 0 || port == 0 || port > 65535 {
		return "", errors.New("this node is no member of the private network")
	}
	if len(keys) != len(clients) {
		return "", errors.New("the master didn't send the key of each client")
	}
	addrs, bound := make([]netip.Addr, len(clients)), make([]string, len(clients))
	for i, client := range clients {
		addr, _ := netip.ParseAddr(client)
		bound[i] = st.key(addr)
		switch {
		case bound[i] == "" || slices.Contains(addrs[:i], addr):
			return "", fmt.Errorf("%s is no node of the private network of this node", client)
		case keys[i] != bound[i]:
			return "", fmt.Errorf("the key of %s in the private network of this node isn't the one the master sent: the master hasn't configured this node since it changed", client)
		}
		addrs[i] = addr
	}
	key, err := s.key()
	if err != nil {
		return "", err
	}
	previous := *st
	// A port belongs to one server or datastore; one that had it before is gone.
	updated := map[string]Client{id: newClient(uint16(port), addrs, bound)} //nolint:gosec // checked
	for other, c := range st.Clients {
		if other != id && c.Port != uint16(port) { //nolint:gosec // checked
			updated[other] = c
		}
	}
	st.Clients = updated
	return st.Address.Addr().String(), s.apply(*st, key, &previous)
}

// Dismiss closes the port of a server or datastore in the network to its clients, e.g. as it
// left its network or was deleted.
func (s *Service) Dismiss(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.state()
	if err != nil || st == nil {
		return err
	}
	if _, ok := st.Clients[id]; !ok {
		return nil
	}
	key, err := s.key()
	if err != nil {
		return err
	}
	previous := *st
	st.Clients = make(map[string]Client, len(previous.Clients))
	for other, c := range previous.Clients {
		if other != id {
			st.Clients[other] = c
		}
	}
	return s.apply(*st, key, &previous)
}

// check returns why the kernel can't run WireGuard, or nil if it can.
func (s *Service) check() error {
	if s.supported || time.Since(s.checked) < time.Minute {
		return s.unsupported
	}
	s.unsupported, s.checked = s.kernel.Check(), time.Now()
	s.supported = s.unsupported == nil
	return s.unsupported
}

// apply puts a state into effect and keeps it for the next boot.
func (s *Service) apply(st State, key wgtypes.Key, previous *State) error {
	if err := s.kernel.Apply(st, key, previous); err != nil {
		return status.Errorf(codes.FailedPrecondition, "The private network could not be configured: %v", err)
	}
	data, err := json.Marshal(st)
	if err == nil {
		err = writeFile(filepath.Join(s.dir, stateFile), data)
	}
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}

// state returns the state of the node in the network, or nil if it isn't a member.
func (s *Service) state() (*State, error) { return readState(s.dir) }

func readState(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile)) //nolint:gosec // in the data directory of the agent
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// key returns the node's private key, which it creates the first time. It never leaves the node.
func (s *Service) key() (wgtypes.Key, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, keyFile))
	if errors.Is(err, fs.ErrNotExist) {
		key, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return key, err
		}
		return key, writeFile(filepath.Join(s.dir, keyFile), []byte(key.String()))
	}
	if err != nil {
		return wgtypes.Key{}, err
	}
	return wgtypes.ParseKey(strings.TrimSpace(string(data)))
}

func allowed(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, allowedFile))
	return err == nil
}

// writeFile replaces a file atomically, readable by the agent's user only.
func writeFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// Allow lets the master add the node to the private network. Only the node's administrator
// allows it, with the local CLI.
func Allow(dataDir string) error {
	return writeFile(filepath.Join(Dir(dataDir), allowedFile), nil)
}

// Deny keeps the master from adding the node to the private network, and removes its key.
// A member leaves in the panel first, so that the networks reach its servers otherwise.
func Deny(dataDir string) error {
	dir := Dir(dataDir)
	if st, err := readState(dir); err != nil || st != nil {
		return cmp.Or(err, errors.New("this node is part of the private network, remove it in the panel first"))
	}
	for _, name := range []string{allowedFile, keyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Up restores the interface and firewall rules of a member, e.g. at boot before the runtime
// starts the containers whose ports are published in the network.
func Up(dataDir string, kernel Kernel) error {
	s := NewService(dataDir, kernel)
	st, err := s.state()
	if err != nil || st == nil || !allowed(s.dir) {
		return err
	}
	key, err := s.key()
	if err != nil {
		return err
	}
	return kernel.Apply(*st, key, st)
}
