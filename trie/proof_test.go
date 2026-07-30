package trie

import (
	"testing"

	"l1chain/core"
)

func TestProofRoundTripValid(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	kvs := map[string]string{"a": "1", "b": "2", "c": "3", "aardvark": "4"}
	for k, v := range kvs {
		root = Put(nodes, root, []byte(k), []byte(v))
	}
	for k, v := range kvs {
		proof, value, found := GenerateProof(nodes, root, []byte(k))
		if !found || string(value) != v {
			t.Fatalf("generate %q: found=%v value=%q", k, found, value)
		}
		if !VerifyProof(root, []byte(k), value, proof) {
			t.Fatalf("valid proof for %q rejected", k)
		}
	}
}

func TestProofGenerateMissingKeyReportsNotFound(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := Put(nodes, core.Hash{}, []byte("k"), []byte("v"))
	_, _, found := GenerateProof(nodes, root, []byte("missing"))
	if found {
		t.Fatal("expected not found for a key that was never inserted")
	}
}

func TestProofRejectsTamperedNode(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	root = Put(nodes, root, []byte("k1"), []byte("v1"))
	root = Put(nodes, root, []byte("k2"), []byte("v2"))
	proof, value, found := GenerateProof(nodes, root, []byte("k1"))
	if !found {
		t.Fatal("expected found")
	}

	tampered := make(Proof, len(proof))
	for i, e := range proof {
		cp := make([]byte, len(e))
		copy(cp, e)
		tampered[i] = cp
	}
	tampered[0][len(tampered[0])-1] ^= 0xff

	if VerifyProof(root, []byte("k1"), value, tampered) {
		t.Fatal("tampered proof accepted")
	}
}

func TestProofRejectsWrongValue(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := Put(nodes, core.Hash{}, []byte("k"), []byte("v"))
	proof, _, found := GenerateProof(nodes, root, []byte("k"))
	if !found {
		t.Fatal("expected found")
	}
	if VerifyProof(root, []byte("k"), []byte("wrong-value"), proof) {
		t.Fatal("wrong claimed value accepted")
	}
}

func TestProofRejectsWrongKey(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	root = Put(nodes, root, []byte("k1"), []byte("v1"))
	root = Put(nodes, root, []byte("k2"), []byte("v2"))
	proof, value, found := GenerateProof(nodes, root, []byte("k1"))
	if !found {
		t.Fatal("expected found")
	}
	if VerifyProof(root, []byte("k2"), value, proof) {
		t.Fatal("k1's proof accepted as a proof for k2")
	}
}

func TestProofRejectsWrongRoot(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root1 := Put(nodes, core.Hash{}, []byte("k"), []byte("v"))
	root2 := Put(nodes, root1, []byte("k2"), []byte("v2")) // different root
	proof, value, found := GenerateProof(nodes, root1, []byte("k"))
	if !found {
		t.Fatal("expected found")
	}
	if VerifyProof(root2, []byte("k"), value, proof) {
		t.Fatal("proof against root1 accepted against a different root2")
	}
}

func TestProofEmptyProofRejected(t *testing.T) {
	if VerifyProof(core.Hash{}, []byte("k"), []byte("v"), nil) {
		t.Fatal("an empty proof against the empty-trie root must not verify anything")
	}
}
