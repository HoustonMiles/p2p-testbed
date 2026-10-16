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
| NAT routers: full cone, restricted, port-restricted, symmetric | done | `testbed/router/configure-router.sh` |
| Carrier-grade NAT (optional, per side) | done | `cgn-a`, `cgn-b` |
| Link emulation: latency, jitter, loss, uplink/downlink caps | done | netem + IFB on the home routers |
| STUN server with RFC 5780 support | done | `testbed/stun/` (coturn, STUN-only) |
| Smoke test and NAT verification | done | `p2prunner smoke`, `p2prunner verify` (`p2prunner/checks.py`) |
| Experiment runner (matrix, state reset, resume, metadata) | done | `p2prunner/` |
| Trial types `nat_verify`, `ping`, `command` | done | `p2prunner/trials.py` |
| Analysis notebook | done | `analysis/results.ipynb` |
| Raw UDP hole-punching baseline (STUN, rendezvous, punch, peer-to-peer RTT, throughput) | done, tests pass | `p2p/` (`punch`, `rendezvous`) |
| Rendezvous server in the testbed | done | `rendezvous` service, 203.0.113.101:7000 |
| Direct channel: Noise handshake (KK, IK or XX with static keys; `punch -genkey`), encrypted UDP transport with replay window | done, tests pass | `p2p/internal/noise/`, `p2p/internal/identity/` |
| Port-hopping: clock-based HKDF port schedule, early open + probing, drain-based close | done, tests pass | `p2p/internal/direct/` |
| `punch -direct`: handshake, port check, hopping, then ping + throughput through the hopper | done, tests pass | `p2p/internal/peer/`, `p2p/cmd/punch` |
| Clock offset/drift flags | done | `punch -clock-offset`, `-clock-drift-ppm` |
| Mesh step 1: the lower static key starts the handshake; the direct channel has no a/b roles | done, tests pass | `p2p/internal/peer/handshake.go` (`initiates`), `p2p/internal/direct/schedule.go` (sides) |
| Mesh step 2: rendezvous rooms (any number of members, joins and leaves), peer IDs on the wire, trusted key directories | done, tests pass | `p2p/internal/rendezvous/`, `p2p/internal/wire/`, `punch -name -room`, `-peer-key DIR` |
| Result reports (standard library only) | done | `p2prunner hops`, `p2prunner compare` (`p2prunner/reports.py`) |
| Tests: Go unit + fuzz tests beside the code, Python `unittest` in `tests/` | done | see Testing |
| **Result:** NAT matrix | 32/32 configurations verified, 96/96 trials | `results/nat_matrix/20261009-200208` |
| **Result:** path baseline | 180/180 trials; RTT within 0.4 ms of target, jitter matches netem theory | `results/path_baseline/20261009-175144` |
| **Result:** punch matrix (raw baseline) | 320/320 trials; 24/32 configurations punch, exactly as RFC 4787 predicts | `results/punch_matrix/20261012-124540` |
| **Result:** punch path (raw baseline) | 60/60 trials; RTT = profile RTT, link fully used | `results/punch_path/20261012-132335` |
| **Result:** Experiment A, hop success | 320/320 trials; hopping holds in all 18 configurations where both NATs keep ports, fails in the rest, as the port check predicts | `results/hop_matrix/20261012-104647` + `results/hop_matrix_oneway/20261012-121649` |
| **Result:** Experiment B, performance cost | 30/30 trials; no latency cost, -2.0% goodput (= Noise overhead), no loss from hopping | `results/direct_path/` (latest run, drain-based close) |

Path baseline (each peer to the STUN server, median of 30 trials):

| Profile | Expected RTT | Measured | Jitter (stdev) |
|---|---|---|---|
| local | 5 ms | 5.19 to 5.22 ms | 0.06 ms |
| coast | 35 ms | 35.08 to 35.39 ms | 4.02 to 4.13 ms |
| transatlantic | 75 ms | 74.80 to 75.18 ms | 6.23 to 6.42 ms |

