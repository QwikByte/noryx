// Package node manages the machines running an agent: registration, enrollment
// and the mutually authenticated connections the master uses to command them.
package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/enrollment"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
	"github.com/QwikByte/noryx/internal/pki"
)

// reconnect caps the retry delay, so a node shows up soon after its agent (re)starts.
var reconnect = grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: time.Second, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 10 * time.Second}}

var errNotFound = httpapi.Errorf(http.StatusNotFound, "Node not found.")

type Node struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Address    string     `json:"address,omitempty"`
	EnrolledAt *time.Time `json:"enrolledAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// CertificateExpiresAt is when the latest certificate of the node that the master issued or
	// saw expires, also while the node is offline.
	CertificateExpiresAt *time.Time `json:"certificateExpiresAt,omitempty"`
	Settings
}

// Settings apply to the servers of a node.
type Settings struct {
	// DefaultStorage is the storage location preselected for new servers.
	DefaultStorage string `json:"defaultStorage"`
	Limits
	Agent
}

// Limits restrict the servers of a node. New nodes get the defaults of the master's settings.
type Limits struct {
	// PortMin and PortMax limit the ports of servers; nil allows any port.
	PortMin *uint32 `json:"portMin"`
	PortMax *uint32 `json:"portMax"`
	// MemoryReserveMB is kept free of server memory for the system; nil allows
	// assigning more memory than the node has.
	MemoryReserveMB *uint32 `json:"memoryReserveMb"`
}

// Validate checks the port range and the memory reserve.
func (l Limits) Validate() error {
	switch {
	case (l.PortMin == nil) != (l.PortMax == nil),
		l.PortMin != nil && (*l.PortMin < 1024 || *l.PortMax > 65535 || *l.PortMin > *l.PortMax):
		return httpapi.FieldErrorf(http.StatusBadRequest, "portMin", "Enter a port range from 1024 to 65535, or none.")
	case l.MemoryReserveMB != nil && *l.MemoryReserveMB > 1<<20:
		return httpapi.FieldErrorf(http.StatusBadRequest, "memoryReserveMb", "Enter the memory to keep free in MB.")
	}
	return nil
}

// Agent holds the settings of a node that its agent applies itself, within the bounds of
// noryxv1.NodeSettings. The master sends them whenever the agent reports others, e.g. as it was
// offline when they changed.
type Agent struct {
	// LogFileMB and LogFiles limit how much output the runtime keeps of each container created
	// from now on; 0 for both leaves it to the runtime's configuration.
	LogFileMB uint32 `json:"logFileMb"`
	LogFiles  uint32 `json:"logFiles"`
	// KeepFreeGB is the space that uploads and backups leave free on the node.
	KeepFreeGB uint32 `json:"keepFreeGb"`
	// StatsIntervalSeconds is the time between two measurements of what the node uses.
	StatsIntervalSeconds uint32 `json:"statsIntervalSeconds"`
}

func agentOf(s *noryxv1.NodeSettings) Agent {
	return Agent{s.GetLogFileMb(), s.GetLogFiles(), s.GetKeepFreeGb(), s.GetStatsIntervalSeconds()}
}

func (a Agent) proto() *noryxv1.NodeSettings {
	return &noryxv1.NodeSettings{LogFileMb: a.LogFileMB, LogFiles: a.LogFiles, KeepFreeGb: a.KeepFreeGB, StatsIntervalSeconds: a.StatsIntervalSeconds}
}

// Config holds the settings of the master that concern nodes. They can change at any time.
type Config interface {
	// EnrollAddr is the host:port join tokens tell agents to enroll at.
	EnrollAddr() string
	JoinTokenTTL() time.Duration
	NodeDefaults() Limits
}

const nodeColumns = `id, name, address, enrolled_at, created_at, certificate_expires_at, default_storage, port_min, port_max, memory_reserve_mb,
	log_file_mb, log_files, keep_free_gb, stats_interval_seconds`

// StorageName matches the names of storage locations, which agents allow.
var StorageName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type Service struct {
	noryxv1.UnimplementedEnrollmentServiceServer

	db     *sql.DB
	ca     *pki.CA
	cert   *pki.Holder // the master's own certificate
	config Config

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
	// gen counts the changes and removals of connections, so that Conn doesn't store a
	// connection to a node that changed while it was loaded.
	gen uint64
	// expiries are expiries of node certificates that are stored already, so that checking a
	// node writes to the database only once it presents a newer certificate.
	expiries map[string]time.Time
	renewMu  sync.Mutex // one certificate renewal at a time
	// enrolls throttles enrollments per client, as anyone who reaches the endpoint may try.
	enrolls *ratelimit.Limiter
}

// A client has 5 enrollment attempts, then one every 12 seconds.
const enrollBurst, enrollEvery = 5, 12 * time.Second

func NewService(db *sql.DB, ca *pki.CA, cert *pki.Holder, config Config) *Service {
	return &Service{
		db: db, ca: ca, cert: cert, config: config, conns: make(map[string]*grpc.ClientConn), expiries: map[string]time.Time{},
		enrolls: ratelimit.New(enrollBurst, enrollEvery),
	}
}

// JoinToken lets the agent of a node enroll once until it expires.
type JoinToken struct {
	enrollment.Token
	ExpiresAt time.Time
}

// InstallCommand installs the agent in the master's version on a node, or updates it, and
// enrolls it with the token.
func (t JoinToken) InstallCommand() string {
	return fmt.Sprintf("curl -fsSLO %s && sudo bash install.sh agent --join %s", buildinfo.InstallScript(), t)
}

// Create registers a node with the default limits and returns the join token its agent enrolls with.
func (s *Service) Create(ctx context.Context, name, address string) (Node, JoinToken, error) {
	n := Node{
		ID: strings.ToLower(rand.Text()), Name: strings.TrimSpace(name), Address: strings.TrimSpace(address), CreatedAt: time.Now(),
		Settings: Settings{DefaultStorage: "default", Limits: s.config.NodeDefaults(), Agent: agentOf(noryxv1.DefaultNodeSettings())},
	}
	if err := validate(n); err != nil {
		return n, JoinToken{}, err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes (id, name, address, created_at, default_storage, port_min, port_max, memory_reserve_mb,
			log_file_mb, log_files, keep_free_gb, stats_interval_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.Address, n.CreatedAt.Unix(), n.DefaultStorage, n.PortMin, n.PortMax, n.MemoryReserveMB,
		n.LogFileMB, n.LogFiles, n.KeepFreeGB, n.StatsIntervalSeconds)
	if err != nil {
		return n, JoinToken{}, uniqueName(err, n.Name)
	}
	token, err := s.NewJoinToken(ctx, n.ID)
	return n, token, err
}

// Update changes the name, address and settings of a node. A new address applies to
// the next connection; the node keeps its identity.
func (s *Service) Update(ctx context.Context, n Node) (Node, error) {
	n.Name, n.Address = strings.TrimSpace(n.Name), strings.TrimSpace(n.Address)
	if err := validate(n); err != nil {
		return n, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET name = ?, address = ?, default_storage = ?, port_min = ?, port_max = ?, memory_reserve_mb = ?,
			log_file_mb = ?, log_files = ?, keep_free_gb = ?, stats_interval_seconds = ?
		WHERE id = ?`, n.Name, n.Address, n.DefaultStorage, n.PortMin, n.PortMax, n.MemoryReserveMB,
		n.LogFileMB, n.LogFiles, n.KeepFreeGB, n.StatsIntervalSeconds, n.ID)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errNotFound
	}
	if err != nil {
		return n, uniqueName(err, n.Name)
	}
	s.mu.Lock()
	s.gen++
	if conn, ok := s.conns[n.ID]; ok && conn.Target() != n.Address {
		conn.Close()
		delete(s.conns, n.ID)
	}
	s.mu.Unlock()
	return s.Get(ctx, n.ID)
}

