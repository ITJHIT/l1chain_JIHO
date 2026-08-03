// Package chain implements the block/state transition engine for M1: genesis
// construction, the account state-transition function, block application with a
// coinbase reward, and a longest-chain (heaviest cumulative difficulty) engine
// with reorg support.
//
// It depends only on the core, state and consensus packages and the Go stdlib.
// Signature verification is intentionally pluggable (a func(core.Transaction)
// bool): the real secp256k1 verifier is injected by a later slice; for now the
// default accepts any non-empty signature.
package chain

import (
	"l1chain/core"
	"l1chain/exchange"
	"l1chain/state"
)

// Genesis describes the initial allocation and PoW parameters of a chain.
type Genesis struct {
	Alloc      map[core.Address]uint64 // address -> starting native/quote balance
	// BaseAlloc credits the exchange's base asset at genesis, parallel to Alloc.
	// It is the only mint path a real chain can reach: exchange.CreditBase is
	// otherwise only ever called by tests against a hand-built StateDB, never
	// through Genesis/Chain, so no real crossing trade has ever had a funded
	// sell side (see rpc/exchange_crossing_e2e_test.go for the closed gap).
	BaseAlloc  map[core.Address]uint64
	Difficulty uint32 // difficulty carried in the genesis header
	Timestamp  int64  // genesis header timestamp

	// InitialBaseFee (M9) seeds the genesis header's BaseFee -- there is no
	// parent block to derive it from (chain.ComputeBaseFee needs a parent),
	// exactly like real EIP-1559's own London bootstrap rule. The genesis
	// block carries no transactions, so GasUsed is always 0 regardless.
	InitialBaseFee uint64
}

// ApplyGenesis funds the alloc accounts into st and returns the genesis block
// (height 0, zero PrevHash, no transactions, no PoW). The block's StateRoot is
// the root of the funded state; its MerkleRoot is the (empty) tx root.
func ApplyGenesis(st state.StateDB, g Genesis) core.Block {
	for addr, bal := range g.Alloc {
		acct := st.GetAccount(addr)
		acct.Balance += bal
		st.SetAccount(addr, acct)
	}
	for addr, amt := range g.BaseAlloc {
		exchange.CreditBase(st, addr, amt)
	}
	st.Commit()

	h := core.Header{
		Height:     0,
		Timestamp:  g.Timestamp,
		Difficulty: g.Difficulty,
		BaseFee:    g.InitialBaseFee,
	}
	h.StateRoot = st.StateRoot()
	b := core.Block{Header: h}
	b.Header.MerkleRoot = b.TxRoot() // zero hash for an empty tx set
	return b
}

// ToBlock builds the genesis block against a fresh StateDB.
func (g Genesis) ToBlock() core.Block {
	return ApplyGenesis(state.New(), g)
}
