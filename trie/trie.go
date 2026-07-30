package trie

import "l1chain/core"

// Get returns the value stored at rawKey under root and whether it was found.
// root == core.Hash{} (the empty-trie sentinel) always reads as not-found,
// with no lookup performed.
func Get(nodes map[core.Hash][]byte, root core.Hash, rawKey []byte) ([]byte, bool) {
	return get(nodes, root, path(rawKey))
}

func get(nodes map[core.Hash][]byte, root core.Hash, p []nibble) ([]byte, bool) {
	if root.IsZero() {
		return nil, false
	}
	enc, ok := nodes[root]
	if !ok {
		return nil, false
	}
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
		return get(nodes, nd.child, p[len(nd.nibbles):])
	case *branchNode:
		if len(p) == 0 {
			if nd.hasValue {
				return nd.value, true
			}
			return nil, false
		}
		return get(nodes, nd.children[p[0]], p[1:])
	default:
		return nil, false
	}
}

// Put inserts or overwrites rawKey -> value and returns the new root. Never
// mutates an existing stored node -- Put/Delete are path-copying, writing new
// nodes and leaving superseded ones as unreferenced garbage (see node.go's
// store doc comment for why that is fine here).
func Put(nodes map[core.Hash][]byte, root core.Hash, rawKey, value []byte) core.Hash {
	return put(nodes, root, path(rawKey), value)
}

func put(nodes map[core.Hash][]byte, root core.Hash, p []nibble, value []byte) core.Hash {
	if root.IsZero() {
		return store(nodes, &leafNode{nibbles: cloneNibbles(p), value: cloneBytes(value)})
	}
	enc, ok := nodes[root]
	if !ok {
		// Root hash not found in the store -- should not happen for a
		// well-formed trie constructed only through this package's own
		// functions. Do not silently drop the write: treat the position as
		// if it were empty so the value is not lost.
		return store(nodes, &leafNode{nibbles: cloneNibbles(p), value: cloneBytes(value)})
	}
	n, err := decodeNode(enc)
	if err != nil {
		return store(nodes, &leafNode{nibbles: cloneNibbles(p), value: cloneBytes(value)})
	}
	switch nd := n.(type) {
	case *leafNode:
		return putIntoLeaf(nodes, nd, p, value)
	case *extensionNode:
		return putIntoExtension(nodes, nd, p, value)
	case *branchNode:
		return putIntoBranch(nodes, nd, p, value)
	default:
		panic("trie: unreachable node type")
	}
}

func putIntoLeaf(nodes map[core.Hash][]byte, nd *leafNode, p []nibble, value []byte) core.Hash {
	cp := commonPrefixLen(p, nd.nibbles)
	if cp == len(nd.nibbles) && cp == len(p) {
		// Same key: overwrite.
		return store(nodes, &leafNode{nibbles: cloneNibbles(p), value: cloneBytes(value)})
	}

	var branch branchNode
	if cp < len(nd.nibbles) {
		idx := nd.nibbles[cp]
		child := &leafNode{nibbles: cloneNibbles(nd.nibbles[cp+1:]), value: nd.value}
		branch.children[idx] = store(nodes, child)
	} else {
		// The existing leaf's path terminates exactly at the branch point.
		branch.hasValue = true
		branch.value = nd.value
	}
	if cp < len(p) {
		idx := p[cp]
		child := &leafNode{nibbles: cloneNibbles(p[cp+1:]), value: cloneBytes(value)}
		branch.children[idx] = store(nodes, child)
	} else {
		branch.hasValue = true
		branch.value = cloneBytes(value)
	}

	branchHash := store(nodes, &branch)
	if cp > 0 {
		return store(nodes, &extensionNode{nibbles: cloneNibbles(p[:cp]), child: branchHash})
	}
	return branchHash
}

func putIntoExtension(nodes map[core.Hash][]byte, nd *extensionNode, p []nibble, value []byte) core.Hash {
	cp := commonPrefixLen(p, nd.nibbles)
	if cp == len(nd.nibbles) {
		newChild := put(nodes, nd.child, p[cp:], value)
		return store(nodes, &extensionNode{nibbles: cloneNibbles(nd.nibbles), child: newChild})
	}

	// Partial match: split the extension into a branch at cp.
	var branch branchNode
	oldIdx := nd.nibbles[cp]
	oldRem := nd.nibbles[cp+1:]
	if len(oldRem) == 0 {
		branch.children[oldIdx] = nd.child
	} else {
		branch.children[oldIdx] = store(nodes, &extensionNode{nibbles: cloneNibbles(oldRem), child: nd.child})
	}

	if cp < len(p) {
		newIdx := p[cp]
		leaf := &leafNode{nibbles: cloneNibbles(p[cp+1:]), value: cloneBytes(value)}
		branch.children[newIdx] = store(nodes, leaf)
	} else {
		branch.hasValue = true
		branch.value = cloneBytes(value)
	}

	branchHash := store(nodes, &branch)
	if cp > 0 {
		return store(nodes, &extensionNode{nibbles: cloneNibbles(p[:cp]), child: branchHash})
	}
	return branchHash
}

