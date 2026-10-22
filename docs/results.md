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
| Relay step 5a: TURN relay in the testbed (coturn at 198.18.0.102, long-term credentials, optional `RELAY_DELAY_MS`); Go TURN client (allocate, permissions, channels, ChannelData; no new dependencies) on a STUN message layer with MESSAGE-INTEGRITY; relay in `smoke`; NAT checks retry a peer that got no reply | done, tests pass; MESSAGE-INTEGRITY checked against the RFC 5769 sample; relay-to-relay through coturn from the host and from behind a port-restricted NAT | `p2p/internal/turn/`, `p2p/internal/stun/message.go`, `testbed/stun/` |
| Relay step 5b: `punch -relay` (every link through the TURN relay, with Noise sealing everything after the handshake under `-direct`); the rendezvous carries relayed addresses and introduces relayed members by them alone; `relay` outcome in mesh trials; `smoke`/`verify` refuse a mix of two stacks; `run` says which configuration is left running | done, tests pass (fake TURN relay in the loopback tests; the relay sees no readable Data under `-direct`, mutation-checked); real coturn: two symmetric NATs, which cannot punch, connected with 20/20 pings and 20 Mbit/s; 8 peers of every NAT type: 28/28 pairs through the relay against 15/28 hop-connected | `p2p/internal/peer/relay.go`, `p2p/internal/wire`, `p2p/internal/rendezvous` |
| Relay step 5c: `relay_matrix` (every NAT pairing through the relay; a 2-peer mesh trial), `relay_path` (direct_path's configurations through the relay: Experiment B's third arm), `relay_detour` (relay 0 / 10 / 40 ms away); `compare` counts relayed trials and lines up configurations that differ in one setting (`--ignore`) | done, tests pass; `relay_matrix` once per configuration in a VM without netem: 32/32 pairings connected through the relay, including the 8 that cannot punch | `experiments/relay_*.toml`, `p2prunner/reports.py` |
| Handoff step 6a: `punch -handoff -direct` (each link binds its relay channel, punches, runs its handshake through the relay, then hops if both sides punched and both NATs keep ports, else stays on the relay); `path` and `handoff_reason` columns; mesh trials count hop and relay pairs as usable and `split` pairs; `handoff_matrix`, `handoff_mesh`. Also fixed: a late 401 to a resent Allocate no longer reads as wrong TURN credentials | done, tests pass (loopback handoff tests, mutation-checked); in a VM without netem: `handoff_matrix` twice per configuration, 64/64 connected on the predicted path (36 hop, 28 relay: 16 punch, 12 ports); 8 mixed peers, 3 trials: 28/28 pairs each (15 hop, 13 relay), no split pairs. Pairs that cannot punch are ready only after `-punch-timeout` (5 s here). A first coast run failed about half the trials of every hop-capable pairing and every mixed mesh: a side that had started hopping answered the member's relayed setup through its hopper, which the member did not have yet. Fixed (answers go back the way they came); the rerun below is clean | `p2p/internal/peer/handoff.go`, `p2p/internal/turn`, `experiments/handoff_*.toml` |
| Result reports (standard library only) | done | `p2prunner hops`, `p2prunner compare` (`p2prunner/reports.py`) |
| Tests: Go unit + fuzz tests beside the code, Python `unittest` in `tests/` | done | [testing.md](testing.md) |
| **Result:** NAT matrix | 32/32 configurations verified, 96/96 trials | `results/nat_matrix/20261009-200208` |
| **Result:** path baseline | 180/180 trials; RTT within 0.4 ms of target, jitter matches netem theory | `results/path_baseline/20261009-175144` |
| **Result:** punch matrix (raw baseline) | 320/320 trials; 24/32 configurations punch, exactly as RFC 4787 predicts | `results/punch_matrix/20261012-124540` |
| **Result:** punch path (raw baseline) | 60/60 trials; RTT = profile RTT, link fully used | `results/punch_path/20261012-132335` |
| **Result:** Experiment A, hop success | 320/320 trials; hopping holds in all 18 configurations where both NATs keep ports, fails in the rest, as the port check predicts | `results/hop_matrix/20261012-104647` + `results/hop_matrix_oneway/20261012-121649` |
| **Result:** mesh join | 80/80 trials; every pair as RFC 4787 predicts at every size and spacing; join time about 425 ms in every configuration (about six coast round trips), so neither load nor start pattern matters up to 16 peers | `results/mesh_join/20261020-111009` |
| Handoff step 6b: a hopping link moves to the relay while it runs, after `-pivot-after` missed hops in a row (default 3), or at once when the member's sealed traffic arrives through the relay; hopper reports each hop (`direct.Config.OnHop`); columns `pivoted`, `pivot_ms`, `pivot_reason`; mesh trials can break peers' direct paths mid-run (`break_at_s`, `break_peers`; `p2prunner/breaks.py`) and report `pairs_to_pivot`, `pairs_pivoted`, `pivot_ms_*`; `handoff_break`, `handoff_break_mesh`. Also fixed: hopping links in handoff mode no longer count ChannelData's 4 bytes in `rx_ip_mbit` | done, tests pass (loopback: a's hoppers silenced mid-pings, b moves on missed hops and a follows within ~40 ms on b's relayed pings; mutation-checked); in a VM without netem, 1 s slots, 3 trials each: the link reached the relay 1.9 to 2.4 s after the break after 1 missed hop, 3.8 to 4.1 s after 3, losing 20 pings per second of it; 8 mixed peers: exactly a's 5 hopping links moved, the other 23 pairs did not | `p2p/internal/peer/handoff.go`, `p2p/internal/direct`, `p2prunner/breaks.py`, `experiments/handoff_break*.toml` |
| **Result:** handoff break (step 6b) | 70/70 trials; every hopping link with a broken end moved to the relay, after 1.6 to 2.4 s with `-pivot-after 1` and 3.7 to 4.6 s with 3 (1 s slots), at every distance; the pings lost match the outage (about 20 per second). The mesh run also found single link ends moving after their member had left (a trial artifact, fixed; see below); rerun with the fix: none | `results/handoff_break/20261022-122543`, `results/handoff_break_mesh/20261022-124134`, rerun `results/handoff_break_mesh/20261022-140843` |
| **Result:** handoff (step 6a) | 350/350 trials, coast profile; every pair on the path RFC 4787 predicts: 320/320 matrix pairs (180 hop, 140 relay), 1540/1540 mesh pairs at 4, 8 and 16 mixed peers, no split pairs; pairs that punch are ready in about 0.5 s, pairs that cannot after the 5 s punch timeout | `results/handoff_matrix/20261022-110401`, `results/handoff_mesh/20261022-113919` |
| **Result:** relay baseline | 380/380 trials; the relay adds 0.56 ms RTT on the path and exactly 2 x its detour off it; goodput matches header overhead exactly; it connects all 32 NAT configurations | `results/relay_path/20261020-141454`, `results/relay_detour/20261020-142341`, `results/relay_matrix/20261020-143233` |
| **Result:** Experiment B, performance cost | 30/30 trials; no latency cost, -2.0% goodput (= Noise overhead), no loss from hopping | `results/direct_path/` (latest run, drain-based close) |