The realistic and hardest pairings measure the same, so the NAT type itself
adds no measurable delay. The constant ~0.2 ms above target is container and
NAT processing overhead (the testbed's noise floor).

#### Experiment A: where does port-hopping hold?

`hop_matrix` (all 16 pairings x side B on the WAN or behind CGNAT, coast
profile, 1 s slots, `-hop-force`), with `hop_matrix_oneway` rerunning the 6
configurations that crashed before the hopping barrier waited for an ack.
Summarised with `p2prunner hops`.

| Group | Configurations | Result |
|---|---|---|
| Both NATs keep the local port (no symmetric NAT anywhere) | 18 (incl. CGNAT on B) | hopping connected and clean in 180/180 trials: no missed hop, ~5 hops per side, RTT 70.2 to 70.8 ms, ping loss 0 to 1% (the profile's own loss) |
| One side symmetric, punch still succeeds | 6 | one-way only: the cone side receives on every slot (median 9 hops), the symmetric side on none |
| Punch fails (symmetric vs port-restricted or symmetric; a CGN counts as port-restricted) | 8 | never reach hopping; these need the relay |

The port check made before hopping (`hop_possible`: STUN-mapped port equals
the local port on both sides) predicted the outcome in all 32
configurations, so the handoff to the relay can be decided before any hop
is tried. CGNAT does not break hopping here only because both Linux NAT
layers keep the source port (see Paper updates: threats to validity).

#### Experiment B: what do Noise and hopping cost?

`direct_path` against `punch_path`, realistic pairing (port-restricted on
both sides), 50 Mbit/s links, 60 Mbit/s offered per direction, 1 s slots.
Medians of 10 trials, both sides. Compared with `p2prunner compare`.

| Profile | RTT p50 raw / direct | rx_ip_mbit raw / direct | rx_mbit raw / direct | rx_loss raw / direct | Handshake |
|---|---|---|---|---|---|
| local | 10.68 / 10.67 ms | 49.43 / 49.44 | 48.31 / 47.35 | 0.143 / 0.159 | 8.8 ms |
| coast | 70.55 / 70.96 ms | 49.34 / 49.36 | 48.22 / 47.27 | 0.153 / 0.169 | 64.0 ms |
| transatlantic | 151.95 / 151.44 ms | 49.34 / 49.30 | 48.21 / 47.22 | 0.165 / 0.180 | 128.6 ms |

- No latency cost: RTT p50, p95 and jitter unchanged within 0.25 ms.
- The link is fully used (`rx_ip_mbit` unchanged). Goodput is 2.0% lower:
  each packet carries 25 more bytes (1228 -> 1253 on the wire).
- The extra loss is the same overhead, not hopping: the sender offers the
  same packets per second, 2% fewer fit through the cap, so
  1 - (1 - raw loss) x 0.980 predicts 0.160 / 0.170 / 0.182, measured
  0.159 / 0.169 / 0.180.
- The handshake costs about one RTT, once per session.

**Overlap finding.** The first `direct_path` run used a fixed 100 ms overlap
(the old port closed 100 ms after each boundary) and lost ~8% more
(`rx_loss` 0.22 to 0.26, `rx_ip_mbit` ~45): with the link saturated, the
1000-packet router queue holds ~200 ms of packets for the old port, plus the
one-way delay. A 400 ms overlap (`direct_path_overlap`) removed the loss,
which confirmed the cause; the hopper now closes the old port once it has
drained instead (see Direct channel). The overlap must cover queueing delay,
not just the RTT.

Runs: fixed overlap `results/direct_path/20261012-134117`, 400 ms overlap
`results/direct_path_overlap/20261012-140631`.

### Not done

| Part | What it is for |
|---|---|
| Full mesh, steps 3-5: a link manager with one handshake and hopper per neighbor, sharing sockets when two links pick the same port; an N-peer testbed with per-link results; a mesh experiment (time to full mesh, hop success per link as N grows, port clashes) | The paper's system is a mesh of pairwise direct links |
| Relay baseline: coturn as a TURN relay in the testbed (203.0.113.102), `punch -relay`, `relay_path` experiment | The comparison baseline, and the path for pairings that cannot hop |
| Handoff Manager | Falls back to the relay when `hop_possible` is false or hopping stops holding |
| Attack harness: flood containers (.200+) and the three attacker tiers (static, scripted reactive, AI-driven adaptive) | DoS experiments |
| Experiment C | Attack resilience, direct hopping vs relay (`hop_drain_cutoffs`, `hop_foreign`, `hop_auth_failed` are there for it) |
| Clock experiment files | Sweep `-clock-offset` / `-clock-drift-ppm` against the hop tolerance (min of `-hop-lead` and `-hop-min-overlap`); the runner cannot vary command parameters yet, so one file per value or a runner change |
| Rerun `direct_path` | Only to record the new `hop_drain_cutoffs` column (expected 0); the numbers above are already final |

### Paper updates pending

- Scope: the system is a full mesh of pairwise direct links, each with its
  own Noise session, hop key and schedule (the proposal still speaks of "the
  two authenticated peers"). In each pair the lower static key initiates.
  Peers meet in rendezvous rooms; "already in a network" means each peer
  holds the members' public keys (a key directory).
- Threat model: the rendezvous is unauthenticated, so anyone who knows a
  room number learns its members' addresses (not their traffic: the
  handshake still proves keys). Say so, or authenticate Register.
- Procedures: the rendezvous is UDP, not WebSocket (it must see each peer's
  public UDP mapping for the punch); it carries the static public keys. The
  handshake is Noise KK, IK or XX (`-noise`), matching the "peers already
  in a network" or "first contact" assumption.
- Methodology: describe the testbed as built (CGNAT, RFC 4787 names for the
  NAT types, half the RTT per direction, downlink cap before the NAT, RFC 5780
  verification run without packet loss).
- Define jitter: the profiles' ±7 / ±11 ms per-direction ranges, or the
  measured standard deviation.
- Report the NAT matrix, path baseline, punch matrix and Experiments A and B
  (tables above).
- The overlap finding: the old port must stay open for the queueing delay,
  not just the RTT; fixed 100 ms lost ~8% under load, drain-based close
  loses nothing.
- Threats to validity: shared kernel and CPU; host firewall settings needed
  to reproduce (see Requirements); the testbed's NATs (Linux MASQUERADE)
  keep source ports, so CGNAT does not break hopping here, but many real
  CGNs allocate port blocks or random ports, which would (behave like a
  symmetric NAT for hopping).
- Ping loss with vs without hopping: `hop_matrix` sends 100 pings per trial,
  `punch_matrix` 20, so compare mean loss (or rerun with equal counts); the
  medians (0 vs 0 to 1%) match the coast profile's own ~1% round-trip loss.
- Hopping needs endpoint-independent mapping that keeps the port on BOTH
  sides; with one symmetric side the hop path is one-way (Experiment A).
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
docker compose exec peer-a punch -genkey /keys/a   # once: static Noise keys for -direct,
docker compose exec peer-b punch -genkey /keys/b   #   kept in ./keys (not in git)
uv run python -m p2prunner smoke        # sanity check (seconds)
uv run python -m p2prunner verify       # RFC 5780 check of the running NAT types (~10 s)
uv run python -m p2prunner verify --all # check every NAT type and CGNAT (~1 min)
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
code most real home routers run. See `testbed/router/configure-router.sh`.
`p2prunner verify` confirms each type with the RFC 5780 tests.

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
uv run python -m p2prunner smoke                 # testbed wiring check
uv run python -m p2prunner verify [--all]        # NAT behaviour check
```

The smoke test and the NAT verification live in `p2prunner/checks.py`, and
use the same NAT expectations as the `nat_verify` trial, so the checks and
the experiments cannot disagree.

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
| `hop_matrix` | Experiment A: direct channel, handshake and hop success for all 16 pairings with and without CGNAT (~1 h) |
| `hop_matrix_oneway` | The 6 `hop_matrix` pairings with one symmetric side that can still punch (~20 min) |
| `direct_path` | Experiment B: `punch_path` through the direct channel, realistic pairing x 3 distances (~10 min) |
| `direct_path_overlap` | `direct_path` with `-hop-overlap 400ms`, the test that found the overlap problem (~10 min) |

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

## Code layout

`p2p/` is one Go module. Each folder is one part of the system, each
protocol lives in one folder, and every folder keeps its tests beside its
code (`_test.go`). Dependencies only point down this list.

```
p2p/
  cmd/punch/          the peer program: flags -> peer.Run -> one JSON line
  cmd/rendezvous/     the rendezvous program
  internal/
    peer/             one peer's run: socket.go (the one socket, dispatch), meet.go (STUN,
                      rendezvous), link.go (punch -> secure -> hop), handshake.go, hop.go,
                      barrier.go, measure.go, result.go; loopback_test.go runs whole trials
    rendezvous/       rooms: server.go, and client.go (a member's side)
    stun/             STUN messages
    direct/           the port-hopping channel: schedule, hopper, slots, probe, drain, receive
    noise/            Noise handshake, encrypted session, replay window
    identity/         static keys and trusted-key directories
    wire/             the packet format everything above shares
```

The protocol packages (`rendezvous`, `stun`, `noise`) never touch a socket:
they build packets and interpret replies, and `peer` carries them, because
every step before hopping must use the one socket whose NAT mapping the
rendezvous saw.

## Raw UDP hole-punching baseline

`p2p/` is a Go module with two programs, compiled into the peer image
(`testbed/host/Dockerfile` runs `go vet` and `go test` during the build):

- `rendezvous` runs as the `rendezvous` service and introduces the members
  of each room to each other: every Register is answered with one PeerInfo
  per other member (its address, ID and key), and each of them is told about
  the sender. A member not refreshed for `-ttl` (5 s) is dropped and the rest
  of the room is sent PeerLeft. It never relays peer traffic.
- `punch` runs on both peers at once, on one UDP socket: STUN, registration
  in the room, hole punching with the first member introduced (switching to
  its real address if its packets arrive from a different port), peer-to-peer
  pings, then throughput: the peer with the lower ID sends first, then the
  other. It prints one JSON line of results at the end.

Every packet carries the room and the sender's peer ID (the start of its
static key in direct mode, random otherwise); a peer accepts packets only
from the member it was introduced to.

No encryption and no port-hopping: these numbers are the floor that the Noise
channel and the relay are compared against.

**Run it through the runner** (the normal way; the trial seed is used as the
room, so both peers of a trial find each other):

```bash
uv run python -m p2prunner run experiments/punch_matrix.toml   # which pairings connect (~1 h)
uv run python -m p2prunner run experiments/punch_path.toml     # RTT + throughput (~20 min)
```

**Run it by hand** for a single try, in two terminals at the same time:

```bash
docker compose exec peer-a punch -name a -room 42
docker compose exec peer-b punch -name b -room 42
```

Use a new `-room` number for each try. `-name` labels the logs and results
and names the default key file. `punch -h` lists every flag
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

## Direct channel (Noise + port-hopping)

`punch -direct` runs the same steps and reports the same columns as the raw
baseline, but after the punch it moves to the encrypted, port-hopping
channel:

- `noise/handshake.go`: Noise handshake over the punched socket
  (X25519, ChaChaPoly, BLAKE2s) with each peer's static key (`identity/`);
  the pattern is chosen with `-noise` (below). Both sides resend until done,
  so lost or repeated handshake packets are harmless.
- `noise/session.go`, `noise/replay.go`: the UDP transport, with explicit
  nonces and a 2048-packet replay window. The hop key is derived from the
  session keys.
- `direct/schedule.go`: time is cut into slots of `-hop-interval`; each side's port
  for each slot is HKDF(hop key, side, slot), from the range 10000-32767
  (below Linux's ephemeral ports), where side is initiator or responder.
  Nothing about the schedule is sent.
- `direct/hopper.go` (with `slots.go`, `probe.go`, `drain.go`): opens the next slot's port `-hop-lead` early and probes the
  peer's next port until both directions work (the hole punch, repeated on
  every hop), sends on the current slot, and closes the old port once it has
  drained: after `-hop-min-overlap`, as soon as traffic has moved to the new
  port and the old one has been quiet for `-hop-drain-idle`, and at the
  latest after `-hop-overlap`.

**Handshake patterns and keys.** Each peer has a static keypair in `./keys`
(mounted at `/keys`; make them with `punch -genkey /keys/a` and `/keys/b`).
The rendezvous passes each peer's public key to the other with the
addresses, for the setups that take keys from it. The peer with the lower
public key initiates, so no peer needs to be told a role; with `-peer-key
none` the key the rendezvous announced sets the order, and the handshake
still authenticates whoever answers. With a directory of trusted keys, the
rendezvous's announcement picks which member's key the handshake must
prove; a key outside the directory is refused before the handshake.

| `-noise` | Peers know beforehand | Fits | `-peer-key` |
|---|---|---|---|
| `kk` (default) | each other's key | peers already in a network | a file, or a directory of the members' keys (default `/keys`) |
| `ik` | the initiator knows the responder's key | known network, WireGuard-style; 1 round trip | a file, or `rendezvous` |
| `xx` | nothing; keys travel in the handshake | first contact; 1.5 round trips | `rendezvous` (checked against it) or `none` (trust on first use) |

All three authenticate both peers and give forward secrecy. `-peer-key
rendezvous` trusts the rendezvous not to swap keys; a key file does not.
A peer that presents an unexpected key is rejected, and an impostor cannot
block the real initiator (the responder starts over). In the testbed both
peers can read both private keys in `./keys`; attacker containers must not
mount it.

Steps added after the punch: Noise handshake; a barrier carrying each side's
port check (does its NAT keep the local port); if both do (or `-hop-force`),
start the hopper and pass a barrier through it that needs an ack, proving the
first hop works both ways. A port check that fails, or a first hop that does
not connect, is a result (`hop_possible` / `hop_connected` = false), not an
error.

```bash
docker compose exec peer-a punch -name a -room 42 -direct -hop-interval 1s
docker compose exec peer-b punch -name b -room 42 -direct -hop-interval 1s
```

| Flag | Default | Meaning |
|---|---|---|
| `-direct` | off | use the direct channel |
| `-noise` | `kk` | handshake pattern: `kk`, `ik` or `xx` (see above) |
| `-key` | `/keys/<name>.key` | this peer's static key |
| `-peer-key` | `/keys` | the trusted peers' keys: a `.pub` file, a directory of them (our own is skipped), `rendezvous` or `none` |
| `-genkey BASE` | | write `BASE.key` and `BASE.pub`, then exit |
| `-hop-interval` | 1s | slot length |
| `-hop-lead` | interval/10 | open the next port this early |
| `-hop-min-overlap` | interval/10 | keep the old port at least this long (with `-hop-lead`, the clock tolerance) |
| `-hop-overlap` | interval/2 | close the old port at the latest after this long |
| `-hop-drain-idle` | 50ms | quiet time that counts as drained |
| `-hop-force` | off | hop even when a NAT changes ports, to measure the failure |
| `-clock-offset`, `-clock-drift-ppm` | 0 | skew this peer's hop clock |

Result columns added by mesh step 2: `name` and `room` (were `role` and
`session`), `id`, `peer_id`.

Extra result columns: `direct`, `noise`, `peer_key_from`, `initiator`, `handshake_ms`, `keeps_port`,
`peer_keeps_port`, `hop_possible`, `hopping`, `hop_connected`,
`hop_interval_ms`, `clock_offset_ms`, `clock_drift_ppm`, and the hopper's
counters, all prefixed `hop_`:

| Column | Meaning |
|---|---|
| `hop_hops_ok`, `hop_hops_missed` | slots on which something from the peer arrived on our port (the slot running at start is not counted) |
| `hop_sent`, `hop_received`, `hop_send_errors` | Data packets |
| `hop_probes_sent`, `hop_probes_recv` | pinhole probes |
| `hop_foreign`, `hop_auth_failed`, `hop_replayed` | dropped on arrival: wrong source IP (before any crypto), failed authentication, replay |
| `hop_bind_failed` | our port for a slot was taken |
| `hop_drain_ms_avg` | how long old ports stayed open after the boundary (mixes busy and idle hops) |
| `hop_drain_cutoffs` | old ports closed at `-hop-overlap` while Data was still arriving: the hops that can lose packets. Should be 0. |
| `hop_late_opens` | the sender found the current slot not yet open (housekeeping fell behind). Should be 0. |

`rx_ip_mbit` also counts the 25 bytes Noise adds per packet in direct mode.

## Testing

| What | Where | Run |
|---|---|---|
| Go unit tests | `_test.go` files beside the code (`p2p/`) | `cd p2p && go test -race ./...` |
| Go fuzz tests for everything that parses network input | `fuzz_test.go` in `wire`, `stun`, `noise` | `cd p2p && go test -fuzz=FuzzOpen -fuzztime=1m ./internal/noise` (one fuzz target per run) |
| Python tests for the runner, checks and reports (fake testbed, no Docker) | `tests/` | `python3 -m unittest` (from the repo root) |
| The real testbed | | `p2prunner smoke`, `p2prunner verify`, then a short experiment run |

`go test` without `-fuzz` runs each fuzz target on its saved inputs only
(`testdata/fuzz/`), so the fuzzers' past finds stay as regression tests. The
Docker build runs `go vet` and `go test` too, so an image never ships code
that fails its tests. On NixOS without Go installed:
`nix-shell -p go --run "cd p2p && go test ./..."`.

Fuzz targets: `wire.FuzzParse`, `wire.FuzzParseAddrs`, `stun.FuzzParse`,
`direct.FuzzOpen` (nothing forged authenticates), `direct.FuzzHandshake`,
`direct.FuzzReplayWindow` (the ring bitmap against a plain map).

## Analysis

```bash
uv run --group analysis jupyter lab analysis/results.ipynb
```

The notebook loads the newest run of each experiment: the NAT verification
table, the path-baseline summary and RTT chart, the hole-punching success
matrix, the punched-path RTT and throughput, and the columns of any other
`command` experiment.

Two reports need only Python's standard library (no numpy/pandas, so no
NixOS setup):

```bash
# Experiment A: hop success per pairing; later runs replace earlier ones per configuration
uv run python -m p2prunner hops results/hop_matrix/<run> results/hop_matrix_oneway/<run>

# any two runs of the same configurations, e.g. Experiment B: raw vs direct
uv run python -m p2prunner compare results/punch_path/<run> results/direct_path/<run>
```

Replace `<run>` with the run's folder name (e.g. `20261012-132335`); typed
literally, bash reads the angle brackets as redirection. Configurations are
matched across experiments by the hash part of `config_id`.

On NixOS, Python packages from PyPI look for `libstdc++` and other system
libraries in standard Linux locations. Add `programs.nix-ld.enable = true;` to
`configuration.nix`, rebuild, start a new login shell, and launch with:

```bash
LD_LIBRARY_PATH=$NIX_LD_LIBRARY_PATH uv run --group analysis jupyter lab analysis/results.ipynb
```
