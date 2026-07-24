// Package evm is the B1 "full equivalence" leg of the hybrid VM strategy for
// this L1 (milestone M4). It embeds go-ethereum's production EVM
// (github.com/ethereum/go-ethereum/core/vm) executing over a real go-ethereum
// state (github.com/ethereum/go-ethereum/core/state) backed by
// rawdb.NewMemoryDatabase + triedb, i.e. genuine keccak256 hashing and a real
// Merkle-Patricia-Trie. This lets us deploy and exercise a standard ERC-20 with
// Solidity-compatible semantics (keccak-derived storage-mapping slots, EVM
// revert semantics, real gas accounting).
//
// This complements the B2 leg — the custom, from-scratch StackVM in
// ../vm/stackvm.go which is the pedagogical subset VM delivered in M3. The two
// legs coexist deliberately:
//
//   - B2 (vm/stackvm.go): a hand-written stack machine used to learn/execute a
//     restricted opcode subset with our own gas model. It is the chain's
//     experimental execution surface.
//   - B1 (this package): go-ethereum's battle-tested EVM, giving us
//     Solidity/EVM equivalence for real contract bytecode.
//
// Full opcode + precompile + MPT chain-consensus equivalence (i.e. wiring this
// EVM in as the canonical state-transition executor so block hashes match
// go-ethereum byte-for-byte) is an explicit LONG-TERM goal and is NOT required
// for M4 sign-off. M4 demonstrates the EVM execution capability and proves that
// a real ERC-20 transfer updates keccak-mapped MPT storage.
package evm
