package consensus

import "testing"

func TestModeZeroValueIsPoW(t *testing.T) {
	var m Mode
	if m != PoW {
		t.Fatal("Mode's zero value must be PoW, so any Chain that never calls SetConsensusMode behaves exactly as it always has")
	}
}

func TestModeString(t *testing.T) {
	if got := PoW.String(); got != "pow" {
		t.Fatalf("PoW.String() = %q, want \"pow\"", got)
	}
	if got := PoS.String(); got != "pos" {
		t.Fatalf("PoS.String() = %q, want \"pos\"", got)
	}
	if got := Mode(99).String(); got != "unknown" {
		t.Fatalf("Mode(99).String() = %q, want \"unknown\"", got)
	}
}
