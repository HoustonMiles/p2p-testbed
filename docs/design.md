# How the system works

## Code layout

`p2p/` is one Go module. Each folder is one part of the system, each
protocol lives in one folder, and every folder keeps its tests beside its
code (`_test.go`). Dependencies only point down this list.

```
p2p/
  cmd/punch/          the peer program: flags -> peer.Run -> one JSON line per link
  cmd/rendezvous/     the rendezvous program
  internal/
    peer/             one peer's run, with one link per member: peer.go (the steps),
                      socket.go (the one socket, dispatch to links), meet.go (STUN, rendezvous),
                      link.go (one link: its goroutine and state), punch.go, handshake.go,
                      hop.go, barrier.go, measure.go, result.go; loopback_test.go and
                      mesh_test.go run whole trials
    rendezvous/       rooms: server.go, and client.go (a member's side)
    stun/             STUN messages, with MESSAGE-INTEGRITY (the format TURN builds on)
    turn/             the TURN client: allocation with long-term credentials, permissions,
                      channels, ChannelData (the relay baseline; used by the peer from step 5b)
    direct/           the port-hopping channel: schedule, hopper, slots, probe, drain, receive
    noise/            Noise handshake, encrypted session, replay window
    identity/         static keys and trusted-key directories
    wire/             the packet format everything above shares
```

The protocol packages (`rendezvous`, `stun`, `noise`) never touch a socket:
they build packets and interpret replies, and `peer` carries them, because
every step before hopping must use the one socket whose NAT mapping the
rendezvous saw.

Both programs are compiled into the peer image
(`testbed/host/Dockerfile` runs `go vet` and `go test` during the build).

## Raw UDP hole-punching baseline

- `rendezvous` runs as the `rendezvous` service and introduces the members
  of each room to each other: every Register is answered with one PeerInfo
  per other member (its address, ID and key), and each of them is told about
  the sender. A member not refreshed for `-ttl` (5 s) is dropped and the rest
  of the room is sent PeerLeft. It never relays peer traffic.
- `punch` runs on every member of a room at once (`-peers N`, default 2),
  on one UDP socket: STUN, registration in the room until the other N-1
  members are introduced, then on each link at once: hole punching
  (switching to a member's real address if its packets arrive from a
  different port) and pings; then throughput one link at a time: in each
  pair the peer with the lower ID sends first, then the other. It prints one
  JSON line of results per link at the end.

**A full mesh.** Each member keeps one link per other member. From the
punch on, each link runs on its own goroutine and moves to its next step as
soon as both its ends are ready, meeting the member at a barrier between
steps (each waits `-step-timeout`). A link that fails (no punch, no hop, a
member that left) ends with its result, and no other link waits for it:
`ready_ms` says when each link became usable. (Until mesh step 4b all links
moved through the steps together, so one link that never punched held every
other link of its peer back for the whole punch timeout.) The throughput
tests run pair by pair in one order every member computes the
same way, sorted by the pair's IDs: no two members can wait on each other in
a cycle, and no two tests share a peer's link capacity (a "ready" barrier
starts each pair's test). Pairs with no member in common may test at the
same time. While running, each member re-registers every second, so the
rendezvous never expires it; if a member does leave, links still waiting on
it fail at once ("left the room") instead of timing out. Each link starts
the moment the rendezvous introduces its member, so members may arrive at
different times and the mesh grows while they come; throughput tests wait
until the room is complete. Once it is full, or after `-rendezvous-timeout`
with fewer than N-1, later members are ignored and the run goes on with
those it met (`members`).

Two waits still span links. A peer starts its throughput tests only once
all its links are done pinging, so no ping is measured under a test's load.
And a pair's "ready" barrier before its test allows for the member still
bringing up its other links and testing the pairs ahead of ours.

Every packet carries the room and the sender's peer ID (the start of its
static key in direct mode, random otherwise); a peer accepts packets only
from members it was introduced to, and routes each to its link.

