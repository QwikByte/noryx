package docker

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestEveryDatastoreEngineHasABrowser(t *testing.T) {
	for engine := range engines {
		if _, ok := browsers[engine]; !ok {
			t.Errorf("%s has no browser", engine)
		}
	}
}

// TestBrowseLive browses the tables of both engines in Docker: in the order of the primary
// key or of another column, filtered, with long values cut short, binary values in
// hexadecimal, and a session that only reads, without the superuser's rights; and it counts
// the connections. It only runs with NORYX_DOCKER_TEST set, like TestDatastoresLive.
func TestBrowseLive(t *testing.T) {
	d := live(t)
	tests := map[noryxv1.DatastoreEngine]struct{ blob, bytes, hex, probe string }{
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB: {"BLOB", "X'00FF'", "0x00FF",
			// A view that reads as the user who reads it, e.g. the superuser's password.
			"CREATE SQL SECURITY INVOKER VIEW probes AS SELECT CONCAT('ran as ', CURRENT_USER()) AS who, LOAD_FILE('" + superuserMount + "') AS secret;"},
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: {"bytea", `'\x00ff'`, `\x00ff`,
			// A cast to text that runs as the user who reads its column.
			"CREATE TYPE probe AS (x int);\n" +
				"CREATE FUNCTION probe_text(probe) RETURNS text LANGUAGE plpgsql AS $$ BEGIN\n" +
				" IF (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN RAISE EXCEPTION 'ran as the superuser %', current_user; END IF;\n" +
				" RETURN 'ran as ' || current_user; END $$;\n" +
				"CREATE CAST (probe AS text) WITH FUNCTION probe_text(probe);\n" +
				"CREATE TABLE probes (p probe); INSERT INTO probes VALUES (ROW(1));"},
	}
	for engine, tc := range tests {
		t.Run(engine.Slug(), func(t *testing.T) {
			ctx := t.Context()
			versions := engines[engine].versions
			spec := runtime.DatastoreSpec{ID: runtime.NewID(), Engine: engine, Version: versions[len(versions)-1], MemoryMB: 512}
			must(t, d.CreateDatastore(ctx, spec))
			t.Cleanup(func() { must(t, d.RemoveDatastore(context.WithoutCancel(ctx), spec.ID)) })
			must(t, d.StartDatastore(ctx, spec.ID))
			must(t, d.waitReady(ctx, spec.ID))
			const password = "abcdefghijklmnopqrstuvwxyz234567"
			must(t, d.EnsureDatabase(ctx, spec.ID, "shop", password))
			long := strings.Repeat("ä", maxValue+1)
			asUser(t, d, spec.ID, password, "CREATE TABLE items (v INT); CREATE TABLE notes (id INT PRIMARY KEY, body TEXT, raw "+tc.blob+");"+
				" INSERT INTO notes VALUES (2, NULL, "+tc.bytes+"), (1, 'line\none \"quoted\" "+long+"', NULL);")

			// The agent's own query doesn't count as a connection.
			if u, err := d.DatastoreUsage(ctx, spec.ID); err != nil || u.Connections == nil || *u.Connections > 1 || u.MemoryBytes == 0 {
				t.Errorf("usage %+v, %v", u, err)
			}

			tables, err := d.Tables(ctx, spec.ID, "shop")
			must(t, err)
			if len(tables) != 2 || tables[0].GetName() != "items" || tables[1].GetName() != "notes" || tables[1].GetSize() <= 0 {
				t.Fatalf("tables %v", tables)
			}

			browse := func(req *noryxv1.BrowseTableRequest) (*noryxv1.BrowseTableResponse, error) {
				req.Database, req.Schema = "shop", tables[1].GetSchema()
				req.Table = cmp.Or(req.Table, "notes")
				req.Limit = cmp.Or(req.Limit, 1)
				return d.Browse(ctx, spec.ID, req)
			}
			first, err := browse(&noryxv1.BrowseTableRequest{})
			must(t, err)
			names := []string{}
			for _, c := range first.GetColumns() {
				names = append(names, c.GetName())
			}
			if !slices.Equal(names, []string{"id", "body", "raw"}) || !first.GetColumns()[0].GetPrimaryKey() || first.GetColumns()[1].GetPrimaryKey() {
				t.Fatalf("columns %v", first.GetColumns())
			}
			if len(first.GetRows()) != 1 || !first.GetMore() {
				t.Fatalf("first page %v", first)
			}
			row := first.GetRows()[0].GetValues()
			body := []rune(row[1].GetText())
			if row[0].GetText() != "1" || !row[1].GetTruncated() || len(body) != maxValue || !strings.HasPrefix(string(body), "line\none \"quoted\" ä") || !row[2].GetNull() {
				t.Errorf("first row %v", row)
			}
			second, err := browse(&noryxv1.BrowseTableRequest{Offset: 1})
			must(t, err)
			if row := second.GetRows()[0].GetValues(); second.GetMore() || row[0].GetText() != "2" || !row[1].GetNull() || row[2].GetText() != tc.hex {
				t.Errorf("second page %v", second)
			}

			// Sorted by a column and filtered by its text, with values that would need escaping.
			sorted, err := browse(&noryxv1.BrowseTableRequest{Sort: "id", Descending: true, Limit: 2})
			must(t, err)
			if rows := sorted.GetRows(); !sorted.GetSortedAndFiltered() || len(rows) != 2 || rows[0].GetValues()[0].GetText() != "2" {
				t.Errorf("sorted by id %v", sorted)
			}
			for filter, want := range map[*noryxv1.TableFilter]int{
				{Column: "id", Value: "2"}:                                          1,
				{Column: "raw", Value: tc.hex}:                                      1,
				{Column: "body", Value: `ONE "QUOTED"`, Contains: true}:             1,
				{Column: "body", Value: `one "quoted"`}:                             0,
				{Column: "body", Value: "x'; DROP TABLE notes; --", Contains: true}: 0,
			} {
				res, err := browse(&noryxv1.BrowseTableRequest{Filter: filter, Limit: 10})
				if err != nil || len(res.GetRows()) != want {
					t.Errorf("filtered by %v: %v, %v", filter, res, err)
				}
			}
			for _, req := range []*noryxv1.BrowseTableRequest{{Sort: "missing"}, {Filter: &noryxv1.TableFilter{Column: "missing"}}} {
				if _, err := browse(req); !errors.Is(err, runtime.ErrNoColumn) {
					t.Errorf("browsing %v: %v", req, err)
				}
			}

			for _, table := range []string{"missing", "notes; DROP TABLE notes", "`notes`"} {
				if _, err := browse(&noryxv1.BrowseTableRequest{Table: table}); !errors.Is(err, runtime.ErrNoTable) {
					t.Errorf("browsing %q: %v", table, err)
				}
			}
			write := func(browser) string { return "INSERT INTO items VALUES (1);" }
			if err := d.browse(ctx, spec.ID, "shop", write, func([]byte) error { return nil }); err == nil {
				t.Error("a browsing session wrote")
			}

			// What the database's user wrote runs with its rights, not the superuser's.
			asUser(t, d, spec.ID, password, tc.probe)
			probes, err := browse(&noryxv1.BrowseTableRequest{Table: "probes", Limit: 10})
			if err != nil || len(probes.GetRows()) != 1 {
				t.Fatalf("probes %v, %v", probes, err)
			}
			if row := probes.GetRows()[0].GetValues(); !strings.HasPrefix(row[0].GetText(), "ran as ") || strings.Contains(row[0].GetText(), "root") ||
				len(row) > 1 && !row[1].GetNull() {
				t.Errorf("probes %v", probes)
			}
			if engine == noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB {
				s, err := d.session(ctx, spec.ID)
				must(t, err)
				if left, err := s.query(ctx, "SELECT COUNT(*) FROM mysql.global_priv WHERE User LIKE 'noryx-reader-%'"); err != nil || !slices.Equal(left, []string{"0"}) {
					t.Errorf("readers left: %v, %v", left, err)
				}
			}
		})
	}
}

