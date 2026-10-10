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
Anything not set in the matrix uses the testbed default (see FACTORS).
"""

from __future__ import annotations

import hashlib
import itertools
import json
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

# Testbed settings an experiment can vary: matrix key -> (environment
# variable read by docker-compose.yml, default value).
FACTORS: dict[str, tuple[str, str]] = {
    "nat_a":         ("NAT_A_TYPE", "port_restricted"),
    "nat_b":         ("NAT_B_TYPE", "port_restricted"),
    "upstream_a":    ("NAT_A_UPSTREAM", "wan"),
    "upstream_b":    ("NAT_B_UPSTREAM", "wan"),
    "cgn_a":         ("CGN_A_TYPE", "port_restricted"),
    "cgn_b":         ("CGN_B_TYPE", "port_restricted"),
    "net_profile":   ("NET_PROFILE", "local"),
    "uplink_mbit":   ("UPLINK_MBIT", ""),
    "downlink_mbit": ("DOWNLINK_MBIT", ""),
    "udp_timeout":   ("UDP_TIMEOUT", "30"),
    "udp_timeout_stream": ("UDP_TIMEOUT_STREAM", "120"),
}

NAT_TYPES = {"full_cone", "restricted", "port_restricted", "symmetric"}


@dataclass
class Config:
    """One network configuration: a full set of testbed settings."""
    index: int
    factors: dict[str, str]        # every FACTORS key -> value (defaults filled in)
    bundles: dict[str, str]        # bundle key -> bundle name, e.g. {"pairing": "realistic"}
    varied: tuple = ()             # plain (non-bundle) matrix keys, shown in the label

    @property
    def env(self) -> dict[str, str]:
        """Environment variables for `docker compose up`."""
        return {FACTORS[k][0]: str(v) for k, v in self.factors.items()}

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
        parts += [f"{k}={self.factors[k]}" for k in self.varied]
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
    axes = []  # one list per matrix key; each entry is (bundle_info, settings)
    varied = []  # matrix keys given as plain values (bundles are named instead)
    for key, values in matrix.items():
        if not isinstance(values, list) or not values:
            raise ValueError(f"matrix.{key} must be a non-empty list")
        options = []
        for v in values:
            if isinstance(v, dict):                       # a bundle
                v = dict(v)
                name = str(v.pop("name", f"{key}{len(options)}"))
                _check_settings(v, where=f"matrix.{key}[{name}]")
                options.append(({key: name}, v))
            else:                                         # a plain value
                _check_settings({key: v}, where=f"matrix.{key}")
                options.append(({}, {key: v}))
                if key not in varied:
                    varied.append(key)
        axes.append(options)

    configs = []
    for i, combo in enumerate(itertools.product(*axes) if axes else [()]):
        factors = {k: default for k, (_, default) in FACTORS.items()}
        bundles: dict[str, str] = {}
        for bundle_info, settings in combo:
            bundles.update(bundle_info)
            factors.update({k: str(v) for k, v in settings.items()})
        configs.append(Config(index=i, factors=factors, bundles=bundles, varied=tuple(varied)))
    return configs


def _check_settings(settings: dict, where: str) -> None:
    """Catch typos early, before an overnight run starts."""
    for key, value in settings.items():
        if key not in FACTORS:
            raise ValueError(f"{where}: unknown setting '{key}' "
                             f"(known: {', '.join(FACTORS)})")
        if key.startswith(("nat_", "cgn_")) and str(value) not in NAT_TYPES:
            raise ValueError(f"{where}: {key}='{value}' is not one of {sorted(NAT_TYPES)}")
        if key.startswith("upstream_") and str(value) not in ("wan", "cgnat"):
            raise ValueError(f"{where}: {key} must be 'wan' or 'cgnat'")
