# P2P Testbed

A Docker testbed for the port-hopping vs. relay experiments. Two peers sit
behind real Linux NAT routers (optionally behind carrier-grade NAT too), with
simulated latency, jitter, loss and link capacity between them.

```
[peer-a]--lan_a--[nat-a]--+------------------ wan ------------------+--[nat-b]--lan_b--[peer-b]
                          |                                         |
                          +--carrier_a--[cgn-a]--wan    wan--[cgn-b]--carrier_b--+
```

## Requirements

- Linux with Docker Engine and the Compose plugin
- Kernel modules: `sch_netem`, `ifb`, and the ipset modules (all loaded by `scripts/host_setup.sh`)
- Host reverse-path filtering must be off or "loose". Docker sends traffic
  between containers through the host firewall, and a strict check drops NAT
  replies on the LAN bridges. On NixOS:

  ```nix
  virtualisation.docker.enable = true;
  networking.firewall.checkReversePath = "loose";
  boot.kernelModules = [ "sch_netem" "ifb" "ip_set" "ip_set_bitmap_port" "ip_set_hash_ipport" "xt_set" ];
  ```

  Tailscale also uses 100.64.0.0/10, the same range as the carrier networks.
  Stop `tailscaled` during experiments if CGNAT runs misbehave.

## Quick start

```bash
./scripts/host_setup.sh                 # once per boot (uses sudo)
docker compose up -d --build --wait     # build and start, waits until configured
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
| reserved | .101 rendezvous, .102 coturn relay, .200+ attackers |

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
- **netem jitter reorders packets.** Keep this in mind when testing the
  replay window.

## Coming next

- Step 4: Python runner that loops over configurations and collects results.
