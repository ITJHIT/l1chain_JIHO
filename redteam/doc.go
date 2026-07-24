// Package redteam holds adversarial M1 red-team tests that attempt to break the
// core chain invariants using only the exported l1chain APIs. Each test asserts
// the chain REJECTS or correctly handles a specific attack:
//
//   - double-spend within one block (atomicity)
//   - replay / nonce reuse
//   - signature forgery / wrong signer
//   - invalid Proof-of-Work
//   - tampered StateRoot / MerkleRoot
//   - deep reorg with no cross-branch state leakage
//   - balance underflow / overflow guards
//   - coinbase reward tampering
//
// This package contains no production code and does not modify any public API.
package redteam
