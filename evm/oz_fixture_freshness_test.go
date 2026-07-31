package evm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestFrozenGoFixtureMatchesFrozenJSON closes the other half of the M6
// provenance chain: the contracts CI job (contracts/README.md) already
// proves real solc compiled contracts/*.sol into contracts/frozen/*.json;
// this proves the checked-in Go constants in oz_erc20_fixture.go match that
// JSON exactly -- so go test never needs solc installed at all (it only
// reads a checked-in file), and the two can never silently drift apart.
type frozenFixture struct {
	ABI              json.RawMessage `json:"abi"`
	Bytecode         string          `json:"bytecode"`
	DeployedBytecode string          `json:"deployedBytecode"`
}

func loadFrozen(t *testing.T, name string) frozenFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "contracts", "frozen", name))
	if err != nil {
		t.Fatalf("read contracts/frozen/%s: %v", name, err)
	}
	var f frozenFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse contracts/frozen/%s: %v", name, err)
	}
	return f
}

// assertSameABI compares parsed JSON values, not raw strings: solc's own ABI
// JSON output happens to already be alphabetically-keyed, but relying on
// that (or on Go/JS producing byte-identical re-serializations) would be
// fragile -- semantic equality is the actual property that matters here.
func assertSameABI(t *testing.T, goABI, jsonABI string) {
	t.Helper()
	var a, b interface{}
	if err := json.Unmarshal([]byte(goABI), &a); err != nil {
		t.Fatalf("parse Go-embedded ABI: %v", err)
	}
	if err := json.Unmarshal([]byte(jsonABI), &b); err != nil {
		t.Fatalf("parse frozen JSON ABI: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("ABI in evm/oz_erc20_fixture.go does not match contracts/frozen JSON -- regenerate the Go fixture (see contracts/README.md)")
	}
}

func TestFrozenGoFixtureMatchesFrozenJSON(t *testing.T) {
	l1 := loadFrozen(t, "L1Token.json")
	assertSameABI(t, L1TokenMetaData.ABI, string(l1.ABI))
	if L1TokenMetaData.Bin != l1.Bytecode {
		t.Fatalf("L1TokenMetaData.Bin does not match contracts/frozen/L1Token.json's bytecode -- regenerate the Go fixture")
	}
	if L1TokenMetaData.DeployedBin != l1.DeployedBytecode {
		t.Fatalf("L1TokenMetaData.DeployedBin does not match contracts/frozen/L1Token.json's deployedBytecode -- regenerate the Go fixture")
	}

	pc := loadFrozen(t, "Precompiles.json")
	assertSameABI(t, PrecompileCallerMetaData.ABI, string(pc.ABI))
	if PrecompileCallerMetaData.Bin != pc.Bytecode {
		t.Fatalf("PrecompileCallerMetaData.Bin does not match contracts/frozen/Precompiles.json's bytecode -- regenerate the Go fixture")
	}
}
