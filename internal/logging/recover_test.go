package logging

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A call that panics fails with an internal error instead of ending the program.
func TestRecover(t *testing.T) {
	_, err := recoverUnary(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/noryx.v1.Test/Unary"},
		func(context.Context, any) (any, error) { panic("bug") })
	if status.Code(err) != codes.Internal {
		t.Errorf("unary call: %v", err)
	}
	err = recoverStream(nil, nil, &grpc.StreamServerInfo{FullMethod: "/noryx.v1.Test/Stream"}, func(any, grpc.ServerStream) error { panic("bug") })
	if status.Code(err) != codes.Internal {
		t.Errorf("stream: %v", err)
	}
	if res, err := recoverUnary(t.Context(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return 1, nil }); res != 1 || err != nil {
		t.Errorf("call without a panic = %v, %v", res, err)
	}
}
