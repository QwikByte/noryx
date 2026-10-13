package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/agent/storage"
)

// A service's own meanings of errors come before those that all share; a status passes as it is.
func TestStatus(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, escapes := root.Open("../outside")
	own := []Meaning{
		{fs.ErrNotExist, codes.NotFound, "Backup not found."},
		{ErrEscapes, codes.InvalidArgument, "This path leads outside."},
	}
	for _, tc := range []struct {
		err  error
		code codes.Code
		text string
	}{
		{status.Error(codes.AlreadyExists, "Taken."), codes.AlreadyExists, "Taken."},
		{fmt.Errorf("find: %w", ErrNotFound), codes.NotFound, "Server not found."},
		{fmt.Errorf("open: %w", fs.ErrNotExist), codes.NotFound, "Backup not found."},
		{escapes, codes.InvalidArgument, "This path leads outside."},
		{storage.FullError{Free: 1 << 30, Need: 2 << 30, Kept: 1 << 30}, codes.ResourceExhausted, ""},
		{context.DeadlineExceeded, codes.DeadlineExceeded, ""},
		{errors.New("disk on fire"), codes.Internal, "disk on fire"},
	} {
		s := status.Convert(Status(tc.err, own...))
		if s.Code() != tc.code || tc.text != "" && s.Message() != tc.text {
			t.Errorf("Status(%v) = %v, want %v %q", tc.err, s, tc.code, tc.text)
		}
	}
	if Status(nil) != nil {
		t.Error("no error became one")
	}
}