## Not done

| Part | What it is for |
|---|---|
| Full mesh, next: per-peer clock offsets in mesh trials, port clashes between a peer's links, a long-running mesh (members leaving and rejoining) | The paper's system is a mesh of pairwise direct links |
| Handoff Manager, next: start every link on the relay and move it to hopping once the punch works (removes the punch-timeout wait), and move links back to hopping | Faster setup for pairs that cannot punch; links that recover |
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

## Experiment B: what do Noise, hopping and relaying cost?

`punch_path` (raw), `direct_path` (Noise + hopping) and `relay_path` (Noise
through the TURN relay, relay on the path), realistic pairing
(port-restricted on both sides), 50 Mbit/s links, 60 Mbit/s offered per
direction, 1 s slots. Medians of 10 trials, both sides. Compared with
`p2prunner compare`; the three experiments share their configuration IDs.

| Profile | RTT p50 raw / direct / relay | rx_ip_mbit raw / direct / relay | rx_mbit raw / direct / relay | rx_loss raw / direct / relay | Handshake direct / relay |
|---|---|---|---|---|---|
| local | 10.68 / 10.67 / 11.23 ms | 49.43 / 49.44 / 49.43 | 48.31 / 47.35 / 47.04 | 0.143 / 0.159 / 0.164 | 8.8 / 9.4 ms |
| coast | 70.55 / 70.96 / 71.95 ms | 49.34 / 49.36 / 49.33 | 48.22 / 47.27 / 46.94 | 0.153 / 0.169 / 0.173 | 64.0 / 62.0 ms |
| transatlantic | 151.95 / 151.44 / 151.79 ms | 49.34 / 49.30 / 49.31 | 48.21 / 47.22 / 46.93 | 0.165 / 0.180 / 0.185 | 128.6 / 118.5 ms |

