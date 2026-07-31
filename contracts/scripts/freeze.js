// freeze.js extracts {abi, bytecode, deployedBytecode} from hardhat's
// compiled artifacts and writes a checked-in, provenance-stamped copy to
// contracts/frozen/*.json. Run locally (`npm run freeze`, after
// `npm run compile`) when authoring/updating a fixture; CI's `contracts` job
// re-runs this same script and diffs the result against what's checked in
// here, so this script itself must be deterministic given the same compiled
// artifacts -- no timestamps, no non-deterministic field ordering beyond
// JSON.stringify's own (stable) key order.
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const ROOT = path.join(__dirname, "..");
const ozPkg = require(path.join(ROOT, "node_modules", "@openzeppelin", "contracts", "package.json"));

// Hardcoded rather than required from hardhat.config.js: requiring that file
// directly (as a plain Node module, outside Hardhat's own CLI bootstrap)
// throws HH5 "HardhatContext is not created" -- its top-level
// require("@nomicfoundation/hardhat-toolbox") needs Hardhat's own runtime
// context, which only exists when Hardhat's CLI itself is the entry point.
// This constant must be kept in sync with hardhat.config.js's
// solidity.version by hand -- both are one line, reviewed together.
const solcVersion = "0.8.24";

// { frozen file name -> [artifact path segments under artifacts/contracts, contract name, source file] }
const TARGETS = [
  {
    out: "L1Token.json",
    artifact: ["contracts", "L1Token.sol", "L1Token.json"],
    source: "L1Token.sol",
  },
  {
    out: "Precompiles.json",
    artifact: ["contracts", "Precompiles.sol", "PrecompileCaller.json"],
    source: "Precompiles.sol",
  },
];

function sha256(filePath) {
  const data = fs.readFileSync(filePath);
  return "sha256:" + crypto.createHash("sha256").update(data).digest("hex");
}

for (const t of TARGETS) {
  const artifactPath = path.join(ROOT, "artifacts", ...t.artifact);
  const artifact = JSON.parse(fs.readFileSync(artifactPath, "utf8"));
  const sourcePath = path.join(ROOT, "contracts", t.source);

  const frozen = {
    contractName: artifact.contractName,
    solcVersion,
    openzeppelinVersion: ozPkg.version,
    sourceHash: sha256(sourcePath),
    abi: artifact.abi,
    bytecode: artifact.bytecode,
    deployedBytecode: artifact.deployedBytecode,
  };

  const outPath = path.join(ROOT, "frozen", t.out);
  fs.writeFileSync(outPath, JSON.stringify(frozen, null, 2) + "\n");
  console.log("froze", t.out, "<-", artifactPath);
}
