package trie

import (
	"testing"

	"l1chain/core"
)

func TestEmptyTrieRootIsZero(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	if _, found := Get(nodes, core.Hash{}, []byte("anything")); found {
		t.Fatal("expected not found against the empty-trie sentinel root")
	}
}

func TestGetPutRoundTrip(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	kvs := map[string]string{"alpha": "1", "beta": "2", "gamma": "3", "delta": "4"}
	for k, v := range kvs {
		root = Put(nodes, root, []byte(k), []byte(v))
	}
	for k, v := range kvs {
		got, found := Get(nodes, root, []byte(k))
		if !found {
			t.Fatalf("key %q not found", k)
		}
		if string(got) != v {
			t.Fatalf("key %q: got %q want %q", k, got, v)
		}
	}
	if _, found := Get(nodes, root, []byte("never-inserted")); found {
		t.Fatal("expected a never-inserted key to read as not found")
	}
}

func TestPutOverwriteExistingKey(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := Put(nodes, core.Hash{}, []byte("k"), []byte("v1"))
	root = Put(nodes, root, []byte("k"), []byte("v2"))
	got, found := Get(nodes, root, []byte("k"))
	if !found || string(got) != "v2" {
		t.Fatalf("got %q found=%v, want v2", got, found)
	}
}

func TestInsertionOrderIndependence(t *testing.T) {
	kvs := [][2]string{
		{"apple", "1"}, {"application", "2"}, {"apply", "3"},
		{"banana", "4"}, {"band", "5"}, {"bandana", "6"},
		{"cat", "7"}, {"", "8"},
	}
	nodesA := map[core.Hash][]byte{}
	rootA := core.Hash{}
	for _, kv := range kvs {
		rootA = Put(nodesA, rootA, []byte(kv[0]), []byte(kv[1]))
	}
	nodesB := map[core.Hash][]byte{}
	rootB := core.Hash{}
	for i := len(kvs) - 1; i >= 0; i-- {
		rootB = Put(nodesB, rootB, []byte(kvs[i][0]), []byte(kvs[i][1]))
	}
	if rootA != rootB {
		t.Fatalf("roots differ by insertion order: %x vs %x", rootA, rootB)
	}
}

func TestDeleteRestoresEmptyRoot(t *testing.T) {
	kvs := [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"ab", "4"}, {"abc", "5"}}
	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	for _, kv := range kvs {
		root = Put(nodes, root, []byte(kv[0]), []byte(kv[1]))
	}
	for _, kv := range kvs {
		root = Delete(nodes, root, []byte(kv[0]))
	}
	if root != (core.Hash{}) {
		t.Fatalf("expected zero root after deleting everything, got %x", root)
	}
}

func TestDeleteToEmptyMatchesNeverInserted(t *testing.T) {
	nodesA := map[core.Hash][]byte{}
	rootA := Put(nodesA, core.Hash{}, []byte("x"), []byte("1"))
	rootA = Delete(nodesA, rootA, []byte("x"))

	rootB := core.Hash{} // never had anything inserted

	if rootA != rootB {
		t.Fatalf("insert-then-delete-back-to-nothing root %x differs from never-inserted root %x", rootA, rootB)
	}
}

func TestDeleteAbsentKeyIsNoOp(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := Put(nodes, core.Hash{}, []byte("a"), []byte("1"))
	got := Delete(nodes, root, []byte("nonexistent"))
	if got != root {
		t.Fatalf("deleting an absent key changed the root: %x -> %x", root, got)
	}
	// Also against the empty trie itself.
	if got := Delete(nodes, core.Hash{}, []byte("anything")); got != (core.Hash{}) {
		t.Fatalf("deleting from an empty trie changed the root: %x", got)
	}
}

func TestLeafSplitOnDivergence(t *testing.T) {
	nodes := map[core.Hash][]byte{}
	root := Put(nodes, core.Hash{}, []byte("x"), []byte("1"))
	root = Put(nodes, root, []byte("y"), []byte("2"))
	v1, ok1 := Get(nodes, root, []byte("x"))
	v2, ok2 := Get(nodes, root, []byte("y"))
	if !ok1 || string(v1) != "1" {
		t.Fatalf("x: got %q ok=%v", v1, ok1)
	}
	if !ok2 || string(v2) != "2" {
		t.Fatalf("y: got %q ok=%v", v2, ok2)
	}
}

// TestManyKeysInsertDeleteRoundTrip inserts and deletes enough keys, in
// several different orders, that every split/collapse branch in Put/Delete
// (leaf split, extension split, branch collapse to leaf/extension/empty) is
// exercised many times over -- secure-trie hashing makes it impractical to
// hand-pick nibble paths that hit one specific case deterministically, so
// bulk insert/delete across many keys and orderings is the practical way to
// get real coverage of the split/collapse logic.
func TestManyKeysInsertDeleteRoundTrip(t *testing.T) {
	const n = 200
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte{byte(i), byte(i * 7), byte(i * 13)}
	}

	nodes := map[core.Hash][]byte{}
	root := core.Hash{}
	for i, k := range keys {
		root = Put(nodes, root, k, []byte{byte(i)})
	}
	for i, k := range keys {
		v, ok := Get(nodes, root, k)
		if !ok || len(v) != 1 || v[0] != byte(i) {
			t.Fatalf("key %d: got %v ok=%v", i, v, ok)
		}
	}

	// Delete every other key; confirm the deleted ones are gone and the rest
	// are still exactly right.
	for i := 0; i < n; i += 2 {
		root = Delete(nodes, root, keys[i])
	}
	for i, k := range keys {
		v, ok := Get(nodes, root, k)
		if i%2 == 0 {
			if ok {
				t.Fatalf("key %d still present after delete", i)
			}
			continue
		}
		if !ok || len(v) != 1 || v[0] != byte(i) {
			t.Fatalf("key %d: got %v ok=%v", i, v, ok)
		}
	}

	// Delete everything else too; must land on the exact same zero root as a
	// trie that never had anything inserted.
	for i := 1; i < n; i += 2 {
		root = Delete(nodes, root, keys[i])
	}
	if root != (core.Hash{}) {
		t.Fatalf("expected zero root after deleting every key, got %x", root)
	}
}