Runs: `results/relay_path/20261020-141454` against the `direct_path` and
`punch_path` runs below.

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
- **The relay on the path costs 0.56 ms of RTT** (median over the three
  profiles; coturn's processing) and nothing in jitter or handshake time.
- **Relayed goodput is exactly the header arithmetic:** each relayed packet
  carries 33 bytes more than raw (Noise's 29 since mesh step 3a, plus
  ChannelData's 4), so raw x 1228/1261 predicts 47.05 / 46.96 / 46.95,
  measured 47.04 / 46.94 / 46.93. Against today's direct channel (29
  bytes), relaying costs only ChannelData's 0.3%; the 0.7% gap in the
  table is mostly the receiver index added after `direct_path` ran.

**Relay detour** (`relay_detour`, coast, the relay delaying what it sends):

| Relay delay | RTT p50 | Direct + 2 x delay | rx_mbit | Handshake |
|---|---|---|---|---|
| 0 ms | 71.88 ms | 70.96 | 46.98 | 62.2 ms |
| 10 ms | 91.56 ms | 90.96 | 46.93 | 78.3 ms |
| 40 ms | 151.39 ms | 150.96 | 46.98 | 120.3 ms |

A relay out of the way costs exactly its detour, twice per round trip, and
nothing else: goodput and loss do not move. A relay a continent away (40 ms)
doubles the coast RTT. Run: `results/relay_detour/20261020-142341`.

**Relay reach** (`relay_matrix`, all 16 pairings with and without CGNAT,
coast, 10 trials each): 320/320 trials connected through the relay, every
pairing as predicted, including the 8 configurations that cannot punch.
Direct hopping holds in 18 of the 32 (Experiment A); the relay is the only
path in the other 14. Run: `results/relay_matrix/20261020-143233`.

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

## Handoff Manager: does every link connect, on the right path?

`handoff_matrix` (all 16 pairings x side B on the WAN or behind CGNAT) and
`handoff_mesh` (4, 8 and 16 peers, NAT types round-robin), coast profile,
`-handoff -direct -hop-interval 1s -punch-timeout 5s`, 10 trials each.
Summarised with `p2prunner mesh`.

| Group | Configurations / pairs per trial | Path | Ready (median of medians) |
|---|---|---|---|
| No symmetric NAT on either side | 18 configurations | hop, 180/180 pairs | 0.50 to 0.60 s |
| One side symmetric, the punch works | 6 | relay (`ports`), 60/60 | 0.46 to 0.53 s |
| The punch fails (symmetric vs port-restricted or symmetric; a CGN counts as port-restricted) | 8 | relay (`punch`), 80/80 | 5.34 to 5.39 s |
| Mixed mesh, 4 / 8 / 16 peers | 6 / 28 / 120 pairs | 3 / 15 / 66 hop, the rest relay, every trial | full mesh in 5.35 / 5.40 / 5.57 s |

- Every pair connects, and on the predicted path: as_predicted = 1.00 in
  all 35 configurations, and no pair ever split (one end hopping, the
  other on the relay). With `-direct` alone the 8-peer mixed mesh gets 15
  of its 28 pairs; with `-handoff` all 28.
- Pairs that can punch decide in about half a second, whether they then
  hop or stay on the relay. Pairs that cannot wait out the punch timeout
  first, so a mixed mesh is complete only after about 5.4 s. Starting
  every link on the relay at once and moving it to hopping when the punch
  succeeds would remove that wait; it belongs with moving links back to
  hopping (TODO in `handoff.go`).

## Handoff Manager: how fast does a broken link move?

`handoff_break` (realistic pairing, 3 distances, `-pivot-after` 1 or 3) and
`handoff_break_mesh` (8 mixed peers, coast), 1 s slots, 200 pings at 50
ms; 3 s in, peer a's NAT starts dropping everything but its traffic with
the servers. Time from the break until the second side moved (median of
10 trials, and the worst), and pings lost per link end (median):

| Profile | After 1 missed hop | Lost pings | After 3 missed hops | Lost pings |
|---|---|---|---|---|
| local | 2.11 s (max 2.22) | 42.5 | 3.85 s (max 4.11) | 77.5 |
| coast | 1.96 s (max 2.40) | 41 | 4.19 s (max 4.34) | 85 |
| transatlantic | 1.99 s (max 2.30) | 43.5 | 4.13 s (max 4.49) | 86 |
| 8-peer mixed mesh, coast | | | 4.11 s (max 4.55), 5/5 pairs every trial | 84 |

- Every hopping link with a broken end moved, in all 70 trials, and
  the relay carried it from then on.
- Detection is set by the slots, not the distance: the rest of the slot
  the break falls in (0.5 s on average), the missed slots, and the old
  port's overlap before the slot is judged (up to 0.5 s on an idle
  link). The outage is the whole of that: about 20 pings lost per second.
