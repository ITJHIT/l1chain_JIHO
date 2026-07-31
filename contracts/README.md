# contracts/

Solidity fixtures for `l1chain`'s EVM harness (`evm/`, M6): real
solc-compiled OpenZeppelin contracts, proving the harness runs genuine
production bytecode (event logs, a real `onlyOwner` revert path, a real
precompile call) rather than only the hand-assembled fixture
(`evm/erc20_fixture.go`) that predates this.

This is a self-contained Node/Hardhat project, independent of the Go module
at the repo root — `go test ./...` never needs solc installed; it only reads
the checked-in output of this project (`frozen/*.json`, mirrored into
`evm/oz_erc20_fixture.go`).

## Pinned versions

- solc `0.8.24` (`hardhat.config.js`)
- `@openzeppelin/contracts ^5.1.0`
- `hardhat ^2.22.0` / `@nomicfoundation/hardhat-toolbox ^5.0.0`

## Reproduce / update the fixture

```bash
npm ci
npm run compile   # hardhat compile -> artifacts/
npm run freeze    # scripts/freeze.js: artifacts/ -> frozen/*.json
```

CI's `contracts` job runs the same two commands on every push and fails the
build if `frozen/*.json` doesn't match exactly (`git diff --exit-code --
frozen/`) — this is drift detection, not a one-time generation: it proves
both that solc genuinely compiled this and that the checked-in fixture is
exactly reproducible, on every run.

If you update `contracts/*.sol` (or the pinned solc/OZ versions), run the two
commands above and commit the resulting `frozen/*.json` diff in the same
change — then run `go test ./evm/...` (specifically
`TestFrozenGoFixtureMatchesFrozenJSON`, in `evm/oz_fixture_freshness_test.go`)
to catch the other half of the provenance chain: the checked-in Go fixture
(`evm/oz_erc20_fixture.go`) must be regenerated to match too, by hand (its
provenance comment documents how).
