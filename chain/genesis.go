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
	"l1chain/state"
)

// Genesis describes the initial allocation and PoW parameters of a chain.
type Genesis struct {
	Alloc      map[core.Address]uint64 // address -> starting balance
	Difficulty uint32                  // difficulty carried in the genesis header
	Timestamp  int64                   // genesis header timestamp
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
	st.Commit()

	h := core.Header{
		Height:     0,
		Timestamp:  g.Timestamp,
		Difficulty: g.Difficulty,
	}
	h.StateRoot = st.StateRoot()
	b := core.Block{Header: h}
	b.Header.MerkleRoot = b.TxRoot() // zero hash for an empty tx set
	return b
}

// ToBlock builds the genesis block against a fresh in-memory StateDB.
func (g Genesis) ToBlock() core.Block {
	return ApplyGenesis(state.NewMemStateDB(), g)
}
