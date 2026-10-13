package logs

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
)

// Origins of calls: the master over its mutually authenticated connection, or the CLI on the node.
const (
	FromMaster = "master"
	FromLocal  = "local"
)

// detailFields are the request fields that are logged besides the server ID and the command
// of a console, without what may be a password. Others may hold secrets, such as the
// forwarding secret of a network, or file contents.
var detailFields = []protoreflect.Name{"name", "version", "path", "from", "to", "file_name", "replaces", "backup_id", "label", "location", "job_id", "set_id", "set_name", "dump_id"}

var categories = map[string]slog.Attr{
	"NodeService": logging.Nodes, "ServerService": logging.Servers, "FileService": logging.Files,
	"PropertiesService": logging.Files, "PluginService": logging.Plugins, "BackupService": logging.Backups,
	"ProxyService": logging.Files, "FileSetService": logging.Files, "DatastoreService": logging.Databases,
}

var wordStart = regexp.MustCompile(`([a-z])([A-Z])`)

// Interceptors log every call with its origin to log. Calls that only read are logged at
// the debug level, except downloads; failed calls as warnings, or errors if the agent
// failed itself.
func Interceptors(origin string, log *slog.Logger) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			start := time.Now()
			res, err := handler(ctx, req)
			logCall(ctx, log, origin, info.FullMethod, req, start, err)
			return res, err
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if info.FullMethod == noryxv1.LogService_ReadLog_FullMethodName {
				return handler(srv, ss) // reading the log would add to it
			}
			rs, start := &recording{ServerStream: ss}, time.Now()
			err := handler(srv, rs)
			logCall(ss.Context(), log, origin, info.FullMethod, rs.first, start, err)
			return err
		}),
	}
}

// recording keeps the first message of a stream, which names the server.
type recording struct {
	grpc.ServerStream
	first any
}

func (r *recording) RecvMsg(m any) error {
	err := r.ServerStream.RecvMsg(m)
	if r.first == nil && err == nil {
		r.first = m
	}
	return err
}

func logCall(ctx context.Context, log *slog.Logger, origin, method string, req any, start time.Time, err error) {
	service, name, _ := strings.Cut(strings.TrimPrefix(method, "/noryx.v1."), "/")
	code := status.Code(err)
	level := slog.LevelInfo
	switch {
	case code == codes.Internal || code == codes.Unknown || code == codes.DataLoss:
		level = slog.LevelError
	case err != nil && code != codes.Canceled: // following a console ends with Canceled
		level = slog.LevelWarn
	case strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") || strings.HasPrefix(name, "Stream"):
		level = slog.LevelDebug
	}
	if !log.Enabled(ctx, level) {
		return
	}
	category, ok := categories[service]
	switch {
	case name == "SendCommand" || name == "StreamLogs":
		category = logging.Console
	case !ok:
		category = logging.System
	}
	message := describe(name)
	attrs := []slog.Attr{category, slog.String("origin", origin), slog.Duration("duration", time.Since(start).Round(time.Millisecond))}
	if p, ok := peer.FromContext(ctx); ok && origin == FromMaster {
		attrs = append(attrs, slog.String("peer", p.Addr.String()))
	}
	if m, ok := req.(proto.Message); ok {
		idKey := logging.KeyServer
		if service == "DatastoreService" {
			idKey = "datastore"
		}
		attrs = details(m.ProtoReflect(), attrs, idKey, true)
	}
	if err != nil && code != codes.Canceled {
		message += " failed"
		attrs = append(attrs, slog.String("code", code.String()), slog.String("err", status.Convert(err).Message()))
	}
	log.LogAttrs(ctx, level, message, attrs...)
}

// describe turns the name of a call into words, e.g. "Create backup" for CreateBackup.
func describe(name string) string {
	words := strings.Fields(wordStart.ReplaceAllString(name, "$1 $2"))
	for i, w := range words {
		if i > 0 && strings.ToUpper(w) != w {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

// details adds the ID, of a server unless idKey names another, and the detail fields of a
// request, also those of a header message such as the one that starts an upload.
func details(m protoreflect.Message, attrs []slog.Attr, idKey string, nested bool) []slog.Attr {
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case f.IsList() || f.IsMap():
		case f.Kind() == protoreflect.MessageKind && nested:
			attrs = details(v.Message(), attrs, idKey, false)
		case f.Kind() != protoreflect.StringKind:
		case f.Name() == "id" || f.Name() == "server_id":
			attrs = append(attrs, slog.String(idKey, v.String()))
		case f.Name() == "command":
			attrs = append(attrs, logging.Command(v.String()))
		case slices.Contains(detailFields, f.Name()):
			attrs = append(attrs, slog.String(string(f.Name()), v.String()))
		}
		return true
	})
	return attrs
}
