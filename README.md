# P2P Testbed

A Docker testbed for the port-hopping vs. relay experiments. Two peers sit
behind real Linux NAT routers (optionally behind carrier-grade NAT too), with
simulated latency, jitter, loss and link capacity between them.

```
[peer-a]--lan_a--[nat-a]--+------------------ wan ------------------+--[nat-b]--lan_b--[peer-b]
                          |                                         |
                          +--carrier_a--[cgn-a]--wan    wan--[cgn-b]--carrier_b--+
```

## Project status

Research question: can synchronized port-hopping on a Noise-encrypted direct
P2P channel give DoS resilience comparable to a TURN relay, while keeping
direct-path latency and throughput, against conventional and AI-driven
attackers across NAT types?

### Done

| Part | State | Evidence |
|---|---|---|
| NAT routers: full cone, restricted, port-restricted, symmetric | done | `router/configure-router.sh` |
| Carrier-grade NAT (optional, per side) | done | `cgn-a`, `cgn-b` |
| Link emulation: latency, jitter, loss, uplink/downlink caps | done | netem + IFB on the home routers |
| STUN server with RFC 5780 support | done | `stun/` (coturn, STUN-only) |
| Smoke test and NAT verification scripts | done | `scripts/smoke_test.sh`, `scripts/verify_nat.sh` |
| Experiment runner (matrix, state reset, resume, metadata) | done | `p2prunner/` |
| Trial types `nat_verify`, `ping`, `command` | done | `p2prunner/trials.py` |
| Analysis notebook | done | `analysis/results.ipynb` |
| Raw UDP hole-punching baseline (STUN, rendezvous, punch, peer-to-peer RTT, throughput) | done, tests pass | `baseline/` (`punch`, `rendezvous`) |
| Rendezvous server in the testbed | done | `rendezvous` service, 203.0.113.101:7000 |
| Hole-punching experiments | ready, not yet run on the real testbed | `experiments/punch_matrix.toml`, `experiments/punch_path.toml` |
| **Result:** NAT matrix | 32/32 configurations verified, 96/96 trials | `results/nat_matrix/20261009-200208` |
| **Result:** path baseline | 180/180 trials; RTT within 0.4 ms of target, jitter matches netem theory | `results/path_baseline/20261009-175144` |

Path baseline (each peer to the STUN server, median of 30 trials):

| Profile | Expected RTT | Measured | Jitter (stdev) |
|---|---|---|---|
| local | 5 ms | 5.19 to 5.22 ms | 0.06 ms |
| coast | 35 ms | 35.08 to 35.39 ms | 4.02 to 4.13 ms |
| transatlantic | 75 ms | 74.80 to 75.18 ms | 6.23 to 6.42 ms |

