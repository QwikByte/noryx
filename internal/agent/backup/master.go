package backup

import (
	"cmp"
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// masterOwner is the folder of the backups of a location that keeps the archives of the master.
// Server IDs never take its name.
const masterOwner = "master"

// ImportMasterBackup keeps an archive of the master, which holds the master's database and the
// private key of its certificate authority, in the folder master of the backups of a storage
// location, which only the agent can access. The agent neither reads nor restores it, so it
// doesn't check it: the master encrypts it unless its administrator chose otherwise. It may
// have up to 16 GB while the space kept free stays free, and never replaces another archive.
func (s *Service) ImportMasterBackup(stream noryxv1.BackupService_ImportMasterBackupServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if !noryxv1.ValidMasterBackupName(h.GetName()) {
		return status.Error(codes.InvalidArgument, "invalid name of an archive of the master")
	}
	f, err := s.store.Temp(masterOwner, cmp.Or(h.GetLocation(), storage.Default), 0)
	if err != nil {
		return toStatus(err)
	}
	defer os.Remove(f.Name()) // fails once the archive is in place
	size, err := archive.Receive(stream.Recv, storage.Guard(f, f))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return toStatus(err)
	}
	path := filepath.Join(filepath.Dir(f.Name()), h.GetName())
	if _, err := os.Lstat(path); err == nil {
		return toStatus(fs.ErrExist)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return toStatus(err)
	}
	return stream.SendAndClose(&noryxv1.ImportMasterBackupResponse{Size: size})
}

// DeleteMasterBackup deletes an archive of the master that ImportMasterBackup keeps.
func (s *Service) DeleteMasterBackup(_ context.Context, req *noryxv1.DeleteMasterBackupRequest) (*noryxv1.DeleteMasterBackupResponse, error) {
	b := req.GetBackup()
	if !noryxv1.ValidMasterBackupName(b.GetName()) {
		return nil, status.Error(codes.InvalidArgument, "invalid name of an archive of the master")
	}
	root, err := s.store.storage.BackupPath(cmp.Or(b.GetLocation(), storage.Default))
	if err == nil {
		err = os.Remove(filepath.Join(root, masterOwner, b.GetName()))
	}
	return &noryxv1.DeleteMasterBackupResponse{}, toStatus(err)
}
