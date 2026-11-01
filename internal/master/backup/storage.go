package backup

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/s3"
)

var (
	regionPattern  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)
	keyPattern     = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
	errNoStorage   = httpapi.Errorf(http.StatusNotFound, "Storage not found. It may have been deleted.")
)

// Storage is S3-compatible storage that backup jobs copy backups to. Its secret key is never
// returned.
type Storage struct {
	ID string `json:"id"`
	StorageSettings
	// Copies is how many copies of backups it holds.
	Copies int `json:"copies"`
}

// StorageSettings are where a storage is, and who the master signs in as there.
type StorageSettings struct {
	Name string `json:"name"`
	// Endpoint is the host, with a port unless it is 443; the master only connects over HTTPS.
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	// Prefix is the folder of the copies in the bucket, e.g. noryx; empty for its top.
	Prefix    string `json:"prefix"`
	AccessKey string `json:"accessKey"`
	// PathStyle addresses the bucket in the path rather than in the host, e.g. for MinIO.
	PathStyle bool `json:"pathStyle"`
	// Encrypt asks the storage to encrypt the copies with its own keys (SSE-S3).
	Encrypt bool `json:"encrypt"`
}

// check normalizes the settings and returns a message for the administrator if they are invalid.
func (s *StorageSettings) check() string {
	s.Name, s.Endpoint, s.Region = strings.TrimSpace(s.Name), strings.ToLower(strings.TrimSpace(s.Endpoint)), strings.TrimSpace(s.Region)
	s.Bucket, s.Prefix, s.AccessKey = strings.TrimSpace(s.Bucket), strings.Trim(strings.TrimSpace(s.Prefix), "/"), strings.TrimSpace(s.AccessKey)
	switch {
	case s.Name == "" || utf8.RuneCountInString(s.Name) > 64 || strings.ContainsFunc(s.Name, unicode.IsControl):
		return "Enter a name with up to 64 characters."
	case strings.Contains(s.Endpoint, "://"):
		return "Enter the endpoint without https://, like s3.eu-central-1.amazonaws.com. The master only connects over HTTPS."
	case !s3.ValidEndpoint(s.Endpoint):
		return "Enter the endpoint as a host name, with a port if it isn't 443, like s3.eu-central-1.amazonaws.com."
	case !regionPattern.MatchString(s.Region):
		return "Enter the region of the bucket, like us-east-1."
	case !s3.ValidBucket(s.Bucket):
		return "Enter the name of the bucket."
	case len(s.Prefix) > 256 || s.Prefix != "" && !validPrefix(s.Prefix):
		return "Enter a folder of letters, digits, dots, dashes and underscores, like noryx/backups, or none."
	case !keyPattern.MatchString(s.AccessKey):
		return "Enter the access key."
	}
	return ""
}

