package e2e

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

// runOnce runs a task right away and returns its run once it ended.
func runOnce(t *testing.T, api apiClient, path string) schedule.Run {
	t.Helper()
	var task schedule.Task
	api.do("POST", path+"/run", nil, http.StatusAccepted, &task)
	for deadline := time.Now().Add(time.Minute); task.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the task didn't finish")
		}
		api.do("GET", path, nil, http.StatusOK, &task)
	}
	var runs []schedule.Run
	api.do("GET", path+"/runs", nil, http.StatusOK, &runs)
	return runs[0]
}

// The backup job of the master keeps archives of its database and CA in S3-compatible storage
// or on a node, encrypted with a passphrase that the API never returns. Only administrators
// may see and change it, and there is one at most.
func TestMasterBackups(t *testing.T) {
	m := startMaster(t)
	a1 := m.startAgent(t, "node-1")
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	var st backup.Storage
	api.do("POST", "/api/backup-storages", map[string]any{
		"name": "Offsite", "endpoint": m.s3.Endpoint, "region": "us-east-1", "bucket": m.s3.Bucket, "prefix": "noryx", "accessKey": "key",
		"secretKey": "the-secret-key", "pathStyle": true, "encrypt": true,
	}, http.StatusCreated, &st)

	const passphrase = "correct horse battery staple"
	job := func(to map[string]string, secret any) map[string]any {
		in := map[string]any{
			"name": "Master", "enabled": true, "schedule": map[string]any{"times": []string{"03:00"}, "timeZone": "UTC"}, "targets": []any{},
			"settings": map[string]any{"to": to, "keep": 1},
		}
		if secret != nil {
			in["secret"] = secret
		}
		return in
	}

	// Others than administrators can't see or change it, whatever their permissions.
	other := m.withGrants(t, map[string]any{"name": "Everything else", "permissions": []string{
		"backupjobs.manage", "backups.view", "settings.edit", "policies.manage", "users.manage",
	}})
	other.do("GET", "/api/master-backups", nil, http.StatusForbidden, nil)
	other.do("POST", "/api/master-backups", job(map[string]string{"storage": st.ID}, passphrase), http.StatusForbidden, nil)

	api.do("POST", "/api/master-backups", job(map[string]string{"storage": st.ID}, "too short"), http.StatusBadRequest, nil)
	api.do("POST", "/api/master-backups", job(map[string]string{}, passphrase), http.StatusBadRequest, nil)
	api.do("POST", "/api/backup-jobs", map[string]any{
		"name": "Secret", "enabled": true, "schedule": map[string]any{"times": []string{"03:00"}, "timeZone": "UTC"},
		"targets":  []map[string]string{{"nodeId": a1.node.ID}},
		"settings": map[string]any{"selection": map[string]any{"everything": true}, "keep": 2}, "secret": passphrase,
	}, http.StatusBadRequest, nil)
	var task schedule.Task
	body := api.do("POST", "/api/master-backups", job(map[string]string{"storage": st.ID}, passphrase), http.StatusCreated, &task)
	if !task.HasSecret || strings.Contains(body, passphrase) {
		t.Fatalf("created %s", body)
	}
	api.do("POST", "/api/master-backups", job(map[string]string{"storage": st.ID}, nil), http.StatusConflict, nil)
	path := "/api/master-backups/" + task.ID
	if body := api.do("GET", "/api/master-backups", nil, http.StatusOK, nil); strings.Contains(body, passphrase) {
		t.Fatalf("jobs = %s", body)
	}

	// A run saves an archive encrypted with the passphrase.
	run := runOnce(t, api, path)
	if run.Error != "" || len(run.Steps) != 1 || run.Steps[0].Action != "Back up the master" || !strings.Contains(run.Steps[0].Change, "to Offsite") {
		t.Fatalf("run = %+v", run)
	}
	name := regexp.MustCompile(`noryx-master-\S+\.tar\.gz\.age`).FindString(run.Steps[0].Change)
	object, ok := m.s3.Objects()["noryx/master/"+name]
	if name == "" || !ok {
		t.Fatalf("objects = %v, change %q", m.s3.Keys(), run.Steps[0].Change)
	}
	// Restoring it is tested with a lower work factor of scrypt, which takes long with the race
	// detector.
	if !regexp.MustCompile(`^age-encryption.org/v1\n-> scrypt \S+ 18\n`).Match(object.Data) {
		t.Fatalf("the archive starts with %q", object.Data[:min(len(object.Data), 80)])
	}

	// Without the passphrase, the node keeps plain archives, as many as the job keeps; a change
	// without a secret keeps the passphrase.
	api.do("PUT", path, job(map[string]string{"node": a1.node.ID}, ""), http.StatusOK, &task)
	if task.HasSecret {
		t.Fatal("the passphrase wasn't removed")
	}
	var archives []os.DirEntry
	for range 2 {
		if run := runOnce(t, api, path); run.Error != "" || !strings.Contains(run.Steps[0].Change, "to node-1") {
			t.Fatalf("run = %+v", run)
		}
		archives, err = os.ReadDir(filepath.Join(a1.dir, "backups", "master"))
		check(t, err)
		if len(archives) != 1 || !strings.HasSuffix(archives[0].Name(), ".tar.gz") {
			t.Fatalf("archives on the node = %v", archives)
		}
	}
	data, err := os.ReadFile(filepath.Join(a1.dir, "backups", "master", archives[0].Name()))
	check(t, err)
	if restored, err := backup.ReadMaster(bytes.NewReader(data), func() (string, error) { return "", nil }, t.TempDir()); err != nil || !restored.CA.Equal(m.ca.Cert) {
		t.Fatalf("restore the archive: %v", err)
	}
	if _, ok := m.s3.Objects()["noryx/master/"+name]; !ok {
		t.Error("the archive in the storage the job no longer keeps its archives in was deleted")
	}
	api.do("PUT", path, job(map[string]string{"node": a1.node.ID}, passphrase), http.StatusOK, &task)
	api.do("PUT", path, job(map[string]string{"node": a1.node.ID}, nil), http.StatusOK, &task)
	if !task.HasSecret {
		t.Fatal("a change without a passphrase removed it")
	}

	// Once the user who saved it last is no longer an administrator, it no longer runs.
	api.do("PUT", path, job(map[string]string{"node": a1.node.ID}, nil), http.StatusOK, &task)
	second, err := svc.Users.CreateUser(t.Context(), "second", "the-second-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), second.ID))
	_, err = m.db.ExecContext(t.Context(), `DELETE FROM group_members WHERE user_id = ?`, admin.ID)
	check(t, err)
	api = browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "second", "password": "the-second-password"}, http.StatusOK, nil)
	if run := runOnce(t, api, path); !strings.Contains(run.Error, "admin, who saved it last, is no longer an administrator") {
		t.Errorf("run saved by a former administrator = %+v", run)
	}
}
