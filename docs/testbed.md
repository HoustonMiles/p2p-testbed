# The testbed

## Requirements

- Linux with Docker Engine and the Compose plugin
- Kernel modules `sch_netem`, `ifb` and the ipset modules, and a larger ARP
  table for more than about 30 peers: `testbed/prepare-host.sh` does both,
  once per boot. `p2prunner up` refuses a testbed the ARP table cannot hold
  (past its limit, routers lose track of each other and punches fail as if
  a NAT blocked them).
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
  boot.kernel.sysctl = {
    "net.ipv4.neigh.default.gc_thresh1" = 65536;
    "net.ipv4.neigh.default.gc_thresh2" = 262144;
    "net.ipv4.neigh.default.gc_thresh3" = 524288;
  };
  ```

## Peers

Each peer is one household: a computer (`peer-x`) behind its home router
(`nat-x`), optionally behind its ISP's carrier-grade NAT (`cgn-x`). Peers
are named like spreadsheet columns: `a` ... `z`, `aa` ... `iu` (255 at most).

The committed `docker-compose.yml` has two peers, `a` and `b`, each with a
CGN it may sit behind. For more, the runner writes the stack to
`.testbed/compose.yml` (gitignored) from `p2prunner/compose.py`:

```bash
uv run python -m p2prunner up --peers 8 --cgn e,f   # 8 peers; e and f get a CGN
uv run python -m p2prunner compose --peers 8        # just print the file
uv run python -m p2prunner down                     # back to the committed two peers
```

While `.testbed/compose.yml` exists, `smoke`, `verify` and the runner use
it; plain `docker compose` commands still use the committed file (add
`-f .testbed/compose.yml --project-directory .`). Experiments set the
number of peers per configuration (`peers`, see [running.md](running.md)).

**How many peers.** In a 4-CPU VM, 32 peers (66 containers) ran a full
direct mesh cleanly: 992 links hop-connected in 11 s. At 64 peers the
machine was CPU-bound (load average ~700, mostly Docker's per-container
processes) and links failed for that reason alone. The limit is the
host's CPU; check `uptime` while a large run is going.

## Choosing a configuration

Choose a configuration with environment variables, inline or in a `.env`
file (copy `.env.example`):

```bash
NAT_A_TYPE=symmetric NAT_B_UPSTREAM=cgnat NET_PROFILE=coast \
  docker compose up -d --wait
```

Containers must be recreated (`down` then `up`) for new settings to apply.
The experiment runner sets these itself for each configuration. `X` below
is a peer's name in capitals (`A`, `B`, ..., `AA`).

| Variable | Values (default first) | Meaning |
|---|---|---|
| `NAT_X_TYPE` | `port_restricted`, `full_cone`, `restricted`, `symmetric` | Home router behaviour |
| `NAT_X_UPSTREAM` | `wan`, `cgnat` | Public IP on the home router, or behind the ISP's CGN (needs a CGN in the stack) |
| `CGN_X_TYPE` | same as NAT types | CGN behaviour (only used with `cgnat`) |
| `NET_PROFILE` | `local`, `coast`, `transatlantic`, `none`, `custom` | Distance profile (10 / 70 / 150 ms RTT) |
| `UPLINK_MBIT`, `DOWNLINK_MBIT` | empty = no cap | Access link capacity. **Set `DOWNLINK_MBIT` for flood tests.** |
| `NET_PROFILE_X`, `UPLINK_MBIT_X`, `DOWNLINK_MBIT_X` | empty = the global setting | One peer's own access link |
| `UDP_TIMEOUT`, `UDP_TIMEOUT_STREAM` | `30`, `120` | Seconds an idle UDP pinhole stays open |
| `RELAY_DELAY_MS` | `0` | One-way delay the relay adds to what it sends: a relay out of the way adds 2 x this to a relayed round trip |
| `DEBUG` | `0`, `1` | Print each router's iptables rules at startup |

## Addresses

Defined once, in `p2prunner/topology.py`. For peer number k (a = 1, b = 2, ...):

| Host | Address(es) |
|---|---|
| peer-x | 10.200.k.10 |
| nat-x | LAN 10.200.k.2, WAN 198.18.k.10, carrier 10.201.k.10 |
| cgn-x | carrier 10.201.k.2, WAN 198.18.k.11 |
| stun | 198.18.0.100 and alternate 198.18.0.103, UDP/TCP 3478 and 3479 |
| rendezvous | 198.18.0.101, UDP 7000 |
| relay | 198.18.0.102, UDP 3478 (TURN), relayed addresses on ports 49152 to 65535 |
| reserved | 198.18.0.200+ attackers |

A peer's public IP is its home router's WAN IP, or its CGN's WAN IP when
`UPSTREAM=cgnat`. The WAN is 198.18.0.0/15, the range reserved for
benchmark networks (RFC 2544), so it never clashes with a real network.
Real carrier networks use 100.64.0.0/10 (RFC 6598), but so does Tailscale,
so the testbed's use 10.201.k.0/24; the routers never look at the range.

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
docker compose exec peer-a timeout 3 turnutils_stunclient 198.18.0.100      # your public address
docker compose exec peer-a turnutils_natdiscovery -m -f 198.18.0.100        # mapping + filtering tests
```

`turnutils_stunclient` keeps waiting for RFC 5780 replies that a filtering
NAT correctly blocks, so give it a `timeout`.

## TURN relay

`relay` runs coturn as a TURN relay (RFC 8656), from the same image as
`stun` but its own container, so relay load never affects STUN results.
Login: user `testbed`, password `testbed`, realm `p2p-testbed` (long-term
credentials; not a secret, the testbed never touches a real network).

In relayed mode each peer allocates a relayed address and sends only to the
other peers' relayed addresses, so no peer learns another's real IP; the
traffic stays Noise-encrypted end to end, so the relay never sees what the
peers send. The relay is on the WAN, so a relayed packet crosses the same
two home routers as a direct one: without `RELAY_DELAY_MS` the relay costs
only its processing time, which flatters it against a real relay that is
some distance away.

Check it from the host (the WAN bridge is reachable from there):

```bash
cd p2p && TURN_SERVER=198.18.0.102:3478 go test -run Live -v ./internal/turn
```

Two clients allocate, bind channels to each other's relayed address and
send through the relay. `p2prunner smoke` also checks that a peer reaches
the relay through its NAT.

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
  flow that a cone NAT forwards from the WAN onto a LAN (to the LAN bridge, 10.200.k.1), and
  the peer replies to an address that doesn't exist. This only affected
  unsolicited inbound traffic; the NAT matrix verdicts and ping baselines
  recorded before the fix are unaffected.
- **netem jitter reorders packets.** Keep this in mind when testing the
  replay window.
