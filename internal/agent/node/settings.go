package node

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/logging"
)

// Measurer measures what the node uses, at an interval that the settings choose.
type Measurer interface{ SetInterval(time.Duration) }

// Settings are what the master chose for the node and the agent applies itself: how much
// output the runtime keeps of each container, the space kept free and the interval of
// measurements. They are kept in the data directory, so that they apply again as soon as
// the agent restarts.
type Settings struct {
	file     string
	rt       runtime.Runtime
	measurer Measurer

	mu      sync.Mutex
	current *noryxv1.NodeSettings
}

// LoadSettings applies the settings kept in the data directory, or the defaults until the
// master sends some.
func LoadSettings(dataDir string, rt runtime.Runtime, measurer Measurer) *Settings {
	s := &Settings{file: filepath.Join(dataDir, "settings.json"), rt: rt, measurer: measurer}
	settings, err := s.read()
	if err != nil {
		slog.Warn("Can't read the settings of the node, so the defaults apply until the master sends them", logging.Nodes, "err", err)
		settings = noryxv1.DefaultNodeSettings()
	}
	s.apply(settings)
	return s
}

func (s *Settings) read() (*noryxv1.NodeSettings, error) {
	data, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return noryxv1.DefaultNodeSettings(), nil
	}
	settings := &noryxv1.NodeSettings{}
	if err == nil {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, settings)
	}
	if problem := settings.Problem(); err == nil && problem != "" {
		err = errors.New(problem)
	}
	return settings, err
}

// Get returns the settings that apply.
func (s *Settings) Get() *noryxv1.NodeSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(s.current)
}

// set keeps settings, which are within their bounds, and applies them.
func (s *Settings) set(settings *noryxv1.NodeSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := protojson.Marshal(settings)
	if err == nil {
		err = os.WriteFile(s.file+".tmp", data, 0o600)
	}
	if err == nil {
		err = os.Rename(s.file+".tmp", s.file)
	}
	if err == nil {
		s.apply(settings)
	}
	return err
}

// apply makes the agent follow settings: containers created from now on keep that much output,
// uploads and backups leave the space free, and the next measurement follows after the interval.
func (s *Settings) apply(settings *noryxv1.NodeSettings) {
	s.current = proto.CloneOf(settings)
	s.rt.LimitLogs(settings.GetLogFileMb(), settings.GetLogFiles())
	storage.KeepFree(uint64(settings.GetKeepFreeGb()) << 30)
	s.measurer.SetInterval(time.Duration(settings.GetStatsIntervalSeconds()) * time.Second)
}

// Configure implements noryxv1.NodeServiceServer. The agent checks the bounds of the settings
// itself, so that not even a compromised master can keep less than 1 GiB free.
func (s *Service) Configure(_ context.Context, req *noryxv1.ConfigureRequest) (*noryxv1.ConfigureResponse, error) {
	settings := req.GetSettings()
	if problem := settings.Problem(); problem != "" {
		return nil, status.Error(codes.InvalidArgument, problem)
	}
	if err := s.settings.set(settings); err != nil {
		return nil, status.Errorf(codes.Internal, "can't keep the settings of the node: %v", err)
	}
	slog.Info("Apply the settings of the node", logging.Nodes, "log_file_mb", settings.GetLogFileMb(), "log_files", settings.GetLogFiles(),
		"keep_free_gb", settings.GetKeepFreeGb(), "stats_interval_seconds", settings.GetStatsIntervalSeconds())
	return &noryxv1.ConfigureResponse{}, nil
}
