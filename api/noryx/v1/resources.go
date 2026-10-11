package noryxv1

// The container of a server may run DefaultProcessLimit processes and threads, or up to
// MaxProcessLimit if its settings say so, e.g. for a large modded server with many threads. The
// limit keeps a compromised server from exhausting the node, e.g. with a fork bomb.
const (
	DefaultProcessLimit = 1024
	MaxProcessLimit     = 8192
)

// ProcessLimit is the limit of processes and threads of a server with process_limit; 0, as older
// masters and agents send it, means DefaultProcessLimit.
func ProcessLimit(limit uint32) int64 {
	if limit == 0 {
		return DefaultProcessLimit
	}
	return int64(limit)
}

// ValidProcessLimit reports whether process_limit is 0, for the default, or from
// DefaultProcessLimit to MaxProcessLimit.
func ValidProcessLimit(limit uint32) bool {
	return limit == 0 || limit >= DefaultProcessLimit && limit <= MaxProcessLimit
}

// ValidInitMemory reports whether init_memory_mb is 0, for an initial heap as large as the
// memory of the server, or from MinMemoryMB to its memory, memoryMB. Aikar's flags want both
// the same.
func ValidInitMemory(initMB, memoryMB uint32, aikarFlags bool) bool {
	return initMB == 0 || !aikarFlags && initMB >= MinMemoryMB && initMB <= memoryMB
}
