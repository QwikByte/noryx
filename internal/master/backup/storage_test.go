package backup

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/s3/s3test"
)

func TestStorageSettingsCheck(t *testing.T) {
	valid := StorageSettings{Name: "Offsite", Endpoint: "s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "noryx-backups", AccessKey: "AKIAEXAMPLE"}
	s := valid
	s.Name, s.Endpoint, s.Prefix, s.AccessKey = " Offsite ", " S3.EU-Central-1.amazonaws.com ", "/noryx/backups/", " AKIAEXAMPLE "
	if msg := s.check(); msg != "" || s.Name != "Offsite" || s.Endpoint != "s3.eu-central-1.amazonaws.com" || s.Prefix != "noryx/backups" || s.AccessKey != "AKIAEXAMPLE" {
		t.Fatalf("check = %q, settings %+v", msg, s)
	}
	for _, tc := range []struct {
		name string
		edit func(s *StorageSettings)
		want string
	}{
		{"a port", func(s *StorageSettings) { s.Endpoint = "minio.example.com:9000" }, ""},
		{"a folder", func(s *StorageSettings) { s.Prefix = "noryx/backups_2.v1" }, ""},
		{"no name", func(s *StorageSettings) { s.Name = " " }, "name"},
		{"a long name", func(s *StorageSettings) { s.Name = strings.Repeat("ä", 65) }, "name"},
		{"a name of two lines", func(s *StorageSettings) { s.Name = "Off\nsite" }, "name"},
		{"a scheme", func(s *StorageSettings) { s.Endpoint = "https://s3.amazonaws.com" }, "without https://"},
		{"plain HTTP", func(s *StorageSettings) { s.Endpoint = "http://minio.local" }, "without https://"},
		{"a path", func(s *StorageSettings) { s.Endpoint = "s3.amazonaws.com/bucket" }, "endpoint as a host name"},
		{"a user", func(s *StorageSettings) { s.Endpoint = "user@s3.amazonaws.com" }, "endpoint as a host name"},
		{"port 0", func(s *StorageSettings) { s.Endpoint = "s3.amazonaws.com:0" }, "endpoint as a host name"},
		{"a large port", func(s *StorageSettings) { s.Endpoint = "s3.amazonaws.com:70000" }, "endpoint as a host name"},
		{"no endpoint", func(s *StorageSettings) { s.Endpoint = "" }, "endpoint as a host name"},
		{"no region", func(s *StorageSettings) { s.Region = "" }, "region"},
		{"a region with spaces", func(s *StorageSettings) { s.Region = "eu central" }, "region"},
		{"a short bucket", func(s *StorageSettings) { s.Bucket = "ab" }, "bucket"},
		{"a bucket with a slash", func(s *StorageSettings) { s.Bucket = "a/b" }, "bucket"},
		{"a folder up", func(s *StorageSettings) { s.Prefix = "noryx/../other" }, "folder"},
		{"an empty folder", func(s *StorageSettings) { s.Prefix = "noryx//backups" }, "folder"},
		{"a folder with spaces", func(s *StorageSettings) { s.Prefix = "my backups" }, "folder"},
		{"a long folder", func(s *StorageSettings) { s.Prefix = strings.Repeat("a/", 129) + "a" }, "folder"},
		{"no access key", func(s *StorageSettings) { s.AccessKey = "" }, "access key"},
		{"an access key with spaces", func(s *StorageSettings) { s.AccessKey = "AKIA EXAMPLE" }, "access key"},
	} {
		s := valid
		tc.edit(&s)
		if got := s.check(); tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStorageKey(t *testing.T) {
	for prefix, want := range map[string]string{"": "srv/20261002-040000-abcdef.zip", "noryx/backups": "noryx/backups/srv/20261002-040000-abcdef.zip"} {
		if got := (storage{StorageSettings: StorageSettings{Prefix: prefix}}).key("srv", "20261002-040000-abcdef"); got != want {
			t.Errorf("key with prefix %q = %q, want %q", prefix, got, want)
		}
	}
}

// Storages keep their secret keys to themselves: the master tries them before it saves them,
// keeps the one it has if a change sends none, and never returns it.
func TestStorages(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bucket := s3test.New(t)
	c := NewCopies(db, nil, bucket.Client().Transport)
	ctx := t.Context()
	in := storageInput{StorageSettings{Name: "Offsite", Endpoint: bucket.Endpoint, Region: "us-east-1", Bucket: bucket.Bucket, Prefix: "noryx", AccessKey: "key", PathStyle: true}, "s3cr3t"}

	saved, err := c.saveStorage(ctx, "", in)
	if err != nil || saved.ID == "" || saved.Name != "Offsite" {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if objects := bucket.Objects(); len(objects) != 0 {
		t.Errorf("the check left %d objects", len(objects))
	}
	list, err := c.Storages(ctx)
	data, _ := json.Marshal(list)
	if err != nil || len(list) != 1 || strings.Contains(string(data), "s3cr3t") || strings.Contains(strings.ToLower(string(data)), "secret") {
		t.Fatalf("storages = %s, %v", data, err)
	}
	secret := func() string {
		s, err := c.storage(ctx, saved.ID)
		if err != nil {
			t.Fatal(err)
		}
		return s.secretKey
	}

	in.SecretKey, in.Prefix = "", "noryx/copies"
	if changed, err := c.saveStorage(ctx, saved.ID, in); err != nil || changed.Prefix != "noryx/copies" || secret() != "s3cr3t" {
		t.Fatalf("change without a secret key: %+v, %v, secret %q", changed, err, secret())
	}
	in.SecretKey = "n3w-s3cr3t"
	if _, err := c.saveStorage(ctx, saved.ID, in); err != nil || secret() != "n3w-s3cr3t" {
		t.Fatalf("change of the secret key: %v, secret %q", err, secret())
	}

	for _, tc := range []struct {
		name   string
		id     string
		edit   func(in *storageInput)
		status int
	}{
		{"a new one without a secret key", "", func(in *storageInput) { in.Name, in.SecretKey = "Other", "" }, http.StatusBadRequest},
		{"a secret key of two lines", "", func(in *storageInput) { in.Name, in.SecretKey = "Other", "a\nb" }, http.StatusBadRequest},
		{"invalid settings", "", func(in *storageInput) { in.Name, in.Endpoint = "Other", "https://"+bucket.Endpoint }, http.StatusBadRequest},
		{"a name in use", "", func(in *storageInput) { in.Name = "OFFSITE" }, http.StatusConflict},
		{"one that doesn't exist", "nope", func(in *storageInput) { in.Name, in.SecretKey = "Other", "" }, http.StatusNotFound},
	} {
		in := in
		tc.edit(&in)
		if _, err := c.saveStorage(ctx, tc.id, in); statusOf(err) != tc.status {
			t.Errorf("%s: %v, want %d", tc.name, err, tc.status)
		}
	}

	// A storage that refuses the master's objects isn't saved.
	bucket.Fail(func(*http.Request) int { return http.StatusForbidden })
	in.Name = "Refusing"
	if _, err := c.saveStorage(ctx, "", in); statusOf(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "can't store copies there") {
		t.Errorf("refusing storage: %v", err)
	}
	bucket.Fail(nil)

	// A storage that a job copies to stays.
	settings, _ := json.Marshal(JobSettings{Keep: 3, Copy: &CopyTo{Storage: saved.ID}})
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES ('t1', ?, 'Nightly', 1, '{}', ?, 0)`,
		TaskKind, settings); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteStorage(ctx, saved.ID); statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "Nightly") {
		t.Errorf("deleting a storage in use: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteStorage(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteStorage(ctx, saved.ID); !errors.Is(err, errNoStorage) {
		t.Errorf("deleting it again: %v", err)
	}
}

// statusOf is the HTTP status of an error of the API, or 0.
func statusOf(err error) int {
	var apiErr *httpapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}
