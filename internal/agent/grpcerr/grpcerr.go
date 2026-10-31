// Package grpcerr turns the errors of the agent into the statuses that its services answer
// with, so that operators read what went wrong rather than an internal error.
package grpcerr

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// Known is an error that a service answers with a code and a text for the operator.
type Known struct {
	Err  error // matched with errors.Is
	Code codes.Code
	Text string
}

// common are the errors that all services answer the same way, after their own.
var common = []Known{
	{Err: runtime.ErrNotFound, Code: codes.NotFound, Text: "Server not found."},
}

// Status returns err as a status: a status as it is, e.g. of a refused archive; the first of
// the known and then of the common errors that err is with its code and text; a full storage,
// an unknown storage location and a cancelled call with their own texts; and anything else
// as an internal error.
func Status(err error, known ...Known) error {
	if status.Code(err) != codes.Unknown { // also nil
		return err
	}
	for _, k := range slices.Concat(known, common) {
		if errors.Is(err, k.Err) {
			return status.Error(k.Code, k.Text)
		}
	}
	switch {
	case errors.As(err, new(storage.FullError)):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, storage.ErrUnknown):
		return status.Errorf(codes.InvalidArgument, "%s. Add it on the node with: noryx-agent storage add", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Internal, err.Error())
}
