package consensus

// Mode selects which consensus mechanism a Chain uses for its entire
// lifetime, set once via Chain.SetConsensusMode before any block is mined --
// mirrors chainID/exchangeMode's own "set once, right after construction,
// never touched again" discipline (see chain/chain.go). PoW (the zero value)
// is unchanged from M1-M7; PoS is added ADDITIVELY in M8 alongside it, never
// replacing it.
type Mode uint8

const (
	// PoW is the zero value: a Chain that never calls SetConsensusMode
	// behaves exactly as it always has, before this type existed.
	PoW Mode = iota
	// PoS is M8's stake-weighted proposer-selection + BLS-attested checkpoint
	// finality consensus mode.
	PoS
)

// String implements fmt.Stringer for readable logs/CLI output.
func (m Mode) String() string {
	switch m {
	case PoW:
		return "pow"
	case PoS:
		return "pos"
	default:
		return "unknown"
	}
}
