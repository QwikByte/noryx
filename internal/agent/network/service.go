package network

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// Service implements the ProxyService of the agent, with which the panel edits the
// settings of proxies in their own configuration file.
type Service struct {
	noryxv1.UnimplementedProxyServiceServer
	rt runtime.Runtime
}

func NewService(rt runtime.Runtime) *Service { return &Service{rt: rt} }

func (s *Service) GetProxySettings(ctx context.Context, req *noryxv1.GetProxySettingsRequest) (*noryxv1.GetProxySettingsResponse, error) {
	_, p, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	res := &noryxv1.GetProxySettingsResponse{File: p.File}
	data, err := dir.ReadFile(p.File)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	settings, locked, err := p.Settings(data)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	res.Exists, res.Settings = true, settings
	for key, reason := range locked {
		res.Locked = append(res.Locked, &noryxv1.LockedProperty{Key: key, Reason: reason})
	}
	slices.SortFunc(res.Locked, func(a, b *noryxv1.LockedProperty) int { return cmp.Compare(a.GetKey(), b.GetKey()) })
	return res, nil
}

func (s *Service) UpdateProxySettings(ctx context.Context, req *noryxv1.UpdateProxySettingsRequest) (*noryxv1.UpdateProxySettingsResponse, error) {
	srv, p, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	data, err := dir.ReadFile(p.File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, status.Errorf(codes.FailedPrecondition, "Start the proxy once to create %s.", p.File)
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	config, err := p.Change(data, req.GetSettings())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := dir.WriteFile(p.File, config); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	running := srv.State == noryxv1.ServerState_SERVER_STATE_RUNNING
	if running {
		if err := s.rt.Reload(ctx, srv.ID); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "The settings were saved, but %s", err)
		}
	}
	return &noryxv1.UpdateProxySettingsResponse{Reloaded: running}, nil
}

// open finds a proxy and opens its data directory.
func (s *Service) open(ctx context.Context, id string) (runtime.Server, Proxy, *datadir.Dir, error) {
	if !runtime.ValidID(id) {
		return runtime.Server{}, Proxy{}, nil, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	if err != nil {
		return srv, Proxy{}, nil, grpcerr.Status(err)
	}
	p, ok := ProxyOf(srv.Type)
	if !ok {
		return srv, p, nil, status.Error(codes.FailedPrecondition, "Only proxies have these settings.")
	}
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return srv, p, nil, status.Error(codes.Internal, err.Error())
	}
	return srv, p, dir, nil
}