func validate(n Node) error {
	switch {
	case n.Name == "" || len(n.Name) > 64:
		return httpapi.Errorf(http.StatusBadRequest, "Enter a name with up to 64 characters.")
	case !validAddress(n.Address):
		return httpapi.Errorf(http.StatusBadRequest, "Enter the agent address as host:port, for example 203.0.113.10:7443.")
	case n.DefaultStorage != "" && !StorageName.MatchString(n.DefaultStorage):
		return httpapi.Errorf(http.StatusBadRequest, "Choose a storage location of the node.")
	case n.Agent.proto().Problem() != "":
		return httpapi.Errorf(http.StatusBadRequest, "%s", n.Agent.proto().Problem())
	}
	return n.Validate()
}

func validAddress(address string) bool {
	_, _, err := net.SplitHostPort(address)
	return err == nil
}

func uniqueName(err error, name string) error {
	if strings.Contains(err.Error(), "UNIQUE") {
		return httpapi.Errorf(http.StatusConflict, "A node named %q already exists.", name)
	}
	return err
}

// NewJoinToken replaces the pending join token of a node, e.g. to reinstall its agent.
func (s *Service) NewJoinToken(ctx context.Context, id string) (JoinToken, error) {
	secret := rand.Text()
	hash := sha256.Sum256([]byte(secret))
	expires := time.Now().Add(s.config.JoinTokenTTL())
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET join_secret_hash = ?, join_expires_at = ? WHERE id = ?`,
		hash[:], expires.Unix(), id)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errNotFound
	}
	token := enrollment.Token{Master: s.config.EnrollAddr(), NodeID: id, Secret: secret, CAFingerprint: pki.Fingerprint(s.ca.Cert)}
	return JoinToken{token, expires}, err
}

// EnrollmentServer returns the gRPC server of the enrollment port, which is open to the
// internet and serves only Enroll: one call of a few hundred bytes, after which an agent goes
// away. So its connections are bounded: the TLS handshake gets 10 seconds, a connection 4
// calls at once of up to 64 KiB, a minute without calls and 5 minutes in all, and a client
// that doesn't answer pings for 20 seconds is cut off, as is one that pings more than every 5
// minutes, which agents never do here.
func (s *Service) EnrollmentServer() *grpc.Server {
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(pki.MasterServerTLS(s.cert))),
		grpc.ConnectionTimeout(10*time.Second),
		grpc.MaxConcurrentStreams(4),
		grpc.MaxRecvMsgSize(64<<10),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: time.Minute, MaxConnectionAge: 5 * time.Minute, MaxConnectionAgeGrace: 30 * time.Second,
			Time: time.Minute, Timeout: 20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Minute}),
	)
	noryxv1.RegisterEnrollmentServiceServer(srv, s)
	return srv
}

// Enroll implements noryxv1.EnrollmentServiceServer. The join token is consumed atomically.
func (s *Service) Enroll(ctx context.Context, req *noryxv1.EnrollRequest) (*noryxv1.EnrollResponse, error) {
	// Anyone who reaches the enrollment endpoint can send IDs, so they are kept short.
	attrs := []any{logging.Nodes, logging.KeyNode, req.GetNodeId()[:min(len(req.GetNodeId()), 64)]}
	var client string
	if p, ok := peer.FromContext(ctx); ok {
		attrs = append(attrs, "ip", p.Addr.String())
		client, _, _ = net.SplitHostPort(p.Addr.String())
	}
	if !s.enrolls.Allow(ratelimit.Client(client)) {
		return nil, status.Error(codes.ResourceExhausted, "too many enrollment attempts, wait a minute and try again")
	}
	// The join token is used up only if the master signs the certificate, and checked first.
	var cert []byte
	err := database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		hash := sha256.Sum256([]byte(req.GetSecret()))
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx, `
			UPDATE nodes SET join_secret_hash = NULL, join_expires_at = NULL, enrolled_at = ?
			WHERE id = ? AND join_secret_hash = ? AND join_expires_at > ?`, now, req.GetNodeId(), hash[:], now)
		if err == nil && database.RowsAffected(res) != 1 {
			return status.Error(codes.PermissionDenied, "join token is invalid, expired or already used")
		}
		if err == nil {
			if cert, err = s.ca.SignNodeCSR(req.GetCsrDer(), req.GetNodeId()); err != nil {
				return status.Error(codes.InvalidArgument, "invalid certificate signing request")
			}
		}
		return err
	})
	if err != nil {
		slog.Warn("Enroll node failed", append(attrs, "err", status.Convert(err).Message())...)
		return nil, err
	}
	slog.Info("Enroll node", attrs...)
	if c, err := x509.ParseCertificate(cert); err == nil {
		s.seen(ctx, req.GetNodeId(), c.NotAfter)
	}
	return &noryxv1.EnrollResponse{CertificateDer: cert, CaCertificateDer: s.ca.Cert.Raw}, nil
}

func (s *Service) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (Node, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	return n, err
}

// Delete removes a node. Its agent keeps running but is no longer contacted.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	if conn, ok := s.conns[id]; ok {
		conn.Close()
		delete(s.conns, id)
	}
	delete(s.expiries, id)
	return err
}

// Conn returns the mutually authenticated connection to the agent of an enrolled node.
// The node is loaded without holding s.mu, so that calls for other nodes don't wait for the
// database.
func (s *Service) Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error) {
	for {
		s.mu.Lock()
		conn, ok := s.conns[id]
		gen := s.gen
		s.mu.Unlock()
		if ok {
			return conn, nil
		}
		n, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if n.EnrolledAt == nil {
			return nil, httpapi.Errorf(http.StatusConflict, "Node %q has not been enrolled yet.", n.Name)
		}
		creds := credentials.NewTLS(pki.NodeClientTLS(s.cert, s.ca.Cert, n.ID))
		conn, err = grpc.NewClient(n.Address, append(s.explained(id), grpc.WithTransportCredentials(creds), grpc.WithConnectParams(reconnect))...)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		existing, ok := s.conns[id]
		stale := s.gen != gen
		if !ok && !stale {
			s.conns[id] = conn
		}
		s.mu.Unlock()
		switch {
		case ok: // another call was faster
			conn.Close()
			return existing, nil
		case stale: // the node changed or was removed meanwhile, so it's loaded again
			conn.Close()
			continue
		}
		return conn, nil
	}
}

// Close closes all agent connections.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	for id, conn := range s.conns {
		conn.Close()
		delete(s.conns, id)
	}
}

func scanNode(row database.Scanner) (Node, error) {
	var n Node
	var enrolledAt, certExpiresAt sql.NullInt64
	var createdAt int64
	var portMin, portMax, reserve sql.Null[uint32]
	if err := row.Scan(&n.ID, &n.Name, &n.Address, &enrolledAt, &createdAt, &certExpiresAt, &n.DefaultStorage, &portMin, &portMax, &reserve,
		&n.LogFileMB, &n.LogFiles, &n.KeepFreeGB, &n.StatsIntervalSeconds); err != nil {
		return n, err
	}
	n.CreatedAt = time.Unix(createdAt, 0)
	n.EnrolledAt, n.CertificateExpiresAt = unixTime(enrolledAt), unixTime(certExpiresAt)
	n.PortMin, n.PortMax, n.MemoryReserveMB = nullable(portMin), nullable(portMax), nullable(reserve)
	return n, nil
}

func unixTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	return new(time.Unix(v.Int64, 0))
}

func nullable[T any](v sql.Null[T]) *T {
	if !v.Valid {
		return nil
	}
	return &v.V
}
