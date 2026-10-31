package files

import (
	"context"
	"io"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// Nodes provides connections to node agents.
type Nodes interface {
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// Service does what the file manager does with the files of a server, for the panel and for
// SFTP alike. Each operation is one call of the FileService of the agent of the server's
// node, which checks it: it confines paths to the server's folder, hides secrets and keeps
// space free.
type Service struct{ nodes Nodes }

func NewService(nodes Nodes) *Service { return &Service{nodes: nodes} }

// Client returns the FileService of the agent of a node.
func (s *Service) Client(ctx context.Context, nodeID string) (noryxv1.FileServiceClient, error) {
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewFileServiceClient(conn), nil
}

// List lists a folder of a server, folders first, without the files that only hold secrets.
func (s *Service) List(ctx context.Context, nodeID, serverID, p string) (*noryxv1.ListFilesResponse, error) {
	c, err := s.Client(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return c.ListFiles(ctx, &noryxv1.ListFilesRequest{ServerId: serverID, Path: p})
}

// Read reads a file of a server as the file manager shows it, or the part that req asks for.
// It returns the first message, which tells the size and version of the file and where what
// follows starts, and the stream of the others. Errors such as a missing file arrive here.
func (s *Service) Read(ctx context.Context, nodeID string, req *noryxv1.ReadFileRequest) (*noryxv1.ReadFileResponse, noryxv1.FileService_ReadFileClient, error) {
	c, err := s.Client(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	stream, err := c.ReadFile(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	return first, stream, nil
}

// Write creates or replaces a file of a server, as header describes it, with content. The
// file only appears once all of it is written; cancelling ctx discards it.
func (s *Service) Write(ctx context.Context, nodeID string, header *noryxv1.WriteFileHeader, content io.Reader) (*noryxv1.WriteFileResponse, error) {
	c, err := s.Client(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return Write(ctx, c, header, content)
}

// CreateDirectory creates a folder of a server with the folders it is in.
func (s *Service) CreateDirectory(ctx context.Context, nodeID, serverID, p string) error {
	c, err := s.Client(ctx, nodeID)
	if err == nil {
		_, err = c.CreateDirectory(ctx, &noryxv1.CreateDirectoryRequest{ServerId: serverID, Path: p})
	}
	return err
}

// Move renames or moves a file or folder of a server. It never replaces anything.
func (s *Service) Move(ctx context.Context, nodeID, serverID, from, to string) error {
	c, err := s.Client(ctx, nodeID)
	if err == nil {
		_, err = c.MoveFile(ctx, &noryxv1.MoveFileRequest{ServerId: serverID, From: from, To: to})
	}
	return err
}

// Delete deletes a file of a server, or a folder with everything in it.
func (s *Service) Delete(ctx context.Context, nodeID, serverID, p string) error {
	c, err := s.Client(ctx, nodeID)
	if err == nil {
		_, err = c.DeleteFile(ctx, &noryxv1.DeleteFileRequest{ServerId: serverID, Path: p})
	}
	return err
}
