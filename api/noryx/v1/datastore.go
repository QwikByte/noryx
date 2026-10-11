package noryxv1

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	// DatabaseName matches the names of databases, which their users share. They need no
	// quoting in SQL and fit both engines.
	DatabaseName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// DatabasePassword matches the passwords the master generates for the users of databases.
	DatabasePassword = regexp.MustCompile(`^[a-z2-7]{32}$`)
	// DatastoreName matches the names of the datastores of a network.
	DatastoreName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// TableName matches the names of the tables, schemas and columns that the agent shows,
	// which need no escaping in SQL.
	TableName = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)
)

// Limits of datastores, which master and agent both check.
const (
	MaxDatabases         = 50 // of a datastore
	MaxNetworkDatastores = 10
	// MaxImportedDump is the most bytes of an uploaded dump.
	MaxImportedDump = 16 << 30
	// MaxFilterValue is the most bytes of the value that a filter of a table compares.
	MaxFilterValue = 1 << 10
)

// reservedDatabases are the databases and users of the engines themselves.
var reservedDatabases = []string{"information_schema", "mysql", "performance_schema", "sys", "root", "postgres", "template0", "template1", "public"}

// DatabaseNameProblem returns why a database can't have a name, or "".
func DatabaseNameProblem(name string) string {
	switch {
	case !DatabaseName.MatchString(name):
		return "Use up to 32 lower-case letters, digits and underscores, starting with a letter."
	case slices.Contains(reservedDatabases, name) || strings.HasPrefix(name, "pg_"):
		return "The database engine uses this name itself."
	}
	return ""
}

// Problem returns why a filter of a table can't be used, or "". The agent puts the value into
// statements in hexadecimal, so it needs no escaping, but it must be text that both engines
// take: UTF-8 without NUL.
func (f *TableFilter) Problem() string {
	switch v := f.GetValue(); {
	case !TableName.MatchString(f.GetColumn()):
		return "Choose a column to filter by."
	case len(v) > MaxFilterValue:
		return fmt.Sprintf("Filter by up to %d bytes.", MaxFilterValue)
	case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
		return "Filter by text without NUL characters."
	}
	return ""
}

// Bounds of the parameters of datastores, which master and agent both check.
const (
	MinDatastoreConnections = 10
	MaxDatastoreConnections = 1000
	MinDatastoreBufferMB    = 32
)

// DefaultConnections returns the connections at once that an engine takes by default, as many
// as its image does.
func (e DatastoreEngine) DefaultConnections() uint32 {
	if e == DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		return 100
	}
	return 151
}

// MaxConnections returns the most connections at once that an engine may take with memoryMB,
// as each takes memory of its own: one for each MB on MariaDB, and for each 2 MB on PostgreSQL,
// which runs a process for each.
func (e DatastoreEngine) MaxConnections(memoryMB uint32) uint32 {
	if e == DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		memoryMB /= 2
	}
	return min(memoryMB, MaxDatastoreConnections)
}

// DefaultBufferMB returns the MiB of the cache of data that an engine gets with memoryMB by
// default: half of it for MariaDB, as InnoDB reads past the cache of the system, and a quarter
// for PostgreSQL, which relies on that cache within the memory too, but at least the 128 MiB
// of their images, which datastores with the least memory had before.
func (e DatastoreEngine) DefaultBufferMB(memoryMB uint32) uint32 {
	if e == DatastoreEngine_DATASTORE_ENGINE_POSTGRES {
		return max(memoryMB/4, 128)
	}
	return max(memoryMB/2, 128)
}

// MaxBufferMB returns the most MiB of the cache of data with memoryMB: three quarters of it,
// so that the connections and the engine itself have room.
func MaxBufferMB(memoryMB uint32) uint32 { return memoryMB - memoryMB/4 }

// Problem returns why a datastore of an engine with memoryMB can't have the parameters, or "".
func (p *DatastoreParameters) Problem(e DatastoreEngine, memoryMB uint32) string {
	switch c, b := p.GetMaxConnections(), p.GetBufferMb(); {
	case c != 0 && (c < MinDatastoreConnections || c > e.MaxConnections(memoryMB)):
		return fmt.Sprintf("With %d MB of memory, allow from %d to %d connections.", memoryMB, MinDatastoreConnections, e.MaxConnections(memoryMB))
	case b != 0 && (b < MinDatastoreBufferMB || b > MaxBufferMB(memoryMB)):
		return fmt.Sprintf("With %d MB of memory, give the buffers from %d to %d MB.", memoryMB, MinDatastoreBufferMB, MaxBufferMB(memoryMB))
	}
	return ""
}

// Slug returns the short lower-case name, e.g. "mariadb" for DATASTORE_ENGINE_MARIADB.
func (e DatastoreEngine) Slug() string { return slug(e.String(), "DATASTORE_ENGINE_") }

// Slug returns the short lower-case name, e.g. "running" for DATASTORE_STATE_RUNNING.
func (s DatastoreState) Slug() string { return slug(s.String(), "DATASTORE_STATE_") }