// The statements that read rows sort by the chosen column and then by the primary key, and
// filter with the value in hexadecimal.
func TestRows(t *testing.T) {
	columns := []column{{Name: "id", Key: "PRI"}, {Name: "name"}, {Name: "skin", Data: "blob"}}
	req := &noryxv1.BrowseTableRequest{Table: "players", Sort: "name", Descending: true, Offset: 50, Limit: 50,
		Filter: &noryxv1.TableFilter{Column: "name", Value: "O'Brien", Contains: true}}
	for engine, want := range map[noryxv1.DatastoreEngine]string{
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB: "SELECT JSON_ARRAY(LEFT(`id`, 201), LEFT(`name`, 201), CONCAT('0x', HEX(LEFT(`skin`, 100)))) FROM `players`" +
			" WHERE LOCATE(CAST(LOWER(_utf8mb4 x'4f27427269656e') AS BINARY), CAST(LOWER(CONVERT(`name` USING utf8mb4)) AS BINARY)) > 0" +
			" ORDER BY `name` DESC, `id` LIMIT 51 OFFSET 50;",
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: `SELECT to_json(ARRAY[left("id"::text, 201), left("name"::text, 201), left("skin"::text, 201)]::text[]) FROM "public"."players"` +
			` WHERE strpos(lower("name"::text), lower(convert_from(decode('4f27427269656e', 'hex'), 'UTF8'))) > 0` +
			` ORDER BY "name" DESC, "id" LIMIT 51 OFFSET 50;`,
	} {
		if got := browsers[engine].rows(req, "public", columns); got != want {
			t.Errorf("%s:\n got %s\nwant %s", engine.Slug(), got, want)
		}
	}
	plain := &noryxv1.BrowseTableRequest{Table: "players", Limit: 1, Filter: &noryxv1.TableFilter{Column: "skin", Value: "0x00"}}
	if got := browsers[noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB].rows(plain, "", columns); !strings.HasSuffix(got,
		" WHERE CAST(CONVERT(CONCAT('0x', HEX(`skin`)) USING utf8mb4) AS BINARY) = x'30783030' ORDER BY `id` LIMIT 2 OFFSET 0;") {
		t.Errorf("filtered by a binary column: %s", got)
	}
}
