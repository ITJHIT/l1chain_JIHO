package evm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// ---------------------------------------------------------------------------
// Case 9: real OpenZeppelin ERC20 (contracts/L1Token.sol), deployed via real
// solc-compiled bytecode -- not the hand-assembled fixture. Direct proof
// this harness runs genuine production bytecode: a real ABI-encoded
// constructor arg, real selector dispatch, a real OZ Transfer event (the
// actual solc-derived signature hash, not a hand-rolled shape), the
// Harness.SetTxContext fix's non-zero TxHash, and a real OZ Ownable
// require/revert path.
// ---------------------------------------------------------------------------

func TestAdvEVM09OZERC20DeployMintTransferEmitsLogsWithRealTxHash(t *testing.T) {
	parsedABI, err := abi.JSON(strings.NewReader(L1TokenMetaData.ABI))
	if err != nil {
		t.Fatalf("parse L1Token ABI: %v", err)
	}

	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	initialSupply := big.NewInt(1_000_000)
	ctorArgs, err := parsedABI.Constructor.Inputs.Pack(initialSupply)
	if err != nil {
		t.Fatalf("pack constructor args: %v", err)
	}
	creationCode, err := hex.DecodeString(strings.TrimPrefix(L1TokenMetaData.Bin, "0x"))
	if err != nil {
		t.Fatalf("decode L1Token bytecode: %v", err)
	}
	creationCode = append(creationCode, ctorArgs...)

	tokenAddr, _, err := h.Deploy(deployer, creationCode, deployGas)
	if err != nil {
		t.Fatalf("deploy L1Token: %v", err)
	}
	if code := h.Code(tokenAddr); len(code) == 0 {
		t.Fatal("L1Token deployed with empty runtime code")
	}

	call := func(input []byte) []byte {
		t.Helper()
		ret, _, err := h.Call(deployer, tokenAddr, input, callGas)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		return ret
	}
	balOf := func(addr common.Address) *big.Int {
		t.Helper()
		input, err := parsedABI.Pack("balanceOf", addr)
		if err != nil {
			t.Fatalf("pack balanceOf: %v", err)
		}
		vals, err := parsedABI.Unpack("balanceOf", call(input))
		if err != nil {
			t.Fatalf("unpack balanceOf: %v", err)
		}
		return vals[0].(*big.Int)
	}

	if got := balOf(deployer); got.Cmp(initialSupply) != 0 {
		t.Fatalf("deployer balance after deploy = %s, want %s (initial supply)", got, initialSupply)
	}

	// Real selector-dispatched transfer(deployer -> recipient, 1000).
	transferInput, err := parsedABI.Pack("transfer", recipient, big.NewInt(1000))
	if err != nil {
		t.Fatalf("pack transfer: %v", err)
	}
	vals, err := parsedABI.Unpack("transfer", call(transferInput))
	if err != nil {
		t.Fatalf("unpack transfer result: %v", err)
	}
	if ok := vals[0].(bool); !ok {
		t.Fatal("transfer returned false")
	}

	if got := balOf(recipient); got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("recipient balance = %s, want 1000", got)
	}
	wantDeployerBal := new(big.Int).Sub(initialSupply, big.NewInt(1000))
	if got := balOf(deployer); got.Cmp(wantDeployerBal) != 0 {
		t.Fatalf("deployer balance after transfer = %s, want %s", got, wantDeployerBal)
	}

	// The real Transfer event: the exact solc-derived signature hash (not a
	// hand-rolled shape), and the Harness.SetTxContext fix's non-zero
	// TxHash -- this is the case that fix exists for, since the
	// hand-assembled fixture never emitted logs at all.
	transferSig := parsedABI.Events["Transfer"].ID
	found := false
	for _, lg := range h.Logs() {
		if lg.Address != tokenAddr || len(lg.Topics) == 0 || lg.Topics[0] != transferSig {
			continue
		}
		found = true
		if lg.TxHash == (common.Hash{}) {
			t.Fatal("Transfer log has a zero-value TxHash -- the SetTxContext fix regressed")
		}
	}
	if !found {
		t.Fatal("no real OZ Transfer event found in h.Logs() after a real transfer")
	}

	// Real OZ Ownable require/revert path through real solc-compiled
	// bytecode: mint() is onlyOwner, called here from a non-owner.
	mintInput, err := parsedABI.Pack("mint", recipient, big.NewInt(1))
	if err != nil {
		t.Fatalf("pack mint: %v", err)
	}
	if _, _, err := h.Call(recipient, tokenAddr, mintInput, callGas); err == nil {
		t.Fatal("mint() from a non-owner unexpectedly succeeded")
	}
}

// ---------------------------------------------------------------------------
// Case 10: precompile 0x1 (ecrecover) reached through real solc-compiled
// bytecode (contracts/Precompiles.sol) -- OZ's stock ERC20/Ownable never
// themselves reach a precompile, so this exists specifically to prove the
// real dispatch path (go-ethereum's already-wired PrecompiledContractsCancun,
// see evm/runtime.go's package doc) actually executes when called FROM real
// solc output, not just from Go test code calling the precompile directly.
// ---------------------------------------------------------------------------

func TestAdvEVM10PrecompileEcrecoverViaRealSolcBytecode(t *testing.T) {
	parsedABI, err := abi.JSON(strings.NewReader(PrecompileCallerMetaData.ABI))
	if err != nil {
		t.Fatalf("parse Precompiles ABI: %v", err)
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	wantAddr := crypto.PubkeyToAddress(key.PublicKey)

	hash := crypto.Keccak256Hash([]byte("hello precompile"))
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// crypto.Sign returns [R || S || V] with V in {0,1}; ecrecover (and
	// Solidity's ecrecover builtin) expects V in {27,28}.
	var rBytes, sBytes [32]byte
	copy(rBytes[:], sig[:32])
	copy(sBytes[:], sig[32:64])
	v := sig[64] + 27

	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000))

	creationCode, err := hex.DecodeString(strings.TrimPrefix(PrecompileCallerMetaData.Bin, "0x"))
	if err != nil {
		t.Fatalf("decode Precompiles bytecode: %v", err)
	}
	addr, _, err := h.Deploy(deployer, creationCode, deployGas)
	if err != nil {
		t.Fatalf("deploy PrecompileCaller: %v", err)
	}

	input, err := parsedABI.Pack("recover", [32]byte(hash), v, rBytes, sBytes)
	if err != nil {
		t.Fatalf("pack recover: %v", err)
	}
	ret, _, err := h.Call(deployer, addr, input, callGas)
	if err != nil {
		t.Fatalf("call recover: %v", err)
	}
	vals, err := parsedABI.Unpack("recover", ret)
	if err != nil {
		t.Fatalf("unpack recover result: %v", err)
	}
	gotAddr, ok := vals[0].(common.Address)
	if !ok {
		t.Fatalf("recover result type = %T, want common.Address", vals[0])
	}
	if gotAddr != wantAddr {
		t.Fatalf("ecrecover via real solc bytecode recovered %s, want %s (the real signer)", gotAddr.Hex(), wantAddr.Hex())
	}
}
