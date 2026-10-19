# How the system works

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

Both programs are compiled into the peer image
(`testbed/host/Dockerfile` runs `go vet` and `go test` during the build).

## Raw UDP hole-punching baseline

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

Every run reports `name`, `room`, `id` and `peer_id` (mesh step 2; before
it, `name` and `room` were `role` and `session`), the STUN, rendezvous and
punch steps, and the ping and throughput measurements (`rtt_ms_*`, `tx_*`,
`rx_*`).

Direct mode adds: `direct`, `noise`, `peer_key_from`, `initiator`,
`handshake_ms`, `keeps_port`, `peer_keeps_port`, `hop_possible`, `hopping`,
`hop_connected`, `hop_interval_ms`, `clock_offset_ms`, `clock_drift_ppm`,
and the hopper's counters, all prefixed `hop_`:

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
