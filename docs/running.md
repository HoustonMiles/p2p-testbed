# Running experiments

`p2prunner` (Python, run with uv) runs an experiment file: it brings up each
configuration, verifies its NAT behaviour, runs the trials and saves results.

```bash
uv run python -m p2prunner plan experiments/path_baseline.toml   # configurations + time estimate
uv run python -m p2prunner run  experiments/path_baseline.toml   # run it
uv run python -m p2prunner run  experiments/path_baseline.toml --runs 2   # quick test
uv run python -m p2prunner run --resume results/path_baseline/<time>      # continue after Ctrl+C
uv run python -m p2prunner smoke                 # testbed wiring check
uv run python -m p2prunner verify [--all]        # NAT behaviour check
```

The smoke test and the NAT verification live in `p2prunner/checks.py`, and
use the same NAT expectations as the `nat_verify` trial, so the checks and
the experiments cannot disagree.

For an overnight run, start it inside `tmux` (or `nohup ... &`) so closing the
terminal doesn't stop it.

## Experiment files

Experiment files (`experiments/*.toml`) set the trial type, its `[params]`,
the runs per configuration and a `[matrix]`. Every combination of matrix
values is one configuration; tables in the matrix (`[[matrix.pairing]]`)
bundle several settings under one name. Matrix keys (see
[testbed.md](testbed.md#choosing-a-configuration) for their values):

- testbed-wide: `net_profile`, `uplink_mbit`, `downlink_mbit`,
  `udp_timeout`, `udp_timeout_stream`
- per peer, with the peer's name after the underscore: `nat_a`,
  `upstream_a`, `cgn_a`, and `net_profile_a`, `uplink_mbit_a`,
  `downlink_mbit_a` (these default to the testbed-wide value)
- `peers`: how many peers (2 to 255, default 2), and `nats`: a list of NAT
  types the home routers take round-robin (a, b, c, ... in turn), which a
  per-peer `nat_x` overrides:

  ```toml
  [matrix]
  peers = [2, 4, 8]
  nats = [["port_restricted", "full_cone", "symmetric"]]
  ```
- any key of the experiment's `[params]`, which each configuration then
  overrides for its trials (a column in the results, not a testbed
  setting):

  ```toml
  [params]
  stagger_ms = 0
  [matrix]
  stagger_ms = [0, 100, 500, 2000]
  ```

A peer gets a CGN only when the configuration puts it behind one, so large
testbeds stay small. Configurations of two peers keep the IDs they had
before `peers` existed, so older results still match.

| Experiment | What it measures |
|---|---|
| `nat_matrix` | RFC 5780 check of all 16 NAT pairings, with and without CGNAT (~15 min) |
| `path_baseline` | Access-path RTT, realistic and hardest pairings, 3 distances (~35 min) |
| `punch_matrix` | Raw UDP hole-punching success and peer-to-peer RTT, all 16 pairings with and without CGNAT (~1 h) |
| `punch_path` | Peer-to-peer RTT and throughput over the punched path, 2 pairings x 3 distances, 50 Mbit/s links (~20 min) |
| `hop_matrix` | Experiment A: direct channel, handshake and hop success for all 16 pairings with and without CGNAT (~1 h) |
| `hop_matrix_oneway` | The 6 `hop_matrix` pairings with one symmetric side that can still punch (~20 min) |
| `direct_path` | Experiment B: `punch_path` through the direct channel, realistic pairing x 3 distances (~10 min) |
| `direct_path_overlap` | `direct_path` with `-hop-overlap 400ms`, the test that found the overlap problem (~10 min) |
| `relay_matrix` | Every NAT pairing with and without CGNAT, through the TURN relay: does the relay connect all of them? (~50 min) |
| `relay_path` | Experiment B's third arm: `direct_path`'s configurations through the relay with Noise, no relay delay (~10 min) |
| `relay_detour` | `relay_path` on the coast profile with the relay 0, 10 and 40 ms out of the way (~10 min) |
| `mesh_scale` | Full mesh of direct links for 2, 4, 8 and 16 peers, all starting at once, realistic (all port-restricted) and mixed NATs (~30 min) |
| `mesh_join` | Peers joining a running mesh 0, 100, 500 and 2000 ms apart, 8 and 16 peers, realistic and mixed NATs: worst case against best case (~35 min) |
| `handoff_matrix` | Handoff mode for every NAT pairing with and without CGNAT: 18 configurations should hop, the other 14 go through the relay (~1 h) |
| `handoff_mesh` | Handoff mode for 4, 8 and 16 peers of mixed NAT types, all starting at once: every pair should come up, hopping or through the relay (~10 min) |

**Trial types:** `nat_verify` (RFC 5780 check), `ping` (RTT from both peers to
a WAN host), and `command`, which runs `cmd_a`/`cmd_b` on the peers. Each command
prints one JSON object on its last line, and its keys become result columns
(prefixed `a_`/`b_`). This is how the experiments run `punch`; the trial
seed is passed as its `-room`, so both peers of a trial find each other.
`punch` prints one line per link, so with two peers the last line is the
only one. The `command` trial runs on peers a and b only.

The `mesh` trial runs `punch` on every peer of the configuration in one
room (the trial seed), with the flags in `args`; in direct mode it first
makes keys for peers that have none. With `stagger_ms`, peer a starts
first, b `stagger_ms` later, and so on, each joining the mesh the earlier
ones are running; 0 (the default) starts everyone at the same moment. Its trial row has
mesh-level columns: `pairs` (N(N-1)/2), `pairs_usable` (hop-connected at
both ends in direct mode, punched in raw mode, hopping or through the relay
in handoff mode), `pairs_hop`, `pairs_punch`, `pairs_relay`, `pairs_none`,
`pairs_split` (handoff mode: one end hopping, the other on the relay),
`pairs_error`, `pairs_as_predicted` (the outcome RFC 4787
predicts for the pair's NAT behaviours, `p2prunner/nat.py`; in handoff mode
`hop` where it predicts hopping and `relay` everywhere else),
`time_to_usable_ms` (from the first peer's start until the last usable
pair was ready at both ends) and `time_to_full_mesh_ms` (the same, when
every pair is usable; with `stagger_ms` it includes the spacing itself;
with `-relay` in `args` every pair should come out `relay`),
and `join_ms_p50` / `join_ms_max` (from a peer's start until its usable
links with the members already running were ready at both ends). Each link
end's own row goes to `links.jsonl`.

**How trials are run:**
- Each configuration's trials are split into blocks (`block_size`), and the
  blocks of all configurations are shuffled with the experiment's `seed`. This
  spreads slow drift over a long run across configurations.
- Before every trial, all routers' NAT state (conntrack, ipset) is flushed, so
  no mapping from one trial can affect the next.
- Each time a configuration starts, its NAT behaviour is verified first. A
  configuration that fails twice is skipped and recorded as failed.

## Results

Each run goes to `results/<experiment>/<start time>/`:

| File | Contents |
|---|---|
| `trials.jsonl` / `trials.csv` | one row per trial: configuration + metrics |
| `configs.jsonl` | every configuration start, with its NAT verification |
| `meta.json` | date, git commit (and whether there were uncommitted changes), Docker/kernel versions, seed |
| `experiment.toml` | exact copy of the experiment that ran |
| `links.jsonl` | mesh trials: one row per link end, `punch`'s columns plus `peer`, `member`, the pair's `outcome` and `predicted`, and each end's expected `*_mapping` / `*_filtering`; join with trials.jsonl on `config_id` and `trial` |
| `raw/` | full details per trial (e.g. every RTT sample) |

`punch`'s result columns are listed in
[design.md](design.md#result-columns). When reading them:

- A pairing **succeeded** when `a_punched` and `b_punched` are both true.
  `ok` only means the tool ran: a failed punch is a valid result. (If only one
  side punched, that side exits with an error and the row has `ok = false`.)
- `punch_ms` is quantised to roughly the punch interval (50 ms) plus the RTT,
  because the first punches are dropped until the other side's NAT opens.
- `mapping_varies` (STUN and rendezvous saw different ports) measures
  endpoint-dependent mapping directly, and should agree with the NAT matrix.
- Peer-to-peer RTT crosses both routers, so it equals the full profile RTT
  (10 / 70 / 150 ms), not the half measured in the path baseline.
- Compare throughput with the cap using `rx_ip_mbit`. netem also counts the
  14-byte Ethernet header, so with 1200-byte payloads the maximum is about
  98.9% of the cap. In direct mode it also counts the 29 bytes Noise adds
  per packet (25 before mesh step 3a, which added the receiver index).

Expected punch success (cone and symmetric NATs, RFC 4787 behaviour): any
pairing with a full cone side connects; symmetric with address-restricted
connects via the peer's real address; symmetric with port-restricted or
symmetric does not, and those pairings need the relay.

## Reports

The reports need only Python's standard library (no numpy/pandas, so no
NixOS setup):

```bash
# Experiment A: hop success per pairing; later runs replace earlier ones per configuration
uv run python -m p2prunner hops results/hop_matrix/<run> results/hop_matrix_oneway/<run>

# any two runs of the same configurations, e.g. Experiment B: raw vs direct vs relayed
uv run python -m p2prunner compare results/punch_path/<run> results/direct_path/<run>
uv run python -m p2prunner compare results/direct_path/<run> results/relay_path/<run>

# configurations that differ only in some settings: --ignore lines them up
uv run python -m p2prunner compare results/direct_path/<run> results/relay_detour/<run> --ignore relay_delay_ms

# mesh trials: pairs usable, share as predicted, full meshes, time until ready
uv run python -m p2prunner mesh results/mesh_scale/<run>
```

`compare` counts a trial when its path was up: both sides punched (and
hop-connected in direct mode), or, relayed, both sides ready through the
relay.

Replace `<run>` with the run's folder name (e.g. `20261012-132335`); typed
literally, bash reads the angle brackets as redirection. Configurations are
matched across experiments by the hash part of `config_id`.

## Running `punch` by hand

For a single try, in two terminals at the same time:

```bash
docker compose exec peer-a punch -name a -room 42
docker compose exec peer-b punch -name b -room 42
```

The same through the direct channel:

```bash
docker compose exec peer-a punch -name a -room 42 -direct -hop-interval 1s
docker compose exec peer-b punch -name b -room 42 -direct -hop-interval 1s
```

Through the TURN relay instead of a hole punch (works for every NAT
pairing, including two symmetric NATs):

```bash
docker compose exec peer-a punch -name a -room 43 -relay -direct
docker compose exec peer-b punch -name b -room 43 -relay -direct
```

Handoff mode: hop if the pairing allows it, else the relay (each row's
`path` and `handoff_reason` say which and why):

```bash
docker compose exec peer-a punch -name a -room 44 -handoff -direct
docker compose exec peer-b punch -name b -room 44 -handoff -direct
```

Use a new `-room` number for each try. `-name` labels the logs and results
and names the default key file. With more members, give each the room size,
e.g. `-peers 3`; each prints one row per link. For more than two peers,
start a larger testbed first (`p2prunner up --peers N`, see
[testbed.md](testbed.md#peers)) and give each peer its key
(`punch -genkey /keys/<name>`). `punch -h` lists every flag; the direct
channel's are in [design.md](design.md#flags).

## Analysis notebook

The notebook covers the early experiments only: the NAT verification table,
the path-baseline summary and RTT chart, the hole-punching success matrix,
the punched-path RTT and throughput, and the columns of any other `command`
experiment. It loads the newest run of each.

```bash
uv run --group analysis jupyter lab analysis/results.ipynb
```

On NixOS, Python packages from PyPI look for `libstdc++` and other system
libraries in standard Linux locations. Add `programs.nix-ld.enable = true;` to
`configuration.nix`, rebuild, start a new login shell, and launch with:

```bash
LD_LIBRARY_PATH=$NIX_LD_LIBRARY_PATH uv run --group analysis jupyter lab analysis/results.ipynb
```
