package evm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

var (
	deployer  = common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

const (
	deployGas = uint64(3_000_000)
	callGas   = uint64(1_000_000)
)

// deployToken builds a fresh harness, funds the deployer, and deploys the
// standard ERC-20 fixture.
func deployToken(t *testing.T) (*Harness, common.Address) {
	t.Helper()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	h.Fund(deployer, uint256.NewInt(1_000_000_000_000_000_000)) // 1 ETH for gas headroom
	addr, gasUsed, err := h.Deploy(deployer, ERC20CreationCode(), deployGas)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if (addr == common.Address{}) {
		t.Fatal("Deploy returned zero contract address")
	}
	if gasUsed == 0 {
		t.Fatal("Deploy reported zero gas used")
	}
	if code := h.Code(addr); len(code) == 0 {
		t.Fatal("deployed contract has empty runtime code in state")
	}
	return h, addr
}

func balanceOf(t *testing.T, h *Harness, token, who common.Address) *big.Int {
	t.Helper()
	ret, _, err := h.Call(deployer, token, EncodeBalanceOf(who), callGas)
	if err != nil {
		t.Fatalf("balanceOf(%s): %v", who.Hex(), err)
	}
	return DecodeUint256(ret)
}

func TestDeployERC20(t *testing.T) {
	h, addr := deployToken(t)
	t.Logf("contract=%s runtimeCodeLen=%d", addr.Hex(), len(h.Code(addr)))
}

func TestInitialSupplyMintedToDeployer(t *testing.T) {
	h, token := deployToken(t)

	got := balanceOf(t, h, token, deployer)
	if got.Cmp(ERC20InitialSupply) != 0 {
		t.Fatalf("balanceOf(deployer) = %s, want initial supply %s", got, ERC20InitialSupply)
	}

	ret, _, err := h.Call(deployer, token, EncodeTotalSupply(), callGas)
	if err != nil {
		t.Fatalf("totalSupply: %v", err)
	}
	if ts := DecodeUint256(ret); ts.Cmp(ERC20InitialSupply) != 0 {
		t.Fatalf("totalSupply = %s, want %s", ts, ERC20InitialSupply)
	}
	t.Logf("initial supply / balanceOf(deployer) = %s", got)
}

func TestTransferUpdatesKeccakMappedStorage(t *testing.T) {
	h, token := deployToken(t)

	amount := big.NewInt(500)
	before := balanceOf(t, h, token, deployer)

	ret, gasUsed, err := h.Call(deployer, token, EncodeTransfer(recipient, amount), callGas)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if DecodeUint256(ret).Sign() == 0 {
		t.Fatal("transfer returned false")
	}
	if gasUsed == 0 {
		t.Fatal("transfer reported zero gas used")
	}

	afterDeployer := balanceOf(t, h, token, deployer)
	afterRecipient := balanceOf(t, h, token, recipient)

	wantDeployer := new(big.Int).Sub(before, amount)
	if afterDeployer.Cmp(wantDeployer) != 0 {
		t.Fatalf("deployer balance = %s, want %s", afterDeployer, wantDeployer)
	}
	if afterRecipient.Cmp(amount) != 0 {
		t.Fatalf("recipient balance = %s, want %s", afterRecipient, amount)
	}
	t.Logf("transfer ok: gasUsed=%d deployer %s->%s recipient 0->%s",
		gasUsed, before, afterDeployer, afterRecipient)
}

func TestTransferMoreThanBalanceReverts(t *testing.T) {
	h, token := deployToken(t)

	deployerBefore := balanceOf(t, h, token, deployer)
	recipientBefore := balanceOf(t, h, token, recipient)

	tooMuch := new(big.Int).Add(ERC20InitialSupply, big.NewInt(1))
	ret, _, err := h.Call(deployer, token, EncodeTransfer(recipient, tooMuch), callGas)

	// EVM revert semantics: either an error is returned, or (for a spec that
	// returns false) the boolean return is zero. Our fixture reverts.
	if err == nil && DecodeUint256(ret).Sign() != 0 {
		t.Fatal("over-balance transfer unexpectedly succeeded")
	}

	// Balances must be unchanged.
	if got := balanceOf(t, h, token, deployer); got.Cmp(deployerBefore) != 0 {
		t.Fatalf("deployer balance changed after reverted transfer: %s != %s", got, deployerBefore)
	}
	if got := balanceOf(t, h, token, recipient); got.Cmp(recipientBefore) != 0 {
		t.Fatalf("recipient balance changed after reverted transfer: %s != %s", got, recipientBefore)
	}
	t.Logf("over-balance transfer reverted (err=%v); balances unchanged", err)
}

// runSequence deploys and performs a transfer on a fresh state, returning the
// observable balances and the resulting MPT state root.
func runSequence(t *testing.T, amount *big.Int) (balDeployer, balRecipient *big.Int, root common.Hash) {
	t.Helper()
	h, token := deployToken(t)
	if _, _, err := h.Call(deployer, token, EncodeTransfer(recipient, amount), callGas); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	return balanceOf(t, h, token, deployer), balanceOf(t, h, token, recipient), h.StateRoot()
}

func TestDeterministicAcrossFreshStates(t *testing.T) {
	amount := big.NewInt(12345)
	d1, r1, root1 := runSequence(t, amount)
	d2, r2, root2 := runSequence(t, amount)

	if d1.Cmp(d2) != 0 || r1.Cmp(r2) != 0 {
		t.Fatalf("non-deterministic balances: run1(d=%s,r=%s) run2(d=%s,r=%s)", d1, r1, d2, r2)
	}
	if root1 != root2 {
		t.Fatalf("non-deterministic state root: %s != %s", root1.Hex(), root2.Hex())
	}
	t.Logf("deterministic: deployer=%s recipient=%s root=%s", d1, r1, root1.Hex())
}
