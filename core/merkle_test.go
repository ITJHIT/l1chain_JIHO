package core

import "testing"

func TestMerkleRootEmpty(t *testing.T) {
	if got := MerkleRoot(nil); !got.IsZero() {
		t.Fatalf("empty merkle root should be zero, got %s", got.Hex())
	}
}

func TestMerkleRootSingle(t *testing.T) {
	leaf := SumHash([]byte("tx1"))
	if got := MerkleRoot([]Hash{leaf}); got != leaf {
		t.Fatalf("single-leaf root should equal leaf; got %s want %s", got.Hex(), leaf.Hex())
	}
}

func TestMerkleRootDeterministic(t *testing.T) {
	leaves := []Hash{SumHash([]byte("a")), SumHash([]byte("b")), SumHash([]byte("c"))}
	r1 := MerkleRoot(leaves)
	r2 := MerkleRoot(leaves)
	if r1 != r2 {
		t.Fatalf("merkle root not deterministic: %s vs %s", r1.Hex(), r2.Hex())
	}
}

func TestMerkleRootOrderSensitive(t *testing.T) {
	a, b := SumHash([]byte("a")), SumHash([]byte("b"))
	if MerkleRoot([]Hash{a, b}) == MerkleRoot([]Hash{b, a}) {
		t.Fatal("merkle root must depend on leaf order")
	}
}

func TestMerkleRootOddDuplicatesLast(t *testing.T) {
	a, b, c := SumHash([]byte("a")), SumHash([]byte("b")), SumHash([]byte("c"))
	// 3 leaves duplicate c -> hash(hash(a,b), hash(c,c))
	ab := HashConcat(a[:], b[:])
	cc := HashConcat(c[:], c[:])
	want := HashConcat(ab[:], cc[:])
	if got := MerkleRoot([]Hash{a, b, c}); got != want {
		t.Fatalf("odd-leaf duplication wrong; got %s want %s", got.Hex(), want.Hex())
	}
}
