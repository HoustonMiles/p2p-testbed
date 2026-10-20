# Status and results

## Done

| Part | State | Evidence |
|---|---|---|
| NAT routers: full cone, restricted, port-restricted, symmetric | done | `testbed/router/configure-router.sh` |
| Carrier-grade NAT (optional, per peer) | done | `cgn-x` |
| Link emulation: latency, jitter, loss, uplink/downlink caps | done | netem + IFB on the home routers |
| STUN server with RFC 5780 support | done | `testbed/stun/` (coturn, STUN-only) |
| Smoke test and NAT verification | done | `p2prunner smoke`, `p2prunner verify` (`p2prunner/checks.py`) |
| Experiment runner (matrix, state reset, resume, metadata) | done | `p2prunner/` |
| Trial types `nat_verify`, `ping`, `command` | done | `p2prunner/trials.py` |
| Analysis notebook (early experiments only) | done | `analysis/results.ipynb` |
| Raw UDP hole-punching baseline (STUN, rendezvous, punch, peer-to-peer RTT, throughput) | done, tests pass | `p2p/` (`punch`, `rendezvous`) |
| Rendezvous server in the testbed | done | `rendezvous` service, 198.18.0.101:7000 |
| Direct channel: Noise handshake (KK, IK or XX with static keys; `punch -genkey`), encrypted UDP transport with replay window | done, tests pass | `p2p/internal/noise/`, `p2p/internal/identity/` |
| Port-hopping: clock-based HKDF port schedule, early open + probing, drain-based close | done, tests pass | `p2p/internal/direct/` |
| `punch -direct`: handshake, port check, hopping, then ping + throughput through the hopper | done, tests pass | `p2p/internal/peer/`, `p2p/cmd/punch` |
| Clock offset/drift flags | done | `punch -clock-offset`, `-clock-drift-ppm` |
| Mesh step 1: the lower static key starts the handshake; the direct channel has no a/b roles | done, tests pass | `p2p/internal/peer/handshake.go` (`initiates`), `p2p/internal/direct/schedule.go` (sides) |
| Mesh step 2: rendezvous rooms (any number of members, joins and leaves), peer IDs on the wire, trusted key directories | done, tests pass | `p2p/internal/rendezvous/`, `p2p/internal/wire/`, `punch -name -room`, `-peer-key DIR` |
| Mesh step 3a: receiver index in the Noise header; one shared pool of hop sockets per peer, so two links on the same port share a socket | done, tests pass | `p2p/internal/noise/session.go`, `p2p/internal/direct/ports.go` |
| Mesh step 3b: link manager. One link per member (`-peers N`), punch, handshake, hop and pings on all links at once, throughput pair by pair in one global order, re-registration and PeerLeft, one result row per link | done, tests pass (3 and 4 peers on loopback, real binaries with 3) | `p2p/internal/peer/` (`link.go`, `punch.go`, `mesh_test.go`) |
| Mesh step 4a: testbed of up to 255 peers. Address plan (WAN 198.18.0.0/15, LAN 10.200.k.0/24, carrier 10.201.k.0/24), generated compose file (`p2prunner up/compose/down --peers N --cgn ...`), a CGN only for the peers behind one, per-peer NAT and access link settings, matrix keys `peers` and `nats`, checks for every running peer, ARP table check | done, tests pass; in a 4-CPU VM: `verify --all` as 8 peers at once, smoke with 8, full direct mesh of 8 / 16 / 32 peers (56 / 240 / 992 links as RFC 4787 predicts; 32 in 11 s); 64 peers CPU-bound there | `p2prunner/topology.py`, `p2prunner/compose.py`, `testbed/prepare-host.sh` |
| Mesh step 4b-1: per-link progress. Each link moves through punch, handshake, hop and pings on its own goroutine, so a dead link holds up only itself; new column `ready_ms` | done, tests pass; 8 mixed-NAT peers: all 30 hop-capable links ready within 0.5 s while the dead links punch for 10 s; 32 peers: 992/992 links hop-connected | `p2p/internal/peer/` (`link.go`, `mesh_test.go`) |
| Mesh step 4b-2: `mesh` trial type (every peer at once), `links.jsonl`, mesh-level columns (pairs usable, as predicted by RFC 4787, time to full mesh), `start_unix_ms`, `p2prunner mesh` report, `mesh_scale` experiment | done, tests pass; in a 4-CPU VM without netem (2 runs per configuration): all 1,240 link ends as RFC 4787 predicts; realistic meshes complete at 2 to 16 peers, time to full mesh about 0.2 / 0.6 / 1.3 s at 4 / 8 / 16 peers | `p2prunner/mesh.py`, `p2prunner/nat.py` |
| Mesh step 4c: peers join a running mesh (each link starts when its member is introduced); `stagger_ms` spaces the starts; join time per peer (`join_ms_*`); `[params]` keys as matrix keys; `mesh_join` experiment | done, tests pass; in a 4-CPU VM without netem (2 runs per configuration): every pair as predicted at every spacing; median join time at 16 peers 317 ms all at once, 169 ms at 500 ms spacing or more (8 peers: about 160 ms at any spacing) | `p2p/internal/peer/meet.go`, `p2prunner/mesh.py`, `p2prunner/experiment.py` |
| Result reports (standard library only) | done | `p2prunner hops`, `p2prunner compare` (`p2prunner/reports.py`) |
| Tests: Go unit + fuzz tests beside the code, Python `unittest` in `tests/` | done | [testing.md](testing.md) |
| **Result:** NAT matrix | 32/32 configurations verified, 96/96 trials | `results/nat_matrix/20261009-200208` |
| **Result:** path baseline | 180/180 trials; RTT within 0.4 ms of target, jitter matches netem theory | `results/path_baseline/20261009-175144` |
| **Result:** punch matrix (raw baseline) | 320/320 trials; 24/32 configurations punch, exactly as RFC 4787 predicts | `results/punch_matrix/20261012-124540` |
| **Result:** punch path (raw baseline) | 60/60 trials; RTT = profile RTT, link fully used | `results/punch_path/20261012-132335` |
| **Result:** Experiment A, hop success | 320/320 trials; hopping holds in all 18 configurations where both NATs keep ports, fails in the rest, as the port check predicts | `results/hop_matrix/20261012-104647` + `results/hop_matrix_oneway/20261012-121649` |
| **Result:** Experiment B, performance cost | 30/30 trials; no latency cost, -2.0% goodput (= Noise overhead), no loss from hopping | `results/direct_path/` (latest run, drain-based close) |

