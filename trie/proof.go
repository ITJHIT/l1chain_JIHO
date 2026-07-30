package trie

import (
	"bytes"

	"l1chain/core"
)

// Proof is an ordered list of raw node encodings, root to terminal leaf (or
// terminal branch value). Verified by VerifyProof.
type Proof [][]byte

// GenerateProof walks the same path Get would, collecting every node visited
// along the way, and returns the proof plus the value found (if any).
func GenerateProof(nodes map[core.Hash][]byte, root core.Hash, rawKey []byte) (Proof, []byte, bool) {
	var proof Proof
	value, found := genProof(nodes, root, path(rawKey), &proof)
	return proof, value, found
}

func genProof(nodes map[core.Hash][]byte, root core.Hash, p []nibble, proof *Proof) ([]byte, bool) {
	if root.IsZero() {
		return nil, false
	}
	enc, ok := nodes[root]
	if !ok {
		return nil, false
	}
	*proof = append(*proof, enc)
	n, err := decodeNode(enc)
	if err != nil {
		return nil, false
	}
	switch nd := n.(type) {
	case *leafNode:
		if nibblesEqual(nd.nibbles, p) {
			return nd.value, true
		}
		return nil, false
	case *extensionNode:
		if len(p) < len(nd.nibbles) || !nibblesEqual(nd.nibbles, p[:len(nd.nibbles)]) {
			return nil, false
		}
		return genProof(nodes, nd.child, p[len(nd.nibbles):], proof)
	case *branchNode:
		if len(p) == 0 {
			if nd.hasValue {
				return nd.value, true
			}
			return nil, false
		}
		return genProof(nodes, nd.children[p[0]], p[1:], proof)
	default:
		return nil, false
	}
}

// VerifyProof reports whether proof demonstrates rawKey -> value under root.
// It trusts nothing about proof's shape beyond what it re-derives itself:
// rawKey's path is recomputed independently (never taken from the proof), and
// every proof element must hash to exactly the pointer the previous element
// named, so a tampered or substituted node breaks the chain and the proof is
// rejected. Only proves inclusion (the key exists with this value) -- a
// non-existence proof is a natural, low-cost extension of the same walk but
// is not implemented here.
func VerifyProof(root core.Hash, rawKey, value []byte, proof Proof) bool {
	p := path(rawKey)
	expected := root
	for i, enc := range proof {
		if core.SumHash(enc) != expected {
			return false
		}
		n, err := decodeNode(enc)
		if err != nil {
			return false
		}
		last := i == len(proof)-1
		switch nd := n.(type) {
		case *leafNode:
			if !last {
				return false // a leaf can only be the terminal element
			}
			return nibblesEqual(nd.nibbles, p) && bytes.Equal(nd.value, value)
		case *extensionNode:
			if last {
				return false // an extension is never terminal
			}
			if len(p) < len(nd.nibbles) || !nibblesEqual(nd.nibbles, p[:len(nd.nibbles)]) {
				return false
			}
			p = p[len(nd.nibbles):]
			expected = nd.child
		case *branchNode:
			if len(p) == 0 {
				if !last {
					return false
				}
				return nd.hasValue && bytes.Equal(nd.value, value)
			}
			if last {
				return false // path not exhausted but proof is
			}
			expected = nd.children[p[0]]
			p = p[1:]
		default:
			return false
		}
	}
	return false // proof exhausted without a terminal match
}
