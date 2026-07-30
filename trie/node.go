// Package trie implements a from-scratch, SHA-256-based Merkle Patricia Trie
// (MPT), key-agnostic: it operates on opaque []byte keys and values, so the
// same package serves both the state package's world (account) trie and every
// account's own per-contract storage trie with no special-casing.
//
// Keys are secure-hashed (core.SumHash) before deriving a nibble path, so an
// attacker who chooses a key (an address, or -- more pointedly -- a contract
// storage slot the exchange package lets callers influence) cannot also choose
// the trie's shape. A side effect: every path is exactly 64 nibbles regardless
// of whether the original key was a 20-byte address or a 32-byte storage word,
// which is why one generic trie serves both levels.
//
// Node encoding is a tag byte followed by binary.BigEndian fixed/length-
// prefixed fields -- the same house style as core.Transaction.preimage() and
// core.Header.preimage() -- rather than RLP, which nothing else in this
// codebase uses. A node's hash is SHA-256 of its own encoding; the tag byte
// being the first byte of every encoding gives free domain separation between
// node kinds (a Leaf's bytes can never collide with a Branch's), and every
// child reference is always a full 32-byte hash pointer -- no size-based
// inline-embedding shortcut -- so there is never ambiguity about whether a
// stored value is a hash or a raw node.
package trie

import (
	"encoding/binary"
	"errors"

	"l1chain/core"
)

// tag bytes: 0x00 is reserved (never encoded -- "no node" is the zero Hash).
const (
	tagLeaf      = 0x01
	tagExtension = 0x02
	tagBranch    = 0x03
)

var errShortInput = errors.New("trie: truncated node encoding")
var errBadTag = errors.New("trie: unknown node tag")

// nibble is a 4-bit value in [0,16). A branch node has exactly 16 numbered
// children (one per nibble value) plus an optional value at the branch itself.
type nibble byte

// nibblesOf expands b into its nibbles, most-significant nibble of byte 0
// first. Used both to derive a secure-hashed key's path (always 64 nibbles,
// since core.Hash is 32 bytes) and, internally, nowhere else -- packed nibbles
// on the wire are unpacked by unpackNibbles below.
func nibblesOf(b []byte) []nibble {
	out := make([]nibble, 0, len(b)*2)
	for _, x := range b {
		out = append(out, nibble(x>>4), nibble(x&0x0f))
	}
	return out
}

// path derives the secure-trie nibble path for a raw key: hash it, then
// expand the digest into nibbles. Every caller into this package that starts
// a Get/Put/Delete/GenerateProof from a raw key must go through this function
// -- and, symmetrically, every proof verifier recomputes it independently
// from the raw key it is given rather than trusting a supplied path (see
// proof.go) -- so a path can never be chosen independently of the key it
// claims to represent.
func path(key []byte) []nibble {
	h := core.SumHash(key)
	return nibblesOf(h[:])
}

func commonPrefixLen(a, b []nibble) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// packNibbles packs nibbles two-per-byte, high nibble first; the final byte's
// low nibble is zero-padded when len(ns) is odd. The pad bit is never
// ambiguous on decode because the nibble count is carried explicitly
// alongside the packed bytes (unlike a hex-prefix flag-nibble scheme).
func packNibbles(ns []nibble) []byte {
	out := make([]byte, (len(ns)+1)/2)
	for i, n := range ns {
		if i%2 == 0 {
			out[i/2] = byte(n) << 4
		} else {
			out[i/2] |= byte(n)
		}
	}
	return out
}

func unpackNibbles(b []byte, count int) []nibble {
	out := make([]nibble, count)
	for i := 0; i < count; i++ {
		by := b[i/2]
		if i%2 == 0 {
			out[i] = nibble(by >> 4)
		} else {
			out[i] = nibble(by & 0x0f)
		}
	}
	return out
}

// node is any of leafNode, extensionNode, branchNode.
type node interface {
	encode() []byte
}

type leafNode struct {
	nibbles []nibble
	value   []byte
}

type extensionNode struct {
	nibbles []nibble
	child   core.Hash
}

type branchNode struct {
	children [16]core.Hash
	hasValue bool
	value    []byte
}