## Not done

| Part | What it is for |
|---|---|
| Full mesh, next: per-peer clock offsets in mesh trials, port clashes between a peer's links, a long-running mesh (members leaving and rejoining) | The paper's system is a mesh of pairwise direct links |
| Relay baseline: coturn as a TURN relay in the testbed (198.18.0.102), `punch -relay`, `relay_path` experiment | The comparison baseline, and the path for pairings that cannot hop |
| Handoff Manager | Falls back to the relay when `hop_possible` is false or hopping stops holding |
| Attack harness: flood containers (.200+) and the three attacker tiers (static, scripted reactive, AI-driven adaptive) | DoS experiments |
| Experiment C | Attack resilience, direct hopping vs relay (`hop_drain_cutoffs`, `hop_unrouted`, `hop_foreign`, `hop_auth_failed` are there for it) |
| Clock experiment files | Sweep `-clock-offset` / `-clock-drift-ppm` against the hop tolerance (min of `-hop-lead` and `-hop-min-overlap`); `[params]` keys can now be matrix keys, so one file can sweep a value given to every peer; a different offset per peer needs a mesh-trial parameter |
| Rerun `direct_path` | Only to record the new `hop_drain_cutoffs` column (expected 0); the numbers below are already final |

## Path baseline

Each peer to the STUN server, median of 30 trials:

| Profile | Expected RTT | Measured | Jitter (stdev) |
|---|---|---|---|
| local | 5 ms | 5.19 to 5.22 ms | 0.06 ms |
| coast | 35 ms | 35.08 to 35.39 ms | 4.02 to 4.13 ms |
| transatlantic | 75 ms | 74.80 to 75.18 ms | 6.23 to 6.42 ms |

The realistic and hardest pairings measure the same, so the NAT type itself
adds no measurable delay. The constant ~0.2 ms above target is container and
NAT processing overhead (the testbed's noise floor).

## Experiment A: where does port-hopping hold?

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
layers keep the source port (see the paper notes: threats to validity).

## Experiment B: what do Noise and hopping cost?

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
  each packet carries 25 more bytes (1228 -> 1253 on the wire). Since mesh
  step 3a the header also carries a 4-byte receiver index, so a rerun would
  show 29 bytes and about 2.3% (predicted, not yet measured).
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
drained instead (see [design.md](design.md#direct-channel-noise--port-hopping)).
The overlap must cover queueing delay, not just the RTT.

Runs: fixed overlap `results/direct_path/20261012-134117`, 400 ms overlap
`results/direct_path_overlap/20261012-140631`.

## Paper updates pending

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
  to reproduce (see [testbed.md](testbed.md#requirements)); the testbed's
  NATs (Linux MASQUERADE) keep source ports, so CGNAT does not break hopping
  here, but many real CGNs allocate port blocks or random ports, which would
  (behave like a symmetric NAT for hopping).
- Ping loss with vs without hopping: `hop_matrix` sends 100 pings per trial,
  `punch_matrix` 20, so compare mean loss (or rerun with equal counts); the
  medians (0 vs 0 to 1%) match the coast profile's own ~1% round-trip loss.
- Hopping needs endpoint-independent mapping that keeps the port on BOTH
  sides; with one symmetric side the hop path is one-way (Experiment A).
- Testbed scale: 32 peers ran a clean full mesh in a 4-CPU VM; at 64 the
  host was CPU-bound. Say which N were run on which hardware. The kernel's
  ARP table (shared by all containers) must be raised past about 30 peers.
- The lockstep finding: when all links of a peer move through the steps
  together, one link that never punches holds every other link back for the
  punch timeout, and the delay spreads to every member waiting on that peer
  (with 8 peers, good links failed at random). Per-link steps remove it: in
  the same 8-peer mesh every usable link was ready within 0.5 s. Time to
  full mesh is only meaningful with per-link steps.
- Mesh formation, worst against best case (sandbox numbers, to be rerun on
  the coast profile): every pair did as RFC 4787 predicts whether peers
  start together or one at a time. Starting together costs join time only
  from about 16 peers (317 ms median against 169 ms when 500 ms apart).
- Find and verify a citation on AI-driven attacks against moving-target
  defenses.
