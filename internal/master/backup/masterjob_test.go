package backup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/s3/s3test"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// The backup job of the master keeps its newest archives in a storage, encrypted with the
// passphrase, and deletes the older ones of its own there, but never other objects.
func TestMasterJob(t *testing.T) {
	db, dir, ca := master(t)
	ctx := t.Context()
	bucket := s3test.New(t)
	c := NewCopies(db, nil, bucket.Client().Transport)
	st, err := c.saveStorage(ctx, "", storageInput{StorageSettings{
		Name: "Offsite", Endpoint: bucket.Endpoint, Region: "us-east-1", Bucket: bucket.Bucket, Prefix: "noryx", AccessKey: "key", PathStyle: true,
	}, "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := c.storage(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	others := []string{"noryx/master/notes.txt", "noryx/srvsrvsrvsrvsrvsrvsrvsrv/20261002-040000-abcdef.zip", "elsewhere/master.tar.gz"}
	for _, key := range others {
		if _, err := stored.client(c.transport).Put(ctx, key, strings.NewReader("not the job's"), 0); err != nil {
			t.Fatal(err)
		}
	}
	j := NewMasterJob(db, dir, c)
	settings := MasterSettings{To: CopyTo{Storage: st.ID}, Keep: 2}
	raw, _ := json.Marshal(settings)
	if _, err := j.Check(raw); err != nil {
		t.Fatal(err)
	}

	archives := func() (encrypted, plain []string) {
		for _, key := range bucket.Keys() {
			switch {
			case regexp.MustCompile(`^noryx/master/noryx-master-\d{8}-\d{6}-[a-z2-7]{6}\.tar\.gz\.age$`).MatchString(key):
				encrypted = append(encrypted, key)
			case regexp.MustCompile(`^noryx/master/noryx-master-\d{8}-\d{6}-[a-z2-7]{6}\.tar\.gz$`).MatchString(key):
				plain = append(plain, key)
			}
		}
		return encrypted, plain
	}
	var change string
	for i := range 3 {
		if change, err = j.backUp(ctx, settings, passphrase); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(change, "to Offsite") || strings.Contains(change, "deleted") != (i == 2) {
			t.Errorf("run %d changed %q", i, change)
		}
	}
	encrypted, plain := archives()
	if len(encrypted) != 2 || len(plain) != 0 || len(bucket.Keys()) != 2+len(others) {
		t.Fatalf("objects = %v", bucket.Keys())
	}
	newest := regexp.MustCompile(`noryx-master-\S+`).FindString(change)
	if !strings.HasSuffix(newest, ".age") || bucket.Objects()["noryx/master/"+newest].Data == nil {
		t.Fatalf("the newest archive %q isn't kept: %v", newest, bucket.Keys())
	}
	m, err := ReadMaster(bytes.NewReader(bucket.Objects()["noryx/master/"+newest].Data), given(passphrase), t.TempDir())
	if err != nil || !m.CA.Equal(ca.Cert) {
		t.Fatalf("restoring the newest archive: %v", err)
	}

	// Without a passphrase, archives aren't encrypted; they count with the encrypted ones.
	if _, err := j.backUp(ctx, settings, ""); err != nil {
		t.Fatal(err)
	}
	encrypted, plain = archives()
	if len(encrypted) != 1 || len(plain) != 1 {
		t.Fatalf("objects = %v", bucket.Keys())
	}
	if _, err := ReadMaster(bytes.NewReader(bucket.Objects()[plain[0]].Data), given(""), t.TempDir()); err != nil {
		t.Fatal(err)
	}

	// A run that can't store its archive keeps the others and records nothing.
	bucket.Fail(func(r *http.Request) int {
		if r.Method == http.MethodPut {
			return http.StatusForbidden
		}
		return 0
	})
	if _, err := j.backUp(ctx, settings, passphrase); err == nil {
		t.Fatal("a run without a storage that takes its archive succeeded")
	}
	bucket.Fail(nil)
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM master_backups`).Scan(&rows); err != nil || rows != 2 || len(bucket.Keys()) != 2+len(others) {
		t.Errorf("%d archives recorded, %v, objects %v", rows, err, bucket.Keys())
	}

	// The storage can't be deleted while the job keeps its archives there.
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES ('m', ?, 'Master', 1, '{}', ?, 0)`,
		MasterTaskKind, raw); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteStorage(ctx, st.ID); statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "Master") {
		t.Errorf("deleting the storage of the master's archives: %v", err)
	}
	// A second job of the master is refused.
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES ('m2', ?, 'Other', 1, '{}', ?, 0)`,
		MasterTaskKind, raw); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("a second job of the master: %v", err)
	}

	// A job without a place for its archives fails.
	err = j.Run(ctx, schedule.Task{Settings: json.RawMessage(`{"to":{},"keep":2}`)}, nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "nowhere to keep") {
		t.Errorf("run without a place: %v", err)
	}
}

func TestMasterJobCheck(t *testing.T) {
	db, dir, _ := master(t)
	bucket := s3test.New(t)
	c := NewCopies(db, nil, bucket.Client().Transport)
	st, err := c.saveStorage(t.Context(), "", storageInput{StorageSettings{
		Name: "Offsite", Endpoint: bucket.Endpoint, Region: "us-east-1", Bucket: bucket.Bucket, AccessKey: "key", PathStyle: true,
	}, "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	j := NewMasterJob(db, dir, c)
	for settings, ok := range map[string]bool{
		`{"to":{"storage":"` + st.ID + `"},"keep":7}`:                true,
		`{"to":{"storage":"` + st.ID + `"},"keep":1000}`:             true,
		`{"to":{"storage":"` + st.ID + `"},"keep":0}`:                false,
		`{"to":{"storage":"` + st.ID + `"},"keep":1001}`:             false,
		`{"to":{"storage":"` + st.ID + `","location":"x"},"keep":7}`: false,
		`{"to":{"storage":"aaaaaaaaaaaaaaaaaaaaaaaaaa"},"keep":7}`:   false,
		`{"to":{},"keep":7}`: false,
		`{"keep":7}`:         false,
		`[]`:                 false,
	} {
		if _, err := j.Check(json.RawMessage(settings)); (err == nil) != ok || err != nil && statusOf(err) != http.StatusBadRequest {
			t.Errorf("Check(%s) = %v", settings, err)
		}
	}
	for passphrase, ok := range map[string]bool{
		"correct horse battery staple": true,
		"zwölf Zeichen":                true,
		"eleven char":                  false,
		"":                             false,
		"two lines\ncorrect horse":     false,
		strings.Repeat("a", 1025):      false,
		"invalid \xff UTF-8 here":      false,
	} {
		if err := j.CheckSecret(passphrase); (err == nil) != ok {
			t.Errorf("CheckSecret(%q) = %v", passphrase, err)
		}
	}
}

// The job no longer keeps archives on a node that was removed, so its runs fail until it
// keeps them elsewhere.
func TestMasterJobPrune(t *testing.T) {
	exists := schedule.Exists{Nodes: map[string]bool{"n1": true}}
	in, _ := json.Marshal(MasterSettings{To: CopyTo{Node: "n2", Location: "big"}, Keep: 3})
	out, removed := MasterJob{}.Prune(in, exists)
	var got MasterSettings
	if err := json.Unmarshal(out, &got); err != nil || got.To != (CopyTo{}) || got.Keep != 3 || len(removed) != 1 {
		t.Fatalf("pruned = %s, removed %q, %v", out, removed, err)
	}
	for _, kept := range []MasterSettings{{To: CopyTo{Node: "n1"}, Keep: 1}, {To: CopyTo{Storage: "s1"}, Keep: 1}} {
		in, _ := json.Marshal(kept)
		if out, removed := (MasterJob{}).Prune(in, exists); string(out) != string(in) || len(removed) != 0 {
			t.Fatalf("pruned %s to %s", in, out)
		}
	}
}