func (n *leafNode) encode() []byte {
	packed := packNibbles(n.nibbles)
	out := make([]byte, 0, 1+2+len(packed)+4+len(n.value))
	out = append(out, tagLeaf)
	out = appendU16(out, uint16(len(n.nibbles)))
	out = append(out, packed...)
	out = appendU32(out, uint32(len(n.value)))
	out = append(out, n.value...)
	return out
}

func (n *extensionNode) encode() []byte {
	packed := packNibbles(n.nibbles)
	out := make([]byte, 0, 1+2+len(packed)+32)
	out = append(out, tagExtension)
	out = appendU16(out, uint16(len(n.nibbles)))
	out = append(out, packed...)
	out = append(out, n.child[:]...)
	return out
}

func (n *branchNode) encode() []byte {
	out := make([]byte, 0, 1+16*32+1+4+len(n.value))
	out = append(out, tagBranch)
	for _, c := range n.children {
		out = append(out, c[:]...)
	}
	if n.hasValue {
		out = append(out, 1)
		out = appendU32(out, uint32(len(n.value)))
		out = append(out, n.value...)
	} else {
		out = append(out, 0)
	}
	return out
}

func appendU16(b []byte, v uint16) []byte {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], v)
	return append(b, buf[:]...)
}

func appendU32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

// decodeNode parses a node's own encoding (as produced by encode() above),
// not a hash -- callers look up the encoding by hash in the node store first.
func decodeNode(enc []byte) (node, error) {
	if len(enc) < 1 {
		return nil, errShortInput
	}
	switch enc[0] {
	case tagLeaf:
		return decodeLeaf(enc[1:])
	case tagExtension:
		return decodeExtension(enc[1:])
	case tagBranch:
		return decodeBranch(enc[1:])
	default:
		return nil, errBadTag
	}
}

func decodeLeaf(b []byte) (*leafNode, error) {
	if len(b) < 2 {
		return nil, errShortInput
	}
	count := int(binary.BigEndian.Uint16(b[0:2]))
	b = b[2:]
	packedLen := (count + 1) / 2
	if len(b) < packedLen+4 {
		return nil, errShortInput
	}
	nibbles := unpackNibbles(b[:packedLen], count)
	b = b[packedLen:]
	valLen := int(binary.BigEndian.Uint32(b[0:4]))
	b = b[4:]
	if len(b) < valLen {
		return nil, errShortInput
	}
	value := make([]byte, valLen)
	copy(value, b[:valLen])
	return &leafNode{nibbles: nibbles, value: value}, nil
}

func decodeExtension(b []byte) (*extensionNode, error) {
	if len(b) < 2 {
		return nil, errShortInput
	}
	count := int(binary.BigEndian.Uint16(b[0:2]))
	b = b[2:]
	packedLen := (count + 1) / 2
	if len(b) < packedLen+core.HashLen {
		return nil, errShortInput
	}
	nibbles := unpackNibbles(b[:packedLen], count)
	b = b[packedLen:]
	var child core.Hash
	copy(child[:], b[:core.HashLen])
	return &extensionNode{nibbles: nibbles, child: child}, nil
}

func decodeBranch(b []byte) (*branchNode, error) {
	if len(b) < 16*core.HashLen+1 {
		return nil, errShortInput
	}
	var n branchNode
	for i := 0; i < 16; i++ {
		copy(n.children[i][:], b[i*core.HashLen:(i+1)*core.HashLen])
	}
	b = b[16*core.HashLen:]
	hasValue := b[0]
	b = b[1:]
	if hasValue == 1 {
		if len(b) < 4 {
			return nil, errShortInput
		}
		valLen := int(binary.BigEndian.Uint32(b[0:4]))
		b = b[4:]
		if len(b) < valLen {
			return nil, errShortInput
		}
		n.hasValue = true
		n.value = make([]byte, valLen)
		copy(n.value, b[:valLen])
	}
	return &n, nil
}

// store hashes n, writes its encoding into nodes keyed by that hash, and
// returns the hash. Nodes are content-addressed and immutable once stored:
// Put/Delete are path-copying (they write new nodes rather than mutate
// existing ones), so old versions simply become unreferenced garbage,
// reclaimed by the Go GC when the whole trie/StateDB goes out of scope (state
// is always rebuilt fresh via full block replay in this codebase -- see
// state/mpt.go).
func store(nodes map[core.Hash][]byte, n node) core.Hash {
	enc := n.encode()
	h := core.SumHash(enc)
	nodes[h] = enc
	return h
}
