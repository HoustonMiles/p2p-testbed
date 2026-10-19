"""
Experiment files and matrix expansion.

An experiment file (TOML) says WHAT to measure (the trial type and its
parameters), under WHICH network configurations (the matrix), and HOW MANY
times (runs per configuration). Example:

    name = "path_baseline"
    trial = "ping"
    runs = 30

    [params]                 # passed to the trial
    count = 50

    [matrix]                 # every combination of these is one configuration
    net_profile = ["local", "coast", "transatlantic"]

    [[matrix.pairing]]       # a "bundle": several settings that go together
    name = "realistic"
    nat_a = "port_restricted"
    nat_b = "port_restricted"

    [[matrix.pairing]]
    name = "hardest"
    nat_a = "symmetric"
    upstream_b = "cgnat"

The matrix above gives 3 profiles x 2 pairings = 6 configurations.
Anything not set in the matrix uses the testbed default (see GLOBAL_FACTORS
and PEER_FACTORS).

More peers: `peers` sets how many (default 2), and `nats` gives the home
routers' NAT types round-robin (a, b, c, ... take the list's types in
turn). A per-peer key such as nat_c, upstream_c or net_profile_c overrides
that for one peer:

    [matrix]
    peers = [2, 4, 8]
    nats = [["port_restricted", "full_cone", "symmetric"]]
"""

from __future__ import annotations

import hashlib
import itertools
import json
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from p2prunner import topology

# Testbed-wide settings: matrix key -> (environment variable read by the
# compose file, default value).
GLOBAL_FACTORS: dict[str, tuple[str, str]] = {
    "net_profile":   ("NET_PROFILE", "local"),
    "uplink_mbit":   ("UPLINK_MBIT", ""),
    "downlink_mbit": ("DOWNLINK_MBIT", ""),
    "udp_timeout":   ("UDP_TIMEOUT", "30"),
    "udp_timeout_stream": ("UDP_TIMEOUT_STREAM", "120"),
}

# Per-peer settings: nat_c is NAT_C_TYPE, and so on. A default of None means
# the peer takes the testbed-wide setting unless one is given for it.
PEER_FACTORS: dict[str, tuple[str, str | None]] = {
    "nat":           ("NAT_{}_TYPE", "port_restricted"),
    "upstream":      ("NAT_{}_UPSTREAM", "wan"),
    "cgn":           ("CGN_{}_TYPE", "port_restricted"),
    "net_profile":   ("NET_PROFILE_{}", None),
    "uplink_mbit":   ("UPLINK_MBIT_{}", None),
    "downlink_mbit": ("DOWNLINK_MBIT_{}", None),
}

DEFAULT_PEERS = 2
NAT_TYPES = {"full_cone", "restricted", "port_restricted", "symmetric"}


def peer_key(key: str) -> tuple[str, str] | None:
    """('nat', 'c') for 'nat_c'; None for a key that is not per-peer."""
    prefix, _, name = key.rpartition("_")
    if prefix not in PEER_FACTORS:
        return None
    try:
        topology.peer_number(name)
    except ValueError:
        return None
    return prefix, name


def resolve(settings: dict) -> dict[str, str]:
    """Every factor of one configuration, defaults filled in. With the
    default two peers and no new keys, this is exactly the set of factors
    the testbed always had, so those configurations keep their IDs."""
    n = int(settings.get("peers", DEFAULT_PEERS))
    nats = list(settings.get("nats") or [])
    factors = {k: default for k, (_, default) in GLOBAL_FACTORS.items()}
    for i, p in enumerate(topology.peers(n)):
        for prefix, (_, default) in PEER_FACTORS.items():
            if default is not None:
                factors[f"{prefix}_{p.name}"] = default
        if nats:
            factors[f"nat_{p.name}"] = str(nats[i % len(nats)])
    if n != DEFAULT_PEERS:
        factors["peers"] = str(n)
    if nats:
        factors["nats"] = ",".join(map(str, nats))
    factors.update({k: str(v) for k, v in settings.items() if k not in ("peers", "nats")})
    check_peers_exist(factors, n)
    return factors


def check_peers_exist(factors: dict[str, str], n: int) -> None:
    for key in factors:
        if (pk := peer_key(key)) and topology.peer_number(pk[1]) > n:
            raise ValueError(f"{key} names peer {pk[1]}, but the configuration has only {n} peers")


