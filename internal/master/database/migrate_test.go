package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// released is the number of migrations of the last release, v0.8.7, whose schema the masters
// that update from it have.
const released = 39

// releasedMigrations are the SHA-256 hashes of the migrations of released versions. They never
// change: masters ran them already, and run each only once.
var releasedMigrations = map[string]string{
	"0001_init.sql":                "3378a529499e52a1d3aa011d2049d51e1ff59526a8fd50e20d0ccc2ab189bd4d",
	"0002_networks.sql":            "34a6fae57f2dd8f6073c20db89aee37f8063401565d03893cd3509678df128d5",
	"0003_node_settings.sql":       "2766d5befeba19c42713e8f4769fca1f75c217aaa41a70576e3ed6814401d148",
	"0004_templates.sql":           "9323214eefb833a4172fb2579183ba20364217ab37cdce88d4cf71cef91fa151",
	"0005_tasks.sql":               "7eb030744e6f9811a620cd23de32dd04d1a9a89dfc6cfeb4b9d9c15660cb8646",
	"0006_settings.sql":            "8be5e5b315300fbcb2cff21594aec053051e82e3bdb6362724eca07d3fcbace9",
	"0007_access.sql":              "65909734cbd2ef57701ddcd060b29ffb7b54502bf1dd3b30354845e91816a587",
	"0008_logs.sql":                "6ba34bbd1a149a5728e8f44d8dddb47410a3cc47b2d5912f9439b3aabccc4070",
	"0009_mfa.sql":                 "3df7e9b62901893af7617acbc8a351125344e4f3ceb707e148438661cf515523",
	"0010_usage.sql":               "6ff9494d1a51de2d06b42eeb345bf039f0e85ad5fbc3e13200ef7c44a23db383",
	"0011_language.sql":            "829e16ac654a8c8cdb44f34641dd6aaa53c9d9dc9cf5f3e8f07ac02586cbae31",
	"0012_network_routing.sql":     "96311968e8b1251cf68480df1a885203b5ba2030b792e796b0c066b1177c6525",
	"0013_server_tags.sql":         "299f930f830619b1ef727fdd27ab35f62d8ec1bd89613c334f9ced6faf835d00",
	"0014_network_bedrock.sql":     "43688309cdd6b4d86f058c2469f4d7155e157c055969967b72d01d9c7c461c4a",
	"0015_network_apply_error.sql": "e23d28807a37020d1bb5c7838f48984e36270ceffda0bdfd0e875486f1ec4144",
	"0016_task_note.sql":           "553a471d527af7ea6c3dcb200b661a7f0dce644e92fbf751441352e0f0b5ae56",
	"0017_overlay.sql":             "9a8adcfcaf303d9403eb2e8732a2bedefc5b62649205089f5f77171c168df240",
	"0018_file_sets.sql":           "d90770c4a047b571117ec4eacbdd61f32efb9f41d17830c9d8daf5e25579531a",
	"0019_datastores.sql":          "4227d681a45be5b8cfb1c9b96a8ae508d097af8dda9017b4158fd9eb3c8d1b55",
	"0020_preferences.sql":         "7e77e1893aa87e5c94b465cb48646aeca66bc3da4f72b85baeebbd7dbcdbaa86",
	"0021_user_settings.sql":       "db90a06f974b873b3ff73fb0905b10df0fdffcb10e79bfe66b65da7c1d7a82fc",
	"0022_server_notes.sql":        "438add402f3392871723253778b9837e6aa893985ee98b38e5cafa02672cf561",
	"0023_log_size.sql":            "be8975b74cc1d003d1bb194c3d506ed3076210932db5b5d27c9cb50f945a93fa",
	"0024_sessions.sql":            "ed61c9c0f02e440a2ecfef4522213cb20b040e8ac5bd5a217d646502f93bb8bf",
	"0025_node_certificates.sql":   "22f9a8cd08a6f15d6c072b0bc481f21560b5ebebdb3e9224c04775d5b1843383",
	"0026_usage_thresholds.sql":    "a47bcd9291c6dea3be9c12f3d5be7d2a47ded6da8036f562a2abc3217c513845",
	"0027_server_modpacks.sql":     "4d9073245340d3a0fc575e2ba16367ae503e7c2ac9dcc9b512c33fb7eb4209bf",
	"0028_task_targets.sql":        "5aab55f423d0c6128ccf6ef4566c53df9497655c53f2dd9a35d83b83b0353c50",
	"0029_task_runs.sql":           "405f1e6333a0bef7857595b677622caf60e2830c05bcd2f3d95fb79f39f28729",
	"0030_api_tokens.sql":          "245a71b98315ec5317dbd5450a8b39240f0baa845218a9be4a87bd20a81fbf47",
	"0031_file_set_variables.sql":  "b9e809421b54e94728a00db21d4eca9e6acfa5c62d96cca639eb7fcdf3910b74",
	"0032_plugin_pins.sql":         "02ddb8beb4c59c6bee8888ec956ea8a9fbc10aea82c1039df5d7d8a0780a059e",
	"0033_task_authors.sql":        "0ae29bc27e5ab8bbfa35fcbfd266d7e2be683f325c67b96289f92554c91ff37d",
	"0034_player_sightings.sql":    "d15f4558cbfd1289fa19bcb44f6a59dadf4b672dfe3487bb0508a5fbb153e00a",
	"0035_notifications.sql":       "692dd7ef8e1cfde965b8766aca4fdf14645ee0d877b916ae19d344e76ce3ff87",
	"0036_datastore_usage.sql":     "c7d09747c6f55b1517e945fafc6ce6968b9579a6e00e3154adc4b2f3c0ee498b",
	"0037_backup_copies.sql":       "960cc42929b43b8cd97c49e87f11d3604e5acbc2144f759054f99415e1355064",
	"0038_alert_filters.sql":       "7b506b80edf21c132d275b44a6cf1866d667d1d7559c2155981e395d60084243",
	"0039_workflows.sql":           "7420f006efd95e3fed51fd1e858347bd3719d301eb00c34bcf3a5f2df4ebee1e",
}

