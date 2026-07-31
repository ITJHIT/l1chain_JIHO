// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";

/// @notice A minimal real-solc-compiled ERC20 for l1chain's EVM harness (M6):
/// proof that the harness runs genuine OpenZeppelin bytecode, not just the
/// hand-assembled fixture (evm/erc20_fixture.go) that predates this. Minting
/// beyond the initial supply is owner-gated so there is a real require/revert
/// path to exercise, and every transfer/mint emits OZ's real Transfer event
/// (event signature keccak256("Transfer(address,address,uint256)")), unlike
/// the hand-assembled fixture which never emits logs at all. Authored fresh
/// for this repo -- see contracts/README.md for provenance, not copied from
/// any other project.
contract L1Token is ERC20, Ownable {
    constructor(uint256 initialSupply) ERC20("L1Token", "L1T") Ownable(msg.sender) {
        _mint(msg.sender, initialSupply);
    }

    /// @notice Mints additional supply. Reverts (OwnableUnauthorizedAccount)
    /// if called by anyone but the owner -- the require/revert path this
    /// fixture exists to exercise through real solc-compiled bytecode.
    function mint(address to, uint256 amount) external onlyOwner {
        _mint(to, amount);
    }
}
