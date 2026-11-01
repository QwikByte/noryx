package logging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Recover returns the options of a gRPC server that turn the panic of a call into an internal
// error and log it with its stack, so that a bug in one call doesn't end the program and every
// other call with it. Net/http does the same for the requests of the panel.
func Recover() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(recoverUnary), grpc.ChainStreamInterceptor(recoverStream)}
}

func recoverUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ any, err error) {
	defer recovered(info.FullMethod, &err)
	return handler(ctx, req)
}

func recoverStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer recovered(info.FullMethod, &err)
	return handler(srv, ss)
}

func recovered(method string, err *error) {
	if p := recover(); p != nil {
		slog.Error("A call failed with a panic", System, "method", method, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		*err = status.Error(codes.Internal, "internal error")
	}
}
