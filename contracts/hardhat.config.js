require("@nomicfoundation/hardhat-toolbox");

// Pinned, documented: solc 0.8.24, matching a version already proven to
// compile real OpenZeppelin v5 contracts elsewhere on this machine.
/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    version: "0.8.24",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200,
      },
    },
  },
};
