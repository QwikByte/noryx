package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// MasterTaskKind identifies the backup job of the master among the scheduled tasks; there is
// one at most.
const MasterTaskKind = "master-backup"

// minPassphrase is the length that a passphrase of the backups of the master needs at least.
const minPassphrase = 12

// MasterSettings are where the backup job of the master keeps its archives, and how many.
type MasterSettings struct {
	// To is a storage, or a node that keeps them in one of its storage locations.
	To CopyTo `json:"to"`
	// Keep is how many of the newest archives the job keeps there.
	Keep uint32 `json:"keep"`
}

// MasterJob is the kind of task that backs up the master: its database and its certificate
// authority in an archive, see WriteMaster, which it encrypts with the passphrase that is the
// task's secret, if it has one. It keeps the newest archives in a storage or on a node, and
// deletes the older ones there; it never touches other objects or files.
type MasterJob struct {
	db      *sql.DB
	dataDir string
	copies  *Copies
}

func NewMasterJob(db *sql.DB, dataDir string, copies *Copies) MasterJob {
	return MasterJob{db: db, dataDir: dataDir, copies: copies}
}

func (j MasterJob) Check(raw json.RawMessage) (json.RawMessage, error) {
	var s MasterSettings
	msg := "Choose where to keep the backups of the master."
	if err := json.Unmarshal(raw, &s); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg = s.To.check(ctx, j.copies)
	}
	if msg == "" && (s.Keep < 1 || s.Keep > noryxv1.MaxRetention) {
		msg = "Keep 1 to 1000 backups."
	}
	if msg != "" {
		return nil, httpapi.Errorf(http.StatusBadRequest, "%s", msg)
	}
	return json.Marshal(s)
}

// CheckSecret checks a passphrase for the archives, which an administrator has to type when
// restoring one, also with `age -d`.
func (MasterJob) CheckSecret(passphrase string) error {
	if n := utf8.RuneCountInString(passphrase); n < minPassphrase || len(passphrase) > 1024 || !utf8.ValidString(passphrase) ||
		strings.ContainsFunc(passphrase, unicode.IsControl) {
		return httpapi.Errorf(http.StatusBadRequest, "Enter a passphrase with %d to 1024 characters, without line breaks.", minPassphrase)
	}
	return nil
}

// Needs tells that only administrators may save and run the job, and that each run needs the
// user who saved it last to still be one: the archives hold the private key of the certificate
// authority, with which anyone controls the agents, and every secret of the master.
func (MasterJob) Needs(json.RawMessage) []access.Permission {
	return []access.Permission{access.Administrators}
}

func (MasterJob) Lead(json.RawMessage) time.Duration { return 0 }

// TargetsOptional tells that the job runs on no servers.
func (MasterJob) TargetsOptional(json.RawMessage) bool { return true }

func (MasterJob) Category() slog.Attr { return logging.Backups }

// Prune takes the archives away from a node that was removed, so that the runs fail until the
// job keeps them elsewhere.
func (MasterJob) Prune(raw json.RawMessage, exists schedule.Exists) (json.RawMessage, []string) {
	var s MasterSettings
	if json.Unmarshal(raw, &s) != nil || s.To.Node == "" || exists.Nodes[s.To.Node] {
		return raw, nil
	}
	s.To = CopyTo{}
	pruned, err := json.Marshal(s)
	if err != nil {
		return raw, nil
	}
	return pruned, []string{"the node that kept the backups of the master, which was removed"}
}

// Run saves an archive of the master where the job keeps them, and then deletes the oldest
// there beyond those it keeps.
func (j MasterJob) Run(ctx context.Context, t schedule.Task, _ schedule.Servers, _ time.Time) error {
	var s MasterSettings
	if err := json.Unmarshal(t.Settings, &s); err != nil {
		return err
	}
	change, err := j.backUp(ctx, s, t.Secret())
	return t.ReportChange(logging.Backups, "Back up the master", change, err)
}

