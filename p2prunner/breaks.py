"""
Breaking the direct path in the middle of a mesh trial, so the Handoff
Manager has to move hopping links to the relay.

    [params]
    break_at_s = 3          # seconds after the peers start; unset: no break
    break_peers = "a"       # whose NAT breaks (comma-separated); default a

At break_at_s each named peer's NAT router starts dropping every forwarded
UDP packet that neither comes from nor goes to the servers (STUN,
rendezvous, relay: 198.18.0.100-103). Its hop ports and punched path go
dead; its relay traffic still flows. The rule is removed when the trial
ends, whatever happens.
"""

from __future__ import annotations

import time
from dataclasses import dataclass

# The servers' four addresses, 198.18.0.100-103 (p2prunner/topology.py).
SERVERS = "198.18.0.100/30"
RULE = ["TB_FWD", "-p", "udp", "!", "-s", SERVERS, "!", "-d", SERVERS, "-j", "DROP"]


@dataclass(frozen=True)
class HopBreak:
    at_s: float
    peers: tuple[str, ...]

    @classmethod
    def of(cls, params: dict) -> "HopBreak | None":
        if params.get("break_at_s") is None:
            return None
        peers = tuple(p.strip() for p in str(params.get("break_peers", "a")).split(",") if p.strip())
        return cls(float(params["break_at_s"]), peers)

    def apply(self, tb) -> float:
        """Wait, then break; returns when it took effect (Unix ms)."""
        time.sleep(self.at_s)
        for peer in self.peers:
            res = tb.exec(f"nat-{peer}", ["iptables", "-I", RULE[0], "1", *RULE[1:]])
            if res.returncode != 0:
                raise RuntimeError(f"could not break nat-{peer}: {res.stderr.strip()}")
        return time.time() * 1000

    def restore(self, tb) -> None:
        for peer in self.peers:
            tb.exec(f"nat-{peer}", ["iptables", "-D", *RULE])

    def touches(self, a: str, b: str) -> bool:
        """Whether the pair has a broken end."""
        return a in self.peers or b in self.peers
