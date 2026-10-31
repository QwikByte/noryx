// Package properties implements the PropertiesService of the agent, with which the
// panel edits the server.properties of game servers.
package properties

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	file       = "server.properties"
	maxChanges = 200
	maxValue   = 4096
)

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Secret properties are never sent to the panel, nor changed through its editor.
var Secret = noryxv1.SecretProperties

// locked returns the properties the manager sets itself, with the reason; changing
// them would break the server or its console.
func locked(spec runtime.Spec) map[string]string {
	l := maps.Clone(noryxv1.ManagedProperties)
	if spec.BehindProxy {
		l["online-mode"] = "The proxy of the server's network authenticates players."
	}
	if spec.BedrockPlayers {
		l["enforce-secure-profile"] = "Bedrock players join through the proxy of the server's network, and they can't sign their chat messages."
	}
	return l
}

type Service struct {
	noryxv1.UnimplementedPropertiesServiceServer
	rt runtime.Runtime
}

func NewService(rt runtime.Runtime) *Service { return &Service{rt: rt} }

func (s *Service) GetServerProperties(ctx context.Context, req *noryxv1.GetServerPropertiesRequest) (*noryxv1.GetServerPropertiesResponse, error) {
	spec, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	data, err := dir.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	res := &noryxv1.GetServerPropertiesResponse{Exists: err == nil, Properties: map[string]string{}}
	for _, e := range parse(string(data)) {
		if e.key != "" && !Secret[e.key] {
			res.Properties[e.key] = e.value
		}
	}
	for key, reason := range locked(spec) {
		res.Locked = append(res.Locked, &noryxv1.LockedProperty{Key: key, Reason: reason})
	}
	slices.SortFunc(res.Locked, func(a, b *noryxv1.LockedProperty) int { return cmp.Compare(a.GetKey(), b.GetKey()) })
	return res, nil
}

func (s *Service) UpdateServerProperties(ctx context.Context, req *noryxv1.UpdateServerPropertiesRequest) (*noryxv1.UpdateServerPropertiesResponse, error) {
	spec, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if msg := Check(spec, req.GetProperties()); msg != "" {
		return nil, status.Error(codes.InvalidArgument, msg)
	}
	if _, err := dir.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return nil, status.Error(codes.FailedPrecondition, "Start the server once to create server.properties.")
	}
	if err := Write(dir, req.GetProperties()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &noryxv1.UpdateServerPropertiesResponse{}, nil
}

// Check returns a message for the operator if the properties can't be set on a server.
func Check(spec runtime.Spec, changes map[string]string) string {
	if spec.Type.Proxy() && len(changes) > 0 {
		return "Proxies have no server.properties."
	}
	return validate(changes, locked(spec))
}

// Write sets properties in server.properties, which is created if it doesn't exist yet,
// e.g. for a new server.
func Write(dir *datadir.Dir, changes map[string]string) error {
	data, err := dir.ReadOptional(file)
	if err != nil {
		return err
	}
	return dir.WriteFile(file, []byte(update(parse(string(data)), changes)))
}

// Read returns all properties of server.properties, also the secret ones, for the agent's
// own use. A missing file has none.
func Read(dir *datadir.Dir) (map[string]string, error) {
	data, err := dir.ReadOptional(file)
	props := map[string]string{}
	for _, e := range parse(string(data)) {
		if e.key != "" {
			props[e.key] = e.value
		}
	}
	return props, err
}

// validate returns a message for the operator if the changes are invalid.
func validate(changes, locked map[string]string) string {
	if len(changes) > maxChanges {
		return "Too many changes at once."
	}
	for key, value := range changes {
		switch {
		case !keyPattern.MatchString(key):
			return "Invalid property name " + key + "."
		case locked[key] != "":
			return key + " can't be changed: " + locked[key]
		case Secret[key]:
			return key + " can only be changed in the file manager."
		case len(value) > maxValue || strings.ContainsFunc(value, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }):
			return "The value of " + key + " is too long or contains control characters."
		}
	}
	return ""
}

// open finds a game server and opens its data directory.
func (s *Service) open(ctx context.Context, id string) (runtime.Spec, *datadir.Dir, error) {
	if !runtime.ValidID(id) {
		return runtime.Spec{}, nil, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	if err != nil {
		return runtime.Spec{}, nil, grpcerr.Status(err)
	}
	spec := srv.Spec
	if spec.Type.Proxy() {
		return spec, nil, status.Error(codes.FailedPrecondition, "Proxies have no server.properties.")
	}
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return spec, nil, status.Error(codes.Internal, err.Error())
	}
	return spec, dir, nil
}
