"""
The testbed's address plan: every peer and service address, defined once.

Each peer is one household: a computer (peer-x) behind its own home router
(nat-x), optionally behind its ISP's carrier-grade NAT (cgn-x). Peer number
k (1, 2, ...) is named like a spreadsheet column: a, b, ..., z, aa, ab, ...

    peer k          LAN 10.200.k.0/24     peer .10, home router .2
                    carrier 10.201.k.0/24 CGN .2, home router .10
                    public IP 198.18.k.10, or 198.18.k.11 behind its CGN
    services        on the WAN, 198.18.0.x (STUN, rendezvous, relay, attackers)

The WAN (the simulated internet) is 198.18.0.0/15, the range reserved for
benchmark networks (RFC 2544), so it never clashes with a real network.
"""

from __future__ import annotations

from dataclasses import dataclass

MAX_PEERS = 255

WAN_SUBNET = "198.18.0.0/15"
WAN_GATEWAY = "198.18.0.1"           # Docker's bridge; nothing routes through it
STUN_IP = "198.18.0.100"
STUN_ALT_IP = "198.18.0.103"         # RFC 5780 needs a second address
RENDEZVOUS_IP = "198.18.0.101"
RELAY_IP = "198.18.0.102"            # reserved for the TURN relay
# 198.18.0.200 and up: reserved for attackers.

LAN_PREFIX = "10.200"                # 10.200.k.0/24: rarely used by real networks
# Real carrier networks use 100.64.0.0/10 (RFC 6598), but so does Tailscale,
# often on the same machine. The routers never look at the range.
CARRIER_PREFIX = "10.201"


def peer_name(k: int) -> str:
    """1 -> 'a', 26 -> 'z', 27 -> 'aa', 255 -> 'iu'."""
    if not 1 <= k <= MAX_PEERS:
        raise ValueError(f"peer number must be 1..{MAX_PEERS}, got {k}")
    name = ""
    while k:
        k, rem = divmod(k - 1, 26)
        name = chr(ord("a") + rem) + name
    return name


def peer_number(name: str) -> int:
    """The inverse of peer_name: 'a' -> 1, 'aa' -> 27."""
    if not name.isalpha() or not name.islower():
        raise ValueError(f"not a peer name: {name!r}")
    k = 0
    for ch in name:
        k = k * 26 + ord(ch) - ord("a") + 1
    if k > MAX_PEERS:
        raise ValueError(f"peer {name!r} is beyond the {MAX_PEERS} the address plan has room for")
    return k


@dataclass(frozen=True)
class Peer:
    """One household's addresses."""
    number: int

    @property
    def name(self) -> str:
        return peer_name(self.number)

    @property
    def lan_subnet(self) -> str:
        return f"{LAN_PREFIX}.{self.number}.0/24"

    @property
    def lan_bridge(self) -> str:      # Docker's bridge on the LAN; nothing routes through it
        return f"{LAN_PREFIX}.{self.number}.1"

    @property
    def router_lan_ip(self) -> str:
        return f"{LAN_PREFIX}.{self.number}.2"

    @property
    def peer_ip(self) -> str:
        return f"{LAN_PREFIX}.{self.number}.10"

    @property
    def carrier_subnet(self) -> str:
        return f"{CARRIER_PREFIX}.{self.number}.0/24"

    @property
    def carrier_bridge(self) -> str:
        return f"{CARRIER_PREFIX}.{self.number}.1"

    @property
    def cgn_inside_ip(self) -> str:
        return f"{CARRIER_PREFIX}.{self.number}.2"

    @property
    def router_carrier_ip(self) -> str:
        return f"{CARRIER_PREFIX}.{self.number}.10"

    @property
    def wan_ip(self) -> str:
        """The home router's public IP, when it is on the WAN directly."""
        return f"198.18.{self.number}.10"

    @property
    def cgn_wan_ip(self) -> str:
        """The public IP of the CGN in front of this peer."""
        return f"198.18.{self.number}.11"

    def public_ip(self, behind_cgn: bool) -> str:
        return self.cgn_wan_ip if behind_cgn else self.wan_ip


def wan_neighbours(n: int, cgn_count: int) -> int:
    """ARP entries the WAN can need: every host on it (each peer's router or
    CGN, and the three services) may talk to every other. The kernel counts
    them for all containers together."""
    hosts = n + cgn_count + 3
    return hosts * (hosts - 1)


def peers(n: int) -> list[Peer]:
    if not 1 <= n <= MAX_PEERS:
        raise ValueError(f"the testbed has room for 1..{MAX_PEERS} peers, not {n}")
    return [Peer(k) for k in range(1, n + 1)]


def peer(name: str) -> Peer:
    return Peer(peer_number(name))
