"""
What the testbed's NATs should do: each NAT type's RFC 4787 behaviour, a
peer's behaviour behind its NATs, and what that predicts for a pair.

    EI  = endpoint-independent
    AD  = address-dependent
    APD = address-and-port-dependent
"""

from __future__ import annotations

# (mapping, filtering) per NAT type
EXPECTED = {
    "full_cone":       ("EI", "EI"),
    "restricted":      ("EI", "AD"),
    "port_restricted": ("EI", "APD"),
    "symmetric":       ("APD", "APD"),
}
RANK = {"EI": 0, "AD": 1, "APD": 2}


def expected_behaviour(cfg, side: str) -> tuple[str, str]:
    """Expected (mapping, filtering) for one side. Behind CGNAT, the pair of
    NATs behaves like the stricter of the two, separately for each."""
    m, f = EXPECTED[cfg.factors[f"nat_{side}"]]
    if cfg.factors[f"upstream_{side}"] == "cgnat":
        cm, cf = EXPECTED[cfg.factors[f"cgn_{side}"]]
        m = max(m, cm, key=RANK.get)
        f = max(f, cf, key=RANK.get)
    return m, f


def predicted(cfg, a: str, b: str, direct: bool) -> str:
    """What peers a and b should reach: "hop", "punch" or "none".

    A punch fails when one side's mapping is port-dependent and the other
    side filters by port: the port the rendezvous saw is not the one used
    toward the peer, and the peer drops what comes from anywhere else.
    Hopping also needs both mappings endpoint-independent: the port check
    asks that each NAT keeps the local port, which the testbed's Linux NATs
    do exactly when the mapping is endpoint-independent.
    """
    (ma, fa), (mb, fb) = expected_behaviour(cfg, a), expected_behaviour(cfg, b)
    if (ma == "APD" and fb == "APD") or (mb == "APD" and fa == "APD"):
        return "none"
    if direct and ma == mb == "EI":
        return "hop"
    return "punch"
