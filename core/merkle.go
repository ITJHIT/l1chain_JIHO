package core

// MerkleRoot computes a binary merkle root over the ordered leaf hashes.
// An empty leaf set yields the zero hash. Odd levels duplicate the last node
// (Bitcoin-style) so every internal node has two children.
func MerkleRoot(leaves []Hash) Hash {
	if len(leaves) == 0 {
		return Hash{}
	}
	level := make([]Hash, len(leaves))
	copy(level, leaves)

	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([]Hash, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			next = append(next, HashConcat(level[i][:], level[i+1][:]))
		}
		level = next
	}
	return level[0]
}
