package noryxv1

import (
	"strings"
	"testing"
)

// Names of databases and tables go into SQL without quoting, so they only have characters
// that need none.
func TestDatastoreNames(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"luckperms", ""},
		{"lp_2", ""},
		{"a" + strings.Repeat("b", 31), ""},
		{"", "Use up to 32"},
		{"2lp", "Use up to 32"},
		{"LuckPerms", "Use up to 32"},
		{"luck-perms", "Use up to 32"},
		{"lp;drop", "Use up to 32"},
		{"a" + strings.Repeat("b", 32), "Use up to 32"},
		{"mysql", "engine uses"},
		{"information_schema", "engine uses"},
		{"postgres", "engine uses"},
		{"template1", "engine uses"},
		{"public", "engine uses"},
		{"root", "engine uses"},
		{"pg_catalog", "engine uses"},
	} {
		checkProblem(t, tc.name, DatabaseNameProblem(tc.name), tc.want)
	}
	checkValid(t, DatastoreName.MatchString,
		[]string{"main", "0db", "my-db", strings.Repeat("a", 32)},
		[]string{"", "-db", "My", "my_db", "my.db", strings.Repeat("a", 33)})
	checkValid(t, DatabasePassword.MatchString,
		[]string{strings.Repeat("a2", 16), "abcdefghijklmnopqrstuvwxyz234567"},
		[]string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("1", 32), strings.Repeat("8", 32)})
	checkValid(t, TableName.MatchString,
		[]string{"users", "luckperms_players", "Mixed-Case$1", strings.Repeat("a", 64)},
		[]string{"", "a b", "a;b", "a`b", "a.b", "a'b", `a"b`, "a\x00", strings.Repeat("a", 65)})
}

// The agent puts the value of a filter into SQL in hexadecimal, which both engines take as
// text without NUL.
func TestTableFilterProblem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter *TableFilter
		want   string
	}{
		{"a value", &TableFilter{Column: "name", Value: "Steve"}, ""},
		{"no value", &TableFilter{Column: "uuid"}, ""},
		{"quotes", &TableFilter{Column: "name", Value: `'; DROP TABLE users; --`}, ""},
		{"as long as allowed", &TableFilter{Column: "name", Value: strings.Repeat("a", MaxFilterValue)}, ""},
		{"no column", &TableFilter{Value: "Steve"}, "column"},
		{"an expression as column", &TableFilter{Column: "name OR 1=1", Value: "Steve"}, "column"},
		{"a long value", &TableFilter{Column: "name", Value: strings.Repeat("a", MaxFilterValue+1)}, "up to 1024 bytes"},
		{"NUL", &TableFilter{Column: "name", Value: "a\x00"}, "without NUL"},
		{"not UTF-8", &TableFilter{Column: "name", Value: "\xff"}, "without NUL"},
	} {
		checkProblem(t, tc.name, tc.filter.Problem(), tc.want)
	}
}

func TestDatastoreParameters(t *testing.T) {
	const maria, postgres = DatastoreEngine_DATASTORE_ENGINE_MARIADB, DatastoreEngine_DATASTORE_ENGINE_POSTGRES
	for _, tc := range []struct {
		name      string
		got, want uint32
	}{
		{"default connections of MariaDB", maria.DefaultConnections(), 151},
		{"default connections of PostgreSQL", postgres.DefaultConnections(), 100},
		{"connections of MariaDB with 512 MB", maria.MaxConnections(512), 512},
		{"connections of PostgreSQL with 512 MB", postgres.MaxConnections(512), 256},
		{"connections of MariaDB with 4 GB", maria.MaxConnections(4096), MaxDatastoreConnections},
		{"buffer of MariaDB with 1 GB", maria.DefaultBufferMB(1024), 512},
		{"buffer of MariaDB with 128 MB", maria.DefaultBufferMB(128), 128},
		{"buffer of PostgreSQL with 1 GB", postgres.DefaultBufferMB(1024), 256},
		{"buffer of PostgreSQL with 256 MB", postgres.DefaultBufferMB(256), 128},
		{"largest buffer with 1 GB", MaxBufferMB(1024), 768},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	for _, tc := range []struct {
		name   string
		engine DatastoreEngine
		params *DatastoreParameters
		want   string
	}{
		{"the defaults", maria, &DatastoreParameters{}, ""},
		{"the fewest connections", maria, &DatastoreParameters{MaxConnections: MinDatastoreConnections}, ""},
		{"the most connections", maria, &DatastoreParameters{MaxConnections: 512}, ""},
		{"the most connections of PostgreSQL", postgres, &DatastoreParameters{MaxConnections: 256}, ""},
		{"the smallest buffer", maria, &DatastoreParameters{BufferMb: MinDatastoreBufferMB}, ""},
		{"the largest buffer", postgres, &DatastoreParameters{BufferMb: 384}, ""},
		{"too few connections", maria, &DatastoreParameters{MaxConnections: MinDatastoreConnections - 1}, "allow from 10 to 512 connections"},
		{"too many connections", maria, &DatastoreParameters{MaxConnections: 513}, "allow from 10 to 512 connections"},
		{"too many connections of PostgreSQL", postgres, &DatastoreParameters{MaxConnections: 257}, "allow from 10 to 256 connections"},
		{"a small buffer", maria, &DatastoreParameters{BufferMb: MinDatastoreBufferMB - 1}, "from 32 to 384 MB"},
		{"a large buffer", maria, &DatastoreParameters{BufferMb: 385}, "from 32 to 384 MB"},
	} {
		checkProblem(t, tc.name, tc.params.Problem(tc.engine, 512), tc.want)
	}
}