- Both sides of these breaks moved on missed hops (`pivot_reason`
  `missed`), since a NAT that drops both directions silences both. The
  `member` trigger matters for one-way breaks (loopback test: the second
  side followed within about 40 ms).
- The mesh run also had 45 single link ends (4.5 per trial) on pairs
  without peer a "moving" after the pair's pings were over: with
  `-rate-mbit 0` a member that is done may leave while its peer still
  waits on a slow link (one that cannot punch), and the peer counted the
  departed member's silence as missed hops. The measurements were not
  affected. Fixed: a link stops watching once its measurements with the
  member are over, and the report now counts such ends
  (`pivots_unexpected`, marked `!`). The pivot times above are of a's
  links only and stand. The mesh rerun with the fix
  (`results/handoff_break_mesh/20261022-140843`): 5/5 pairs moved in
  every trial, no other link end, 4.02 s median (max 4.45).
- Shorter detection needs shorter slots or `-pivot-after 1`; whether
  1 causes moves without a break on lossy paths is open (a hop is missed
  only if nothing arrives for a whole slot, while probes go every 20 ms
  until heard).

## Paper updates pending

- Scope: the system is a full mesh of pairwise direct links, each with its
  own Noise session, hop key and schedule (the proposal still speaks of "the
  two authenticated peers"). In each pair the lower static key initiates.
  Peers meet in rendezvous rooms; "already in a network" means each peer
  holds the members' public keys (a key directory).
- Threat model: the rendezvous is unauthenticated, so anyone who knows a
  room number learns its members' addresses (not their traffic: the
  handshake still proves keys). Say so, or authenticate Register. In
  relayed mode it hands out only relayed addresses, so members never learn
  each other's IPs; the rendezvous and the relay themselves still see them.
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
  (tables above), with the relay as Experiment B's third arm: on the path
  it costs under a millisecond, off the path exactly its detour; it reaches
  every NAT pairing, hopping 18 of 32.
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
- Mesh formation, worst against best case (`mesh_join`, coast profile):
  every pair did as RFC 4787 predicts whether 8 or 16 peers start together
  or 2 s apart, and the median join time is about 425 ms in every case:
  six round trips (introduction, punch, connected barrier, Noise
  handshake, port check, first hop both ways). Join time is set by the
  protocol's round trips, not by load; folding the port check into the
  handshake would save one. (On a 4-CPU VM, 16 peers starting together did
  slow joins, 317 against 169 ms without netem: load matters only when CPU
  runs out.) Time to full mesh, all at once: 0.47 s for 8 peers, 0.63 s for
  16.
- Find and verify a citation on AI-driven attacks against moving-target
  defenses.
