package adapter

// journalEntry is an undo closure: applying it restores s to the value it
// held immediately before the mutation that recorded this entry.
type journalEntry func(s *StateDB)

// journal is a linear undo-log: Snapshot() = len(entries); RevertToSnapshot
// pops and undoes entries in reverse (LIFO) order back down to that length.
//
// This is a new shape, not a reuse of either existing "stage writes, flush
// later" pattern already in this codebase: chain.overlayState's dirty map
// (one flat layer, no nesting) and vm.scope's parent/child chain (nesting,
// but no NUMBERED checkpoint a caller can jump back to out of order) both
// fall short of vm.StateDB's actual contract -- a call can take several
// nested snapshots and revert to any one of them, discarding everything
// recorded after it, which only an ordered, indexable log supports.
type journal struct {
	entries []journalEntry
}

// snapshot returns an id that revert can later roll back to.
func (j *journal) snapshot() int {
	return len(j.entries)
}

// revert undoes every entry recorded since id, most recent first, then
// truncates the log. Matches go-ethereum's own Snapshot/RevertToSnapshot
// contract: id becomes invalid to revert to again once used.
func (j *journal) revert(s *StateDB, id int) {
	for i := len(j.entries) - 1; i >= id; i-- {
		j.entries[i](s)
	}
	j.entries = j.entries[:id]
}