The realistic and hardest pairings measure the same, so the NAT type itself
adds no measurable delay. The constant ~0.2 ms above target is container and
NAT processing overhead (the testbed's noise floor).

### Not done

| Part | What it is for |
|---|---|
| Direct channel (Go): hole punching + Noise handshake | The direct path under test |
| Relay baseline: coturn as a TURN relay in the testbed (203.0.113.102) | The comparison baseline |
| Port-hopping on the direct channel | The project's novelty |
| Handoff Manager | Falls back to the relay when hopping can't hold |
| Attack harness: flood containers and the three attacker tiers (static, scripted reactive, AI-driven adaptive) | DoS experiments |
| Experiments A/B/C | A: traversal and hop success. B: performance cost. C: attack resilience |
| Clock offset/drift flags in the client | Tests the time-based hop schedule against unsynchronized clocks |

### Paper updates pending

- Methodology: describe the testbed as built (CGNAT, RFC 4787 names for the
  NAT types, half the RTT per direction, downlink cap before the NAT, RFC 5780
  verification run without packet loss).
- Define jitter: the profiles' ±7 / ±11 ms per-direction ranges, or the
  measured standard deviation.
- Report the NAT matrix and path baseline results.
- Threats to validity: shared kernel and CPU; host firewall settings needed
  to reproduce (see Requirements).
- Find and verify a citation on AI-driven attacks against moving-target
  defenses.

## Requirements

- Linux with Docker Engine and the Compose plugin
- Kernel modules: `sch_netem`, `ifb`, and the ipset modules (all loaded by `scripts/host_setup.sh`)
- Host reverse-path filtering must be off or "loose". Docker sends traffic
  between containers through the host firewall, and a strict check drops NAT
  replies on the LAN bridges. On NixOS:

  ```nix
  virtualisation.docker.enable = true;
  networking.firewall.checkReversePath = "loose";
  # Loose alone still drops unsolicited packets forwarded through two NATs
  # (cone NAT behind CGNAT), so skip the check on Docker bridges entirely:
  networking.firewall.extraCommands = ''
    iptables -t mangle -I nixos-fw-rpfilter -i br-+ -j RETURN
  '';
  boot.kernelModules = [ "sch_netem" "ifb" "ip_set" "ip_set_bitmap_port" "ip_set_hash_ipport" "xt_set" ];
  ```

  Tailscale also uses 100.64.0.0/10, the same range as the carrier networks.
  Stop `tailscaled` during experiments if CGNAT runs misbehave.

## Quick start

```bash
./scripts/host_setup.sh                 # once per boot (uses sudo)
docker compose up -d --build --wait     # build (incl. the Go tools) and start
./scripts/smoke_test.sh                 # sanity check (seconds)
./scripts/verify_nat.sh                 # RFC 5780 check of the running NAT types (~10 s)
./scripts/verify_nat.sh --all           # check every NAT type and CGNAT (~1 min)
docker compose down -t 1                # tear down
```

Choose a configuration with environment variables, inline or in a `.env`
file (copy `.env.example`):

```bash
NAT_A_TYPE=symmetric NAT_B_UPSTREAM=cgnat NET_PROFILE=coast \
  docker compose up -d --wait
```

Containers must be recreated (`down` then `up`) for new settings to apply.

## Settings

| Variable | Values (default first) | Meaning |
|---|---|---|
| `NAT_A_TYPE`, `NAT_B_TYPE` | `port_restricted`, `full_cone`, `restricted`, `symmetric` | Home router behaviour |
| `NAT_A_UPSTREAM`, `NAT_B_UPSTREAM` | `wan`, `cgnat` | Public IP on the home router, or behind the ISP's CGN |
| `CGN_A_TYPE`, `CGN_B_TYPE` | same as NAT types | CGN behaviour (only used with `cgnat`) |
| `NET_PROFILE` | `local`, `coast`, `transatlantic`, `none`, `custom` | Distance profile (10 / 70 / 150 ms RTT) |
| `UPLINK_MBIT`, `DOWNLINK_MBIT` | empty = no cap | Access link capacity. **Set `DOWNLINK_MBIT` for flood tests.** |
| `UDP_TIMEOUT`, `UDP_TIMEOUT_STREAM` | `30`, `120` | Seconds an idle UDP pinhole stays open |
| `DEBUG` | `0`, `1` | Print each router's iptables rules at startup |

## Addresses

| Host | Address(es) |
|---|---|
| peer-a / peer-b | 192.168.10.10 / 192.168.20.10 |
| nat-a | LAN 192.168.10.2, WAN 203.0.113.10, carrier 100.64.1.10 |
| nat-b | LAN 192.168.20.2, WAN 203.0.113.20, carrier 100.64.2.10 |
| cgn-a / cgn-b | carrier 100.64.1.2 / 100.64.2.2, WAN 203.0.113.11 / 203.0.113.21 |
| stun | 203.0.113.100 and alternate 203.0.113.103, UDP/TCP 3478 and 3479 |
| rendezvous | 203.0.113.101, UDP 7000 |
| reserved | .102 coturn relay, .200+ attackers |

A side's public IP is its home router's WAN IP, or its CGN's WAN IP when
`UPSTREAM=cgnat`.

## How the NAT types are built

All NAT behaviour comes from the Linux kernel (netfilter/conntrack), the same
code most real home routers run. See `router/configure-router.sh`.
`scripts/verify_nat.sh` confirms each type with the RFC 5780 tests.

| Type | Mapping | Inbound allowed from | How |
|---|---|---|---|
| `full_cone` | keeps port | anyone, but only on ports with an active mapping | MASQUERADE + ipset of mapped ports |
| `restricted` | keeps port | any port of an IP contacted from that mapping | MASQUERADE + ipset of (remote IP, port) pairs |
| `port_restricted` | keeps port | exact IP:port the peer contacted | plain MASQUERADE (Linux default) |
| `symmetric` | random port per destination | exact IP:port the peer contacted | MASQUERADE `--random-fully` |

## STUN server

`stun` runs coturn in STUN-only mode on two IP addresses and two ports, which
is what RFC 5780 NAT behaviour discovery needs. The peers include coturn's
client tools:

```bash
docker compose exec peer-a timeout 3 turnutils_stunclient 203.0.113.100      # your public address
docker compose exec peer-a turnutils_natdiscovery -m -f 203.0.113.100        # mapping + filtering tests
```

`turnutils_stunclient` keeps waiting for RFC 5780 replies that a filtering
NAT correctly blocks, so give it a `timeout`.

## Design notes

- **Half the RTT per side.** Each home router delays only its own outgoing
  traffic, so each applies half the profile's RTT (jitter / sqrt(2), loss / 2).
- **The downlink cap comes before the NAT.** Incoming traffic is rate-limited
  on an IFB device at the tc ingress hook, which runs before netfilter. A flood
  therefore fills the line even when the NAT then drops it, like a real access
  link. This is what lets the relay show its advantage against large floods.
- **CGNAT drops upstream.** Behind CGNAT, floods aimed at closed mappings are
  dropped at the CGN, before the home access link.
- **No bypassing the NAT.** Routers drop forwarded WAN traffic that isn't a
  reply or an intended cone forward, and remove Docker's default routes so
  nothing leaks to the host's real network.
- **Docker 28+:** the networks use `gateway_mode_ipv4: nat-unprotected`.
  Without it, Docker drops the routed traffic our NAT routers rely on.
- **No host masquerading:** the networks also set `enable_ip_masquerade: false`.
  Otherwise Docker's host rule rewrites the source of the first packet of a
  flow that a cone NAT forwards from the WAN onto a LAN (to 192.168.x.1), and
  the peer replies to an address that doesn't exist. This only affected
  unsolicited inbound traffic; the NAT matrix verdicts and ping baselines
  recorded before the fix are unaffected.
- **netem jitter reorders packets.** Keep this in mind when testing the
  replay window.

## Running experiments

`p2prunner` (Python, run with uv) runs an experiment file: it brings up each
configuration, verifies its NAT behaviour, runs the trials and saves results.

```bash
uv run python -m p2prunner plan experiments/path_baseline.toml   # configurations + time estimate
uv run python -m p2prunner run  experiments/path_baseline.toml   # run it
uv run python -m p2prunner run  experiments/path_baseline.toml --runs 2   # quick test
uv run python -m p2prunner run --resume results/path_baseline/<time>      # continue after Ctrl+C
```

For an overnight run, start it inside `tmux` (or `nohup ... &`) so closing the
terminal doesn't stop it.

**Experiment files** (`experiments/*.toml`) set the trial type, its
`[params]`, the runs per configuration and a `[matrix]`. Every combination of
matrix values is one configuration; tables in the matrix (`[[matrix.pairing]]`)
bundle several settings under one name. Matrix keys: `nat_a`, `nat_b`,
`upstream_a`, `upstream_b`, `cgn_a`, `cgn_b`, `net_profile`, `uplink_mbit`,
`downlink_mbit`, `udp_timeout`, `udp_timeout_stream`.

| Experiment | What it measures |
|---|---|
| `nat_matrix` | RFC 5780 check of all 16 NAT pairings, with and without CGNAT (~15 min) |
| `path_baseline` | Access-path RTT, realistic and hardest pairings, 3 distances (~35 min) |
| `punch_matrix` | Raw UDP hole-punching success and peer-to-peer RTT, all 16 pairings with and without CGNAT (~1 h) |
| `punch_path` | Peer-to-peer RTT and throughput over the punched path, 2 pairings x 3 distances, 50 Mbit/s links (~20 min) |
| `example_command` | Template for running your own programs on the peers |

**Trial types:** `nat_verify` (RFC 5780 check), `ping` (RTT from both peers to
a WAN host), and `command`, which runs `cmd_a`/`cmd_b` on the peers. Each command
prints one JSON object on its last line, and its keys become result columns
(prefixed `a_`/`b_`). This is the hook for the Go client.

**How trials are run:**
- Each configuration's trials are split into blocks (`block_size`), and the
  blocks of all configurations are shuffled with the experiment's `seed`. This
  spreads slow drift over a long run across configurations.
- Before every trial, all routers' NAT state (conntrack, ipset) is flushed, so
  no mapping from one trial can affect the next.
- Each time a configuration starts, its NAT behaviour is verified first. A
  configuration that fails twice is skipped and recorded as failed.

**Results** go to `results/<experiment>/<start time>/`:

| File | Contents |
|---|---|
| `trials.jsonl` / `trials.csv` | one row per trial: configuration + metrics |
| `configs.jsonl` | every configuration start, with its NAT verification |
| `meta.json` | date, git commit (and whether there were uncommitted changes), Docker/kernel versions, seed |
| `experiment.toml` | exact copy of the experiment that ran |
| `raw/` | full details per trial (e.g. every RTT sample) |

## Raw UDP hole-punching baseline

`baseline/` is a Go module with two programs, compiled into the peer image
(`host/Dockerfile` runs `go vet` and `go test` during the build):

- `rendezvous` runs as the `rendezvous` service and introduces the two peers
  of each session to each other. It never relays their traffic.
- `punch` runs on both peers at once, on one UDP socket: STUN, registration
  with the rendezvous, hole punching (switching to the peer's real address
  if its packets arrive from a different port), peer-to-peer pings, then
  throughput a->b and b->a. It prints one JSON line of results at the end.

No encryption and no port-hopping: these numbers are the floor that the Noise
channel and the relay are compared against.

**Run it through the runner** (the normal way; the trial seed is used as the
session ID, so both peers of a trial find each other):

```bash
uv run python -m p2prunner run experiments/punch_matrix.toml   # which pairings connect (~1 h)
uv run python -m p2prunner run experiments/punch_path.toml     # RTT + throughput (~20 min)
```

**Run it by hand** for a single try, in two terminals at the same time:

```bash
docker compose exec peer-a punch -role a -session 42
docker compose exec peer-b punch -role b -session 42
```

Use a new `-session` number for each try. `punch -h` lists every flag
(timeouts, ping count, rate, duration, packet size).

**Reading the results:**

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
  98.9% of the cap.

Expected success (cone and symmetric NATs, RFC 4787 behaviour): any pairing
with a full cone side connects; symmetric with address-restricted connects via
the peer's real address; symmetric with port-restricted or symmetric does not,
and those pairings need the relay.

## Analysis

```bash
uv run --group analysis jupyter lab analysis/results.ipynb
```

The notebook loads the newest run of each experiment: the NAT verification
table, the path-baseline summary and RTT chart, the hole-punching success
matrix, the punched-path RTT and throughput, and the columns of any other
`command` experiment.

On NixOS, Python packages from PyPI look for `libstdc++` and other system
libraries in standard Linux locations. Add `programs.nix-ld.enable = true;` to
`configuration.nix`, rebuild, start a new login shell, and launch with:

```bash
LD_LIBRARY_PATH=$NIX_LD_LIBRARY_PATH uv run --group analysis jupyter lab analysis/results.ipynb
```
