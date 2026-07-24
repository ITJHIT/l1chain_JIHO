package consensus

import (
	"testing"

	"l1chain/core"
)

func TestMineMeetsTarget(t *testing.T) {
	h := &core.Header{Height: 1, Difficulty: 8} // 8 leading zero bits: cheap
	hash, found := Mine(h, 1_000_000)
	if !found {
		t.Fatal("failed to mine a low-difficulty block within iteration cap")
	}
	if !MeetsTarget(hash, h.Difficulty) {
		t.Fatalf("mined hash %s does not meet difficulty %d", hash.Hex(), h.Difficulty)
	}
	if h.Hash() != hash {
		t.Fatal("winning nonce not persisted into the header")
	}
}

func TestMeetsTargetRejectsInsufficient(t *testing.T) {
	// A hash with a high first byte cannot have many leading zero bits.
	var h core.Hash
	h[0] = 0xFF
	if MeetsTarget(h, 1) {
		t.Fatal("hash starting with 0xFF must not meet difficulty >= 1")
	}
	if !MeetsTarget(h, 0) {
		t.Fatal("difficulty 0 must always be met")
	}
}

func TestNextDifficultyAdjusts(t *testing.T) {
	expected := int64(RetargetInterval * TargetIntervalSeconds)
	if got := NextDifficulty(10, expected/4, expected); got != 11 {
		t.Fatalf("too-fast blocks should harden difficulty to 11, got %d", got)
	}
	if got := NextDifficulty(10, expected*4, expected); got != 9 {
		t.Fatalf("too-slow blocks should ease difficulty to 9, got %d", got)
	}
	if got := NextDifficulty(10, expected, expected); got != 10 {
		t.Fatalf("on-target blocks should hold difficulty at 10, got %d", got)
	}
	if got := NextDifficulty(1, expected*4, expected); got != 1 {
		t.Fatalf("difficulty must not drop below 1, got %d", got)
	}
}
