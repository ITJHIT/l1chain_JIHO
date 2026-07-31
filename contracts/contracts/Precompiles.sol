// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

/// @notice Exercises EVM precompile 0x1 (ecrecover) through real
/// solc-compiled bytecode. OpenZeppelin's stock ERC20/Ownable (see
/// L1Token.sol) never themselves reach a precompile, so this small,
/// purpose-built contract exists specifically to prove l1chain's EVM harness
/// dispatches a real solc-compiled CALL to a precompile address correctly --
/// separate from the go-ethereum-internal precompile wiring already proven
/// unmodified (see evm/runtime.go).
contract PrecompileCaller {
    function recover(bytes32 hash, uint8 v, bytes32 r, bytes32 s) external pure returns (address) {
        return ecrecover(hash, v, r, s);
    }
}