@dataclass
class Config:
    """One network configuration: a full set of testbed settings."""
    index: int
    factors: dict[str, str]        # every factor -> value, defaults filled in (see resolve)
    bundles: dict[str, str]        # bundle key -> bundle name, e.g. {"pairing": "realistic"}
    varied: tuple = ()             # plain (non-bundle) matrix keys, shown in the label

    @property
    def peers(self) -> list[topology.Peer]:
        return topology.peers(int(self.factors.get("peers", DEFAULT_PEERS)))

    @property
    def cgn_peers(self) -> set[str]:
        """The peers this configuration puts behind a CGN."""
        return {p.name for p in self.peers if self.factors[f"upstream_{p.name}"] == "cgnat"}

    @property
    def env(self) -> dict[str, str]:
        """Environment variables for `docker compose up`. Every variable of
        every peer is set, so leftovers in a shell or a .env file can never
        change a configuration by accident; an empty per-peer link setting
        means the testbed-wide one."""
        env = {var: str(self.factors[k]) for k, (var, _) in GLOBAL_FACTORS.items()}
        for p in self.peers:
            for prefix, (var, _) in PEER_FACTORS.items():
                env[var.format(p.name.upper())] = str(self.factors.get(f"{prefix}_{p.name}", ""))
        return env

    @property
    def id(self) -> str:
        """Short, stable ID: the same settings always give the same ID, so
        results from different runs can be matched up."""
        digest = hashlib.sha1(json.dumps(self.factors, sort_keys=True).encode()).hexdigest()
        return f"c{self.index:02d}-{digest[:6]}"

    @property
    def label(self) -> str:
        """Human-readable summary: bundle names plus every plain matrix setting."""
        parts = [f"{k}={v}" for k, v in self.bundles.items()]
        parts += [f"{k}={self.factors.get(k, DEFAULT_PEERS if k == 'peers' else '')}" for k in self.varied]
        return ", ".join(parts) or "defaults"

    def row(self) -> dict:
        """Columns describing this configuration in every result row."""
        return {"config_id": self.id, "config_label": self.label, **self.bundles, **self.factors}


@dataclass
class Experiment:
    name: str
    trial: str
    runs: int
    params: dict = field(default_factory=dict)
    description: str = ""
    block_size: int = 10          # trials of one config run back to back
    seed: int = 1                 # base random seed (order of blocks, trial seeds)
    verify_nat: bool = True       # RFC 5780 check whenever a configuration starts
    configs: list[Config] = field(default_factory=list)
    source_text: str = ""         # the experiment file, saved with the results

    @property
    def total_trials(self) -> int:
        return self.runs * len(self.configs)


def load(path: str | Path) -> Experiment:
    """Read an experiment file and expand its matrix."""
    text = Path(path).read_text()
    data = tomllib.loads(text)

    for key in ("name", "trial", "runs"):
        if key not in data:
            raise ValueError(f"{path}: missing required setting '{key}'")

    exp = Experiment(
        name=data["name"],
        trial=data["trial"],
        runs=int(data["runs"]),
        params=data.get("params", {}),
        description=data.get("description", ""),
        block_size=int(data.get("block_size", 10)),
        seed=int(data.get("seed", 1)),
        verify_nat=bool(data.get("verify_nat", True)),
        source_text=text,
    )
    exp.configs = expand_matrix(data.get("matrix", {}))
    return exp


def expand_matrix(matrix: dict) -> list[Config]:
    """Turn the [matrix] table into the list of all configurations.

    Each matrix key is either:
      - a list of plain values, e.g. net_profile = ["local", "coast"], or
      - a list of tables ("bundles"), each with a "name" and several settings.
    The configurations are every combination (Cartesian product) of the keys.
    """
    axes = [axis_options(key, values) for key, values in matrix.items()]
    # Keys given as plain values appear in the label; bundles are named instead.
    varied = tuple(key for key, values in matrix.items() if any(not isinstance(v, dict) for v in values))
    configs = []
    for i, combo in enumerate(itertools.product(*axes) if axes else [()]):
        settings: dict = {}
        bundles: dict[str, str] = {}
        for bundle_info, options in combo:
            bundles.update(bundle_info)
            settings.update(options)
        configs.append(Config(index=i, factors=resolve(settings), bundles=bundles, varied=varied))
    return configs


def axis_options(key: str, values) -> list[tuple[dict, dict]]:
    """One matrix key's options, each as (bundle name if any, settings)."""
    if not isinstance(values, list) or not values:
        raise ValueError(f"matrix.{key} must be a non-empty list")
    options = []
    for v in values:
        if isinstance(v, dict):
            v = dict(v)
            name = str(v.pop("name", f"{key}{len(options)}"))
            _check_settings(v, where=f"matrix.{key}[{name}]")
            options.append(({key: name}, v))
        else:
            _check_settings({key: v}, where=f"matrix.{key}")
            options.append(({}, {key: v}))
    return options


def _check_settings(settings: dict, where: str) -> None:
    """Catch typos early, before an overnight run starts."""
    for key, value in settings.items():
        if key == "peers":
            _check_peers(value, where)
        elif key == "nats":
            _check_nats(value, where)
        elif key not in GLOBAL_FACTORS and not peer_key(key):
            raise ValueError(f"{where}: unknown setting '{key}' (known: peers, nats, "
                             f"{', '.join(GLOBAL_FACTORS)}, or {', '.join(p + '_<peer>' for p in PEER_FACTORS)})")
        else:
            _check_value(key, value, where)


def _check_value(key: str, value, where: str) -> None:
    if key.startswith(("nat_", "cgn_")) and str(value) not in NAT_TYPES:
        raise ValueError(f"{where}: {key}='{value}' is not one of {sorted(NAT_TYPES)}")
    if key.startswith("upstream_") and str(value) not in ("wan", "cgnat"):
        raise ValueError(f"{where}: {key} must be 'wan' or 'cgnat'")


def _check_peers(value, where: str) -> None:
    if not isinstance(value, int) or not 2 <= value <= topology.MAX_PEERS:
        raise ValueError(f"{where}: peers must be a whole number from 2 to {topology.MAX_PEERS}")


def _check_nats(value, where: str) -> None:
    if not isinstance(value, list) or not value or any(str(v) not in NAT_TYPES for v in value):
        raise ValueError(f"{where}: nats must be a non-empty list of NAT types {sorted(NAT_TYPES)}")
