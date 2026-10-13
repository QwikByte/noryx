package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/agent/storage"
)

// Meaning is what an error means to the calls of a service: the code and the text of the
// status that they answer with.
type Meaning struct {
	Err  error
	Code codes.Code
	Text string
}

// ErrEscapes stands for the error of os.Root about a path that leads outside of its folder,
// e.g. through a link, which has no variable to compare with.
var ErrEscapes = errors.New("path escapes from parent")

// shared are the meanings that all services give to errors.
var shared = []Meaning{{ErrNotFound, codes.NotFound, "Server not found."}}

// Status turns the error of a call into the status it answers with: a status as it is, an
// ended context as such, too little space and an unknown storage location with what to do,
// and then what the meanings of the service say of the error before those that all services
// share, e.g. of a server that doesn't exist. Anything else failed in the agent itself.
func Status(err error, meanings ...Meaning) error {
	switch {
	case err == nil:
		return nil
	case status.Code(err) != codes.Unknown: // e.g. a refused archive
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.As(err, new(storage.FullError)):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, storage.ErrUnknown):
		return status.Errorf(codes.InvalidArgument, "%s. Add it on the node with: noryx-agent storage add", err)
	}
	for _, m := range slices.Concat(meanings, shared) {
		if errors.Is(err, m.Err) || errors.Is(m.Err, ErrEscapes) && strings.Contains(err.Error(), ErrEscapes.Error()) {
			return status.Error(m.Code, m.Text)
		}
	}
	return status.Error(codes.Internal, err.Error())
}