func validPrefix(prefix string) bool {
	for segment := range strings.SplitSeq(prefix, "/") {
		if !segmentPattern.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// storage is a storage with its secret key, which only the master uses.
type storage struct {
	StorageSettings
	ID, secretKey string
}

// client returns a client of the bucket of the storage.
func (s storage) client(transport http.RoundTripper) (*s3.Client, error) {
	return s3.New(s3.Config{
		Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: s.secretKey, PathStyle: s.PathStyle, Encrypt: s.Encrypt,
	}, transport)
}

// key returns the key of the copy of a backup of a server in the storage.
func (s storage) key(serverID, backupID string) string {
	return path.Join(s.Prefix, serverID, backupID+".zip")
}

// Storages returns the storages, without their secret keys.
func (c *Copies) Storages(ctx context.Context) ([]Storage, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.endpoint, s.region, s.bucket, s.prefix, s.access_key, s.path_style, s.encrypt,
			(SELECT count(*) FROM backup_copies WHERE storage_id = s.id)
		FROM backup_storages s ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Storage{}
	for rows.Next() {
		var s Storage
		if err := rows.Scan(&s.ID, &s.Name, &s.Endpoint, &s.Region, &s.Bucket, &s.Prefix, &s.AccessKey, &s.PathStyle, &s.Encrypt, &s.Copies); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// storage returns a storage with its secret key.
func (c *Copies) storage(ctx context.Context, id string) (storage, error) {
	s := storage{ID: id}
	err := c.db.QueryRowContext(ctx, `SELECT name, endpoint, region, bucket, prefix, access_key, secret_key, path_style, encrypt FROM backup_storages WHERE id = ?`, id).
		Scan(&s.Name, &s.Endpoint, &s.Region, &s.Bucket, &s.Prefix, &s.AccessKey, &s.secretKey, &s.PathStyle, &s.Encrypt)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNoStorage
	}
	return s, err
}

// storageInput is a new or changed storage. An empty secret key keeps the one it has.
type storageInput struct {
	StorageSettings
	SecretKey string `json:"secretKey"`
}

// saveStorage checks that the master can write and delete objects in a storage, with the
// encryption it asks for, and saves it; a new one if id is empty.
func (c *Copies) saveStorage(ctx context.Context, id string, in storageInput) (Storage, error) {
	if msg := in.check(); msg != "" {
		return Storage{}, httpapi.Errorf(http.StatusBadRequest, "%s", msg)
	}
	// Whoever changes the storage of the master's archives chooses where they go next.
	if master, err := c.keepsMasterArchives(ctx, id); err != nil || master && !access.From(ctx).Has(access.Administrators) {
		return Storage{}, cmp.Or(err, httpapi.Errorf(http.StatusForbidden, "Only administrators can change the storage that keeps the archives of the master."))
	}
	s := storage{StorageSettings: in.StorageSettings, ID: id, secretKey: in.SecretKey}
	if id != "" && in.SecretKey == "" {
		current, err := c.storage(ctx, id)
		if err != nil {
			return Storage{}, err
		}
		s.secretKey = current.secretKey
	}
	if !keyPattern.MatchString(s.secretKey) {
		return Storage{}, httpapi.Errorf(http.StatusBadRequest, "Enter the secret key.")
	}
	if err := c.try(ctx, s); err != nil {
		return Storage{}, httpapi.Errorf(http.StatusBadRequest, "The master can't store copies there: %s", httpapi.Message(err))
	}
	var err error
	if id == "" {
		s.ID = strings.ToLower(rand.Text())
		_, err = c.db.ExecContext(ctx, `
			INSERT INTO backup_storages (id, name, endpoint, region, bucket, prefix, access_key, secret_key, path_style, encrypt, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Name, s.Endpoint, s.Region, s.Bucket, s.Prefix, s.AccessKey, s.secretKey, s.PathStyle, s.Encrypt, time.Now().Unix())
	} else {
		var res sql.Result
		res, err = c.db.ExecContext(ctx, `
			UPDATE backup_storages SET name = ?, endpoint = ?, region = ?, bucket = ?, prefix = ?, access_key = ?, secret_key = ?, path_style = ?, encrypt = ?
			WHERE id = ?`, s.Name, s.Endpoint, s.Region, s.Bucket, s.Prefix, s.AccessKey, s.secretKey, s.PathStyle, s.Encrypt, id)
		if err == nil {
			if database.RowsAffected(res) == 0 {
				err = errNoStorage
			}
		}
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		err = httpapi.Errorf(http.StatusConflict, "The name %q is taken already.", s.Name)
	}
	if err != nil {
		return Storage{}, err
	}
	list, err := c.Storages(ctx)
	for _, l := range list {
		if l.ID == s.ID {
			return l, err
		}
	}
	return Storage{}, errors.Join(err, errNoStorage)
}

// try writes and deletes a small object in a storage.
func (c *Copies) try(ctx context.Context, s storage) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	client, err := s.client(c.transport)
	if err != nil {
		return err
	}
	key := path.Join(s.Prefix, ".noryx-check")
	_, err = client.Put(ctx, key, bytes.NewReader([]byte("Noryx checks that it can store copies of backups here.\n")), 0)
	if err != nil {
		return err
	}
	return client.Delete(ctx, key)
}

// deleteStorage deletes a storage that no job copies to, and forgets its copies, which stay
// in the bucket.
func (c *Copies) deleteStorage(ctx context.Context, id string) error {
	jobs, err := c.copyingTo(ctx, id)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		return httpapi.Errorf(http.StatusConflict, "%s keeps backups there. Choose another place for them first.", strings.Join(jobs, ", "))
	}
	res, err := c.db.ExecContext(ctx, `DELETE FROM backup_storages WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if database.RowsAffected(res) == 0 {
		return errNoStorage
	}
	return nil
}

// keepsMasterArchives reports whether the backup job of the master keeps its archives in a storage.
func (c *Copies) keepsMasterArchives(ctx context.Context, storageID string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE kind = ? AND settings ->> '$.to.storage' = ?`, MasterTaskKind, storageID).Scan(&n)
	return n > 0, err
}

// copyingTo returns the names of the backup jobs that copy their backups to a storage, and of
// the backup job of the master if it keeps its archives there.
func (c *Copies) copyingTo(ctx context.Context, storageID string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT name, settings FROM tasks WHERE kind IN (?, ?) ORDER BY name`, TaskKind, MasterTaskKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name, raw string
		var s struct {
			Copy *CopyTo `json:"copy"` // of a backup job
			To   CopyTo  `json:"to"`   // of the master's
		}
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &s) == nil && (s.Copy != nil && s.Copy.Storage == storageID || s.To.Storage == storageID) {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

// registerStorages registers the routes of the storages. Copies take the backups of all
// servers away from their nodes, so managing where they go needs the permission to see and
// download the backups of all servers besides the one to manage backup jobs.
func (c *Copies) registerStorages(mux access.Mux) {
	const base = "/api/backup-storages"
	manage := access.All(access.Everywhere(access.BackupJobsManage), access.Everywhere(access.BackupsView))
	mux.Handle("GET "+base, access.Everywhere(access.BackupJobsView), func(w http.ResponseWriter, r *http.Request) {
		list, err := c.Storages(r.Context())
		respond(w, r, http.StatusOK, list, err)
	})
	save := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in storageInput
			if err := httpapi.ReadJSON(w, r, &in); err != nil {
				httpapi.WriteError(w, r, err)
				return
			}
			// The secret key is never logged.
			logging.Note(r.Context(), slog.String("name", in.Name), slog.String("endpoint", in.Endpoint), slog.String("bucket", in.Bucket),
				slog.Bool("secret_key_changed", in.SecretKey != ""))
			s, err := c.saveStorage(r.Context(), r.PathValue("id"), in)
			respond(w, r, status, s, err)
		}
	}
	mux.Handle("POST "+base, manage, save(http.StatusCreated))
	mux.Handle("PUT "+base+"/{id}", manage, save(http.StatusOK))
	mux.Handle("DELETE "+base+"/{id}", manage, func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, http.StatusNoContent, nil, c.deleteStorage(r.Context(), r.PathValue("id")))
	})
}

func respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	switch {
	case err != nil:
		httpapi.WriteError(w, r, err)
	case status == http.StatusNoContent:
		w.WriteHeader(status)
	default:
		httpapi.WriteJSON(w, status, v)
	}
}
