"""Test doubles: a testbed that answers like the real containers, and a
writer for result folders."""

from __future__ import annotations

import json
import tempfile
from pathlib import Path

from p2prunner.checks import SIDES

NATDISCOVERY = {"EI": "Endpoint Independent", "AD": "Address Dependent",
                "APD": "Address and Port Dependent"}


class Done:
    """Stands in for subprocess.CompletedProcess."""

    def __init__(self, stdout: str = "", returncode: int = 0):
        self.stdout, self.stderr, self.returncode = stdout, "", returncode


class FakeTestbed:
    """Answers the commands the checks send. By default the testbed is
    healthy: A on the WAN, B behind a CGN, all port-restricted."""

    def __init__(self, **kw):
        self.nat = kw.get("nat", ("port_restricted", "port_restricted"))
        self.upstream = kw.get("upstream", ("wan", "cgnat"))
        self.cgn = kw.get("cgn", "port_restricted")
        self.public = kw.get("public", {})              # side -> IP STUN reports
        self.bypass = kw.get("bypass", False)           # WAN host reaches the peer
        self.behaviour = kw.get("behaviour", {})        # side -> (mapping, filtering)
        self.running = kw.get("running", True)
        self.ups: list = []                             # every reconfiguration, in order

    def compose(self, *args, env=None, timeout=0):
        if args[:1] == ("ps",):
            return Done("nat-a\nnat-b\npeer-a\npeer-b\nstun\n" if self.running else "stun\n")
        self.ups.append(("compose", args))
        return Done()

    def up(self, env):
        self.ups.append(env)

    def sh(self, service, script, timeout=0):
        if service.startswith("nat-"):
            i = "ab".index(service[-1])
            return Done(f"{self.nat[i]} {self.upstream[i]}\n")
        if service.startswith("cgn-"):
            return Done(self.cgn + "\n")
        if "turnutils_stunclient" in script:
            return Done(f"0: : IPv4. UDP reflexive addr: {self.public_ip(service[-1])}:40000\n")
        raise AssertionError(f"unexpected sh on {service}: {script}")

    def exec(self, service, command, timeout=0):
        if "turnutils_natdiscovery" in command:
            m, f = self.behaviour.get(service[-1], ("EI", "APD"))
            return Done(f"NAT with {NATDISCOVERY[m]} Mapping!\nNAT with {NATDISCOVERY[f]} Filtering!\n")
        if command[0] == "ip":
            return Done()
        if command[0] == "ping":
            return Done(returncode=0 if self.bypass else 1)
        raise AssertionError(f"unexpected exec on {service}: {command}")

    def parallel(self, jobs):
        return {name: fn() for name, fn in jobs.items()}

    def public_ip(self, side: str) -> str:
        if side in self.public:
            return self.public[side]
        key = "cgn_wan" if self.upstream["ab".index(side)] == "cgnat" else "wan"
        return SIDES[side][key]


class ResultFolders:
    """Writes results/<name>/<run>/trials.jsonl folders in a temp dir."""

    def __init__(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)

    def write(self, name: str, rows: list[dict]) -> str:
        run = self.root / name
        run.mkdir(parents=True)
        (run / "trials.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows))
        return str(run)

    def cleanup(self):
        self._tmp.cleanup()


def trial(config_id: str, **columns) -> dict:
    """A result row: config columns for a port-restricted pair, plus columns."""
    row = {"config_id": config_id, "ok": True, "nat_a": "port_restricted",
           "nat_b": "port_restricted", "upstream_b": "wan", "net_profile": "coast"}
    row.update(columns)
    return row


def sides(**columns) -> dict:
    """The same value for a_<column> and b_<column>."""
    return {f"{side}_{k}": v for k, v in columns.items() for side in "ab"}