func putIntoBranch(nodes map[core.Hash][]byte, nd *branchNode, p []nibble, value []byte) core.Hash {
	newBranch := *nd
	if len(p) == 0 {
		newBranch.hasValue = true
		newBranch.value = cloneBytes(value)
	} else {
		idx := p[0]
		newBranch.children[idx] = put(nodes, nd.children[idx], p[1:], value)
	}
	return store(nodes, &newBranch)
}

// Delete removes rawKey if present and returns the new root. Deleting an
// absent key is a safe no-op returning root unchanged -- callers (e.g. the
// exchange package clearing order-book slots that may already be empty)
// routinely delete keys that were never set.
func Delete(nodes map[core.Hash][]byte, root core.Hash, rawKey []byte) core.Hash {
	return del(nodes, root, path(rawKey))
}

func del(nodes map[core.Hash][]byte, root core.Hash, p []nibble) core.Hash {
	if root.IsZero() {
		return root
	}
	enc, ok := nodes[root]
	if !ok {
		return root
	}
	n, err := decodeNode(enc)
	if err != nil {
		return root
	}
	switch nd := n.(type) {
	case *leafNode:
		if nibblesEqual(nd.nibbles, p) {
			return core.Hash{}
		}
		return root
	case *extensionNode:
		if len(p) < len(nd.nibbles) || !nibblesEqual(nd.nibbles, p[:len(nd.nibbles)]) {
			return root
		}
		newChild := del(nodes, nd.child, p[len(nd.nibbles):])
		if newChild.IsZero() {
			return core.Hash{}
		}
		if newChild == nd.child {
			return root // nothing actually changed below; avoid pointless re-store
		}
		return mergeIntoExtensionParent(nodes, nd.nibbles, newChild)
	case *branchNode:
		if len(p) == 0 {
			if !nd.hasValue {
				return root
			}
			newBranch := *nd
			newBranch.hasValue = false
			newBranch.value = nil
			return collapseBranch(nodes, &newBranch)
		}
		idx := p[0]
		if nd.children[idx].IsZero() {
			return root
		}
		newChild := del(nodes, nd.children[idx], p[1:])
		if newChild == nd.children[idx] {
			return root
		}
		newBranch := *nd
		newBranch.children[idx] = newChild
		return collapseBranch(nodes, &newBranch)
	default:
		return root
	}
}

// mergeIntoExtensionParent rebuilds the node that used to be
// Extension(prefix, oldChild) after oldChild's subtree changed to a new,
// non-empty newChild, merging prefix into the child if it is now a Leaf or
// Extension. An Extension must never point at another Extension or a Leaf --
// keeping that shape canonical is what makes the resulting root independent
// of insertion/deletion history, not just of insertion order.
func mergeIntoExtensionParent(nodes map[core.Hash][]byte, prefix []nibble, newChild core.Hash) core.Hash {
	enc, ok := nodes[newChild]
	if !ok {
		return store(nodes, &extensionNode{nibbles: cloneNibbles(prefix), child: newChild})
	}
	n, err := decodeNode(enc)
	if err != nil {
		return store(nodes, &extensionNode{nibbles: cloneNibbles(prefix), child: newChild})
	}
	switch c := n.(type) {
	case *leafNode:
		return store(nodes, &leafNode{nibbles: concatNibbles(prefix, c.nibbles), value: c.value})
	case *extensionNode:
		return store(nodes, &extensionNode{nibbles: concatNibbles(prefix, c.nibbles), child: c.child})
	default: // *branchNode
		return store(nodes, &extensionNode{nibbles: cloneNibbles(prefix), child: newChild})
	}
}

// collapseBranch rebuilds a branch after one of its children (or its own
// value) changed, collapsing it if it no longer has enough children/value to
// remain a branch:
//   - 0 children, no value -> empty (core.Hash{})
//   - 0 children, a value  -> Leaf(nil, value): the value now terminates here
//   - 1 child, no value    -> merge the child's nibble into it
//   - otherwise (2+ children, or 1 child + a value) -> stays a Branch
func collapseBranch(nodes map[core.Hash][]byte, nb *branchNode) core.Hash {
	onlyIdx := -1
	count := 0
	for i, c := range nb.children {
		if !c.IsZero() {
			count++
			onlyIdx = i
		}
	}
	switch {
	case count == 0 && !nb.hasValue:
		return core.Hash{}
	case count == 0 && nb.hasValue:
		return store(nodes, &leafNode{nibbles: nil, value: nb.value})
	case count == 1 && !nb.hasValue:
		return mergeIntoExtensionParent(nodes, []nibble{nibble(onlyIdx)}, nb.children[onlyIdx])
	default:
		return store(nodes, nb)
	}
}

func nibblesEqual(a, b []nibble) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func concatNibbles(a, b []nibble) []nibble {
	out := make([]nibble, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func cloneNibbles(ns []nibble) []nibble {
	out := make([]nibble, len(ns))
	copy(out, ns)
	return out
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
