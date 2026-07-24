package evm

import (
	"encoding/hex"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ERC20CreationHex is the creation (init) bytecode of a standard ERC-20 token
// used as a fixed fixture for M4.
//
// SOURCE / COMPILER NOTE:
// solc is intentionally NOT installed in this environment (see M4 constraints),
// so — instead of compiling Solidity at test time — this is a byte-exact,
// hand-assembled EVM creation-bytecode fixture that faithfully implements the
// STANDARD ERC-20 storage layout and ABI that the Solidity compiler emits:
//
//   contract Token {                       // OpenZeppelin-style semantics
//       mapping(address => uint256) balances;   // storage slot 0
//       uint256 totalSupply;                     // storage slot 1
//       constructor() { _mint(msg.sender, 1_000_000e18); }
//       function balanceOf(address a) view returns (uint256);   // 0x70a08231
//       function transfer(address to, uint256 v) returns (bool); // 0xa9059cbb
//       function totalSupply() view returns (uint256);           // 0x18160ddd
//   }
//
// Critically, it derives each balance's storage slot exactly as Solidity does —
// slot(a) = keccak256(pad32(a) ++ pad32(0)) — via the real SHA3 opcode, so a
// transfer genuinely mutates keccak-mapped Merkle-Patricia-Trie storage under
// go-ethereum's EVM. The constructor mints an initial supply of 1,000,000 * 1e18
// to the deployer (msg.sender) and records it in totalSupply.
//
// The hex below was assembled with a two-pass label assembler and verified end
// to end against github.com/ethereum/go-ethereum/core/vm v1.17.4 (deploy +
// balanceOf + transfer + revert-on-overspend + deterministic state root) before
// being frozen here.
const ERC20CreationHex = "7f00000000000000000000000000000000000000000000d3c21bcecceda1000000806001553360005260006020526040600020556100996100436000396100996000f360003560e01c806370a082311461002c578063a9059cbb1461005257806318160ddd146100465760006000fd5b600435600052600060205260406000205460005260206000f35b60015460005260206000f35b336000526000602052604060002080546024358082106100935790039055600435600052600060205260406000208054602435019055600160005260206000f35b60006000fd"

// ERC20InitialSupply is the amount minted to the deployer by the constructor:
// 1,000,000 * 10^18.
var ERC20InitialSupply = new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// 4-byte function selectors (keccak256(signature)[:4]).
var (
	SelBalanceOf   = [4]byte{0x70, 0xa0, 0x82, 0x31} // balanceOf(address)
	SelTransfer    = [4]byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)
	SelTotalSupply = [4]byte{0x18, 0x16, 0x0d, 0xdd} // totalSupply()
)

// ERC20CreationCode returns the decoded creation bytecode fixture.
func ERC20CreationCode() []byte {
	b, err := hex.DecodeString(ERC20CreationHex)
	if err != nil {
		// The constant is a frozen, tested literal; a decode error is a
		// programming bug, not a runtime condition.
		panic("evm: invalid ERC20CreationHex fixture: " + err.Error())
	}
	return b
}

// padAddress left-pads a 20-byte address into a 32-byte ABI word.
func padAddress(a common.Address) []byte {
	out := make([]byte, 32)
	copy(out[12:], a.Bytes())
	return out
}

// padUint256 left-pads a non-negative integer into a 32-byte ABI word.
func padUint256(v *big.Int) []byte {
	out := make([]byte, 32)
	v.FillBytes(out)
	return out
}

// EncodeBalanceOf builds calldata for balanceOf(address).
func EncodeBalanceOf(addr common.Address) []byte {
	return append(SelBalanceOf[:], padAddress(addr)...)
}

// EncodeTransfer builds calldata for transfer(address,uint256).
func EncodeTransfer(to common.Address, amount *big.Int) []byte {
	out := append([]byte{}, SelTransfer[:]...)
	out = append(out, padAddress(to)...)
	out = append(out, padUint256(amount)...)
	return out
}

// EncodeTotalSupply builds calldata for totalSupply().
func EncodeTotalSupply() []byte {
	return append([]byte{}, SelTotalSupply[:]...)
}

// DecodeUint256 decodes a 32-byte-padded uint256 return value. Shorter returns
// are treated as big-endian; empty returns decode to zero.
func DecodeUint256(ret []byte) *big.Int {
	return new(big.Int).SetBytes(ret)
}
