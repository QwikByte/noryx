// Package node implements the NodeService of the agent.
package node

import (
	"context"
	"log/slog"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
)

type Service struct {
	noryxv1.UnimplementedNodeServiceServer
	rt       runtime.Runtime
	identity *Identity
	storage  *storage.Locations
	settings *Settings
	// Prereleases lets the master update the node to pre-releases too, which the node's
	// administrator allows with --prereleases.
	Prereleases bool
}

func NewService(rt runtime.Runtime, identity *Identity, locations *storage.Locations, settings *Settings) *Service {
	return &Service{rt: rt, identity: identity, storage: locations, settings: settings}
}

// GetInfo succeeds even if the runtime is down, so the panel can tell an
// unreachable agent apart from a broken container runtime.
func (s *Service) GetInfo(ctx context.Context, _ *noryxv1.GetInfoRequest) (*noryxv1.GetInfoResponse, error) {
	hostname, _ := os.Hostname()
	res := &noryxv1.GetInfoResponse{
		AgentVersion: buildinfo.Version, Hostname: hostname, Runtime: noryxv1.RuntimeUnavailable, RuntimeName: s.rt.Name(),
		CertificateNotAfterUnix: s.identity.Get().Leaf.NotAfter.Unix(), Settings: s.settings.Get(),
	}
	if info, err := s.rt.Info(ctx); err == nil {
		res.Os, res.CpuCount, res.MemoryBytes, res.RuntimeVersion = info.OS, info.CPUs, info.MemoryBytes, info.Version
		res.Runtime = res.RuntimeName + " " + info.Version // for older masters
	}
	locations, err := s.storage.List()
	if err != nil {
		slog.Warn("Can't read the storage locations", logging.Nodes, "err", err)
	}
	for _, l := range locations {
		res.Storage = append(res.Storage, &noryxv1.StorageLocation{Name: l.Name, Path: l.Path, FreeBytes: l.FreeBytes, TotalBytes: l.TotalBytes})
	}
	return res, nil
}

func (s *Service) CreateCSR(context.Context, *noryxv1.CreateCSRRequest) (*noryxv1.CreateCSRResponse, error) {
	csr, err := s.identity.createCSR()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &noryxv1.CreateCSRResponse{CsrDer: csr}, nil
}

func (s *Service) InstallCertificate(_ context.Context, req *noryxv1.InstallCertificateRequest) (*noryxv1.InstallCertificateResponse, error) {
	if err := s.identity.install(req.GetCertificateDer()); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "certificate rejected: %v", err)
	}
	slog.Info("Install renewed node certificate", logging.Nodes, "not_after", s.identity.Get().Leaf.NotAfter)
	return &noryxv1.InstallCertificateResponse{}, nil
}
