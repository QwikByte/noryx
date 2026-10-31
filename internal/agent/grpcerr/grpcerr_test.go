package grpcerr

import (
	"context"
	"fmt"
	"io/fs"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

func TestStatus(t *testing.T) {
	plugin := Known{Err: fs.ErrNotExist, Code: codes.NotFound, Text: "Plugin not found."}
	datastore := Known{Err: runtime.ErrNotFound, Code: codes.NotFound, Text: "Datastore not found."}
	for _, tt := range []struct {
		err   error
		known []Known
		code  codes.Code
		text  string
	}{
		{nil, nil, codes.OK, ""},
		{status.Error(codes.InvalidArgument, "refused"), []Known{plugin}, codes.InvalidArgument, "refused"},
		{fmt.Errorf("open plugins/x.jar: %w", fs.ErrNotExist), []Known{plugin}, codes.NotFound, "Plugin not found."},
		{fmt.Errorf("find: %w", runtime.ErrNotFound), []Known{plugin}, codes.NotFound, "Server not found."},
		// A service's own errors come before the common ones.
		{runtime.ErrNotFound, []Known{datastore}, codes.NotFound, "Datastore not found."},
		{storage.FullError{Free: 1 << 30, Need: 3 << 30, Kept: 2 << 30}, nil, codes.ResourceExhausted,
			"The node has 1.0 GB free, but this needs 3.0 GB, as 2 GB stays free for the servers."},
		{fmt.Errorf("%w %q", storage.ErrUnknown, "ssd"), nil, codes.InvalidArgument,
			`unknown storage location "ssd". Add it on the node with: noryx-agent storage add`},
		{fmt.Errorf("pull: %w", context.DeadlineExceeded), nil, codes.DeadlineExceeded, "pull: context deadline exceeded"},
		{fs.ErrNotExist, nil, codes.Internal, "file does not exist"},
	} {
		if st := status.Convert(Status(tt.err, tt.known...)); st.Code() != tt.code || st.Message() != tt.text {
			t.Errorf("Status(%v) = %v %q, want %v %q", tt.err, st.Code(), st.Message(), tt.code, tt.text)
		}
	}
}