// migrationFiles returns the names of the migrations in the order they run.
func migrationFiles(t *testing.T) []string {
	t.Helper()
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestReleasedMigrationsUnchanged(t *testing.T) {
	files := migrationFiles(t)
	if len(files) < released || len(releasedMigrations) != released {
		t.Fatalf("%d migrations, %d of them released", len(files), len(releasedMigrations))
	}
	for i, file := range files {
		data, err := migrations.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name, sum := path.Base(file), sha256.Sum256(data)
		want, ok := releasedMigrations[name]
		switch {
		case i < released && hex.EncodeToString(sum[:]) != want:
			t.Errorf("%s was released and must not change; add a new migration instead", name)
		case i >= released && ok, i < released && !ok:
			t.Errorf("%s runs as migration %d", name, i+1)
		}
		if !strings.HasPrefix(name, fmt.Sprintf("%04d_", i+1)) {
			t.Errorf("migration %d is named %s", i+1, name)
		}
	}
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// A new database gets every migration once, with foreign keys enforced, and a master refuses
// a database of a newer version.
func TestMigrateEmpty(t *testing.T) {
	file := filepath.Join(t.TempDir(), "noryx.db")
	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	var journal, admins string
	err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys)
	if err == nil {
		err = db.QueryRow(`PRAGMA journal_mode`).Scan(&journal)
	}
	if err == nil {
		err = db.QueryRow(`SELECT name FROM user_groups WHERE id = 'administrators'`).Scan(&admins)
	}
	if err != nil || userVersion(t, db) != len(migrationFiles(t)) || foreignKeys != 1 || journal != "wal" || admins != "Administrators" {
		t.Fatalf("version %d, foreign keys %d, journal %q, %q: %v", userVersion(t, db), foreignKeys, journal, admins, err)
	}
	if _, err := db.Exec(`INSERT INTO group_members (group_id, user_id) VALUES ('administrators', 42)`); err == nil {
		t.Error("a member that isn't a user was added")
	}
	db.Close()

	// Opening it again runs nothing again, which would fail as the tables exist.
	if db, err = Open(file); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrationFiles(t))+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("a database of a newer version: %v", err)
	}
}