// backUp saves an archive, encrypted with the passphrase unless it is empty, deletes the old
// ones, and tells what it changed.
func (j MasterJob) backUp(ctx context.Context, s MasterSettings, passphrase string) (string, error) {
	if s.To == (CopyTo{}) {
		return "", errors.New("it has nowhere to keep the backups, e.g. as their node was removed; choose a storage or a node")
	}
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	p, err := j.copies.place(ctx, s.To)
	if err != nil {
		return "", err
	}
	created := time.Now()
	name := noryxv1.MasterBackupName(created, passphrase != "")
	r, w := io.Pipe()
	written := make(chan error, 1)
	go func() {
		err := WriteMaster(ctx, j.db, j.dataDir, w, passphrase)
		w.CloseWithError(err)
		written <- err
	}()
	size, err := j.put(ctx, p, name, r)
	r.CloseWithError(errors.New("the upload ended")) // stops writing if the upload failed
	if writeErr := <-written; err == nil {
		err = writeErr
	}
	if err != nil {
		return "", err
	}
	_, err = j.db.ExecContext(context.WithoutCancel(ctx), `
		INSERT INTO master_backups (name, storage_id, node_id, location, size, created_at) VALUES (?, nullif(?, ''), nullif(?, ''), ?, ?, ?)`,
		name, p.Storage, p.Node, p.Location, size, created.Unix())
	if err != nil {
		return "", err
	}
	change := fmt.Sprintf("saved %s (%.1f MB) to %s", name, float64(size)/1e6, p.name)
	deleted, err := j.prune(ctx, p, s.Keep)
	if deleted > 0 {
		change += fmt.Sprintf(", deleted %d it no longer keeps", deleted)
	}
	return change, err
}

// put stores the archive that r reads where p is, and returns its size.
func (j MasterJob) put(ctx context.Context, p place, name string, r io.Reader) (int64, error) {
	if p.client != nil {
		return p.client.Put(ctx, p.storage.masterKey(name), r, 0)
	}
	conn, err := j.copies.nodes.Conn(ctx, p.Node)
	if err != nil {
		return 0, err
	}
	res, err := send(ctx, noryxv1.NewBackupServiceClient(conn).ImportMasterBackup,
		&noryxv1.ImportMasterBackupRequest{Content: &noryxv1.ImportMasterBackupRequest_Header{Header: &noryxv1.MasterBackup{Name: name, Location: p.Location}}},
		func(data []byte) *noryxv1.ImportMasterBackupRequest {
			return &noryxv1.ImportMasterBackupRequest{Content: &noryxv1.ImportMasterBackupRequest_Data{Data: data}}
		}, r, func(int64) {})
	return res.GetSize(), err
}

// prune deletes the archives where p is beyond the newest keep, and returns how many.
func (j MasterJob) prune(ctx context.Context, p place, keep uint32) (int, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT name, location FROM master_backups WHERE coalesce(storage_id, '') = ? AND coalesce(node_id, '') = ?
		ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET ?`, p.Storage, p.Node, keep)
	if err != nil {
		return 0, err
	}
	type archive struct{ name, location string }
	var old []archive
	for rows.Next() {
		var a archive
		if err := rows.Scan(&a.name, &a.location); err != nil {
			rows.Close()
			return 0, err
		}
		old = append(old, a)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	var errs []error
	deleted := 0
	for _, a := range old {
		if err := j.delete(ctx, p, a.name, a.location); err != nil {
			errs = append(errs, fmt.Errorf("can't delete %s: %s", a.name, httpapi.Message(err)))
			continue
		}
		if _, err := j.db.ExecContext(ctx, `DELETE FROM master_backups WHERE name = ?`, a.name); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, errors.Join(errs...)
}

// delete deletes an archive where p is; one that is gone counts as deleted.
func (j MasterJob) delete(ctx context.Context, p place, name, location string) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if p.client != nil {
		return p.client.Delete(ctx, p.storage.masterKey(name))
	}
	conn, err := j.copies.nodes.Conn(ctx, p.Node)
	if err == nil {
		_, err = noryxv1.NewBackupServiceClient(conn).DeleteMasterBackup(ctx, &noryxv1.DeleteMasterBackupRequest{
			Backup: &noryxv1.MasterBackup{Name: name, Location: location},
		})
	}
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

// masterKey returns the key of an archive of the master in the storage, in the folder master
// of its prefix, which no server ID takes.
func (s storage) masterKey(name string) string { return path.Join(s.Prefix, "master", name) }
