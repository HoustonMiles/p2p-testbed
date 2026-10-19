# Testing

| What | Where | Run |
|---|---|---|
| Go unit tests | `_test.go` files beside the code (`p2p/`) | `cd p2p && go test -race ./...` |
| Go fuzz tests for everything that parses network input | `fuzz_test.go` in `wire`, `stun`, `noise` | `cd p2p && go test -fuzz=FuzzOpen -fuzztime=1m ./internal/noise` (one fuzz target per run) |
| Python tests for the runner, checks and reports (fake testbed, no Docker) | `tests/` | `python3 -m unittest` (from the repo root) |
| The real testbed | | `p2prunner smoke`, `p2prunner verify`, then a short experiment run |

On NixOS without Go installed:
`nix-shell -p go --run "cd p2p && go test ./..."` (add `gcc` to the
packages for `-race`).

`go test` without `-fuzz` runs each fuzz target on its saved inputs only
(`testdata/fuzz/`), so the fuzzers' past finds stay as regression tests. The
Docker build runs `go vet` and `go test` too, so an image never ships code
that fails its tests.

Fuzz targets:

| Target | Checks |
|---|---|
| `wire.FuzzParse` | every accepted packet re-encodes to the same bytes |
| `wire.FuzzParseIntro` | every accepted PeerInfo body re-encodes to its prefix |
| `stun.FuzzParse` | never panics, and accepts only a reply to our own transaction |
| `noise.FuzzOpen` | nothing forged authenticates |
| `noise.FuzzHandshake` | garbage handshake messages are rejected and never complete a handshake |
| `noise.FuzzReplayWindow` | the ring bitmap agrees with a plain map |