No encryption and no port-hopping: these numbers are the floor that the Noise
channel and the relay are compared against.

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
  session keys. Each packet starts with a 13-byte header (type, the
  receiver's 4-byte session index, the 8-byte counter) and ends with a
  16-byte tag: 29 bytes per packet.
- `direct/ports.go`: one peer's hop sockets, shared by all its links. When
  two links' schedules pick the same port in the same slot they use one
  socket, and each packet goes to the link its receiver index names; packets
  naming no link are dropped before any crypto (`hop_unrouted`).
- `direct/schedule.go`: time is cut into slots of `-hop-interval`; each
  side's port for each slot is HKDF(hop key, side, slot), from the range
  10000-32767 (below Linux's ephemeral ports), where side is initiator or
  responder. Nothing about the schedule is sent.
- `direct/hopper.go` (with `slots.go`, `probe.go`, `drain.go`): opens the
  next slot's port `-hop-lead` early and probes the peer's next port until
  both directions work (the hole punch, repeated on every hop), sends on the
  current slot, and closes the old port once it has drained: after
  `-hop-min-overlap`, as soon as traffic has moved to the new port and the
  old one has been quiet for `-hop-drain-idle`, and at the latest after
  `-hop-overlap`.

Steps added after the punch: Noise handshake; a barrier carrying each side's
port check (does its NAT keep the local port); if both do (or `-hop-force`),
start the hopper and pass a barrier through it that needs an ack, proving the
first hop works both ways. A port check that fails, or a first hop that does
not connect, is a result (`hop_possible` / `hop_connected` = false), not an
error.

### Handshake patterns and keys

Each peer has a static keypair in `./keys` (mounted at `/keys`; make them
with `punch -genkey /keys/a` and `/keys/b`). The rendezvous passes each
peer's public key to the other with the addresses, for the setups that take
keys from it. The peer with the lower public key initiates, so no peer needs
to be told a role; with `-peer-key none` the key the rendezvous announced
sets the order, and the handshake still authenticates whoever answers. With
a directory of trusted keys, the rendezvous's announcement picks which
member's key the handshake must prove; a key outside the directory is
refused before the handshake.

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

## Flags

`punch -h` lists every flag. The direct channel's:

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

## Result columns

One row per link. The peer's own columns repeat in each of its rows:
`name`, `room`, `id`, `members` (the room's members it met, itself
included), `local`, `stun_*`, `rdv_addr`, `mapping_varies`, the settings,
and `start_unix_ms` (when its run began, which puts every member's `*_ms`
columns on one timeline).
The link's own: `peer_id` (the member at the other end), `error`, the
rendezvous and punch steps, `ready_ms` (start to the link being usable:
punched, and in direct mode hopping both ways), the ping and throughput measurements
(`rtt_ms_*`, `tx_*`, `rx_*`), and `rx_start_ms` / `rx_end_ms` (when the
link's Data arrived, since start: the test's place in the run). Before mesh
step 2, `name` and `room` were `role` and `session`.

Direct mode adds: `direct`, `noise`, `peer_key_from`, `initiator`,
`handshake_ms`, `keeps_port`, `peer_keeps_port`, `hop_possible`, `hopping`,
`hop_connected`, `hop_interval_ms`, `clock_offset_ms`, `clock_drift_ppm`,
and the hopper's counters, all prefixed `hop_`:

| Column | Meaning |
|---|---|
| `hop_hops_ok`, `hop_hops_missed` | slots on which something from the peer arrived on our port, until the link's measurements ended (the slot running at start is not counted; nor, since mesh step 3b, the linger at the end) |
| `hop_sent`, `hop_received`, `hop_send_errors` | Data packets |
| `hop_probes_sent`, `hop_probes_recv` | pinhole probes |
| `hop_unrouted` | dropped on arrival, peer-wide (the same in every link's row): the receiver index named none of this peer's links, including packets too short to have one |
| `hop_foreign`, `hop_auth_failed`, `hop_replayed` | dropped on arrival by the link: wrong source IP (before any crypto), failed authentication, replay |
| `hop_bind_failed` | our port for a slot was taken |
| `hop_drain_ms_avg` | how long old ports stayed open after the boundary (mixes busy and idle hops) |
| `hop_drain_cutoffs` | old ports closed at `-hop-overlap` while Data was still arriving: the hops that can lose packets. Should be 0. |
| `hop_late_opens` | the sender found the current slot not yet open (housekeeping fell behind). Should be 0. |