// A database of the last release keeps its data through the migrations since, which give
// existing rows the defaults of new columns and convert what changed.
func TestMigrateReleased(t *testing.T) {
	file := filepath.Join(t.TempDir(), "noryx.db")
	old, err := sql.Open("sqlite", "file:"+file+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(db *sql.DB, query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for i, name := range migrationFiles(t)[:released] {
		script, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		exec(old, fmt.Sprintf("%s;\nPRAGMA user_version = %d", script, i+1))
	}
	exec(old, `INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'admin', '$argon2id$x', 1)`)
	exec(old, `INSERT INTO group_members (group_id, user_id) VALUES ('administrators', 1)`)
	exec(old, `INSERT INTO nodes (id, name, address, created_at) VALUES ('n1', 'node-1', 'host:7443', 1)`)
	exec(old, `INSERT INTO networks (id, name, proxy_node_id, proxy_server_id, forwarding_secret, created_at) VALUES ('net', 'Main', 'n1', 'proxy', 's', 1)`)
	exec(old, `INSERT INTO datastores (id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, created_at)
		VALUES ('d1', 'net', 'n1', 'main', 'mariadb', '11', 1024, 0, 'default', 1)`)
	exec(old, `INSERT INTO notification_channels (id, name, kind, created_at) VALUES ('c1', 'Ops', 'discord', 1)`)
	exec(old, `INSERT INTO notification_rules (id, channel_id, enabled, level, categories, created_at) VALUES ('r1', 'c1', 1, 'error', '[]', 1)`)
	policies := map[string]string{ // the settings of tasks, by ID
		"restart": `{"action":"restart","message":"The server restarts in {minutes} min."}`,
		"stop":    `{"action":"stop","message":"The server stops in {minutes} min."}`,
		"own":     `{"action":"restart","message":"Restart in {minutes} minutes!"}`,
		"other":   `{"action":"restart","message":"The server stops in {minutes} min."}`,
	}
	for id, settings := range policies {
		exec(old, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES (?, 'policy', ?, 1, '{}', ?, 1)`, id, id, settings)
	}
	exec(old, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at) VALUES ('job', 'backup', 'job', 1, '{}', ?, 1)`,
		`{"keep":3,"message":"The server restarts in {minutes} min."}`)
	old.Close()

	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := userVersion(t, db); got != len(migrationFiles(t)) {
		t.Fatalf("version %d", got)
	}
	for _, tc := range []struct {
		query string
		want  string
	}{
		{`SELECT username || ' ' || group_id FROM users JOIN group_members ON user_id = id`, "admin administrators"},
		{`SELECT format('%d %d %d %d', log_file_mb, log_files, keep_free_gb, stats_interval_seconds) FROM nodes`, "10 3 1 5"},
		{`SELECT format('%d %d', max_connections, buffer_mb) FROM datastores`, "0 0"},
		{`SELECT format('[%s] [%s]', tag, network_id) FROM notification_rules`, "[] []"},
		{`SELECT format('%d %d [%s]', messages_per_minute, entries_per_message, time_zone) FROM notification_channels`, "1 10 []"},
		// Schedules with the English text of their action warn with the text of the settings.
		{`SELECT json_extract(settings, '$.message') FROM tasks WHERE id = 'restart'`, ""},
		{`SELECT json_extract(settings, '$.message') FROM tasks WHERE id = 'stop'`, ""},
		{`SELECT json_extract(settings, '$.message') FROM tasks WHERE id = 'own'`, "Restart in {minutes} minutes!"},
		{`SELECT json_extract(settings, '$.message') FROM tasks WHERE id = 'other'`, "The server stops in {minutes} min."},
		{`SELECT json_extract(settings, '$.message') FROM tasks WHERE id = 'job'`, "The server restarts in {minutes} min."},
	} {
		var got string
		if err := db.QueryRow(tc.query).Scan(&got); err != nil || got != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.query, got, err, tc.want)
		}
	}
	// The tables of the new migrations work with the old rows.
	exec(db, `INSERT INTO folded_groups (user_id, folded) VALUES (1, '[]')`)
	exec(db, `INSERT INTO plugin_channels (node_id, server_id, project_id, channel) VALUES ('n1', 's1', 'p1', 'beta')`)
	if _, err := db.Exec(`INSERT INTO plugin_channels (node_id, server_id, project_id, channel) VALUES ('n1', 's1', 'p2', 'nightly')`); err == nil {
		t.Error("a channel other than beta and alpha was saved")
	}
	exec(db, `DELETE FROM users`)
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM folded_groups`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d folded groups of deleted users, %v", left, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign keys are broken")
	}
}
