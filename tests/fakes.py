"""Test doubles: a testbed that answers like the real containers, and a
writer for result folders."""

from __future__ import annotations

import json
import tempfile
from pathlib import Path

from p2prunner import topology

NATDISCOVERY = {"EI": "Endpoint Independent", "AD": "Address Dependent",
                "APD": "Address and Port Dependent"}


class Done:
    """Stands in for subprocess.CompletedProcess."""

    def __init__(self, stdout: str = "", returncode: int = 0):
        self.stdout, self.stderr, self.returncode = stdout, "", returncode


class FakeTestbed:
    """Answers the commands the checks send. By default the testbed is
    healthy: a on the WAN, b behind a CGN, all port-restricted. One entry
    per peer in nat and upstream makes a testbed of that many peers."""

    def __init__(self, **kw):
        self.nat = kw.get("nat", ("port_restricted", "port_restricted"))
        self.upstream = kw.get("upstream", ("wan", "cgnat") + ("wan",) * (len(self.nat) - 2))
        self.cgn = kw.get("cgn", "port_restricted")
        self.public = kw.get("public", {})              # peer -> IP STUN reports
        self.bypass = kw.get("bypass", False)           # WAN host reaches the peer
        self.behaviour = kw.get("behaviour", {})        # peer -> (mapping, filtering), or a list of answers in turn
        self.running = kw.get("running", True)
        self.relay = kw.get("relay", True)              # the relay answers
        self.ups: list = []                             # every reconfiguration, in order

    @property
    def names(self) -> list[str]:
        return [p.name for p in topology.peers(len(self.nat))]

    def compose(self, *args, env=None, timeout=0):
        self.ups.append(("compose", args))
        return Done()

    def services(self) -> list[str]:
        if not self.running:
            return ["stun"]
        return ["stun", "relay"] + [f"{kind}-{n}" for n in self.names for kind in ("nat", "peer")]

    def running_peers(self) -> list[str]:
        return [s.removeprefix("peer-") for s in self.services() if s.startswith("peer-")]

    def up(self, cfg):
        self.ups.append(cfg)

    def down(self):
        self.ups.append(("down",))

    def sh(self, service, script, timeout=0):
        if service.startswith("nat-"):
            i = topology.peer_number(service.removeprefix("nat-")) - 1
            return Done(f"{self.nat[i]} {self.upstream[i]}\n")
        if service.startswith("cgn-"):
            return Done(self.cgn + "\n")
        if "turnutils_stunclient" in script and topology.RELAY_IP in script and not self.relay:
            return Done()
        if "turnutils_stunclient" in script:
            return Done(f"0: : IPv4. UDP reflexive addr: {self.public_ip(service.removeprefix('peer-'))}:40000\n")
        raise AssertionError(f"unexpected sh on {service}: {script}")

    def exec(self, service, command, timeout=0):
        if "turnutils_natdiscovery" in command:
            answer = self.behaviour.get(service.removeprefix("peer-"), ("EI", "APD"))
            if isinstance(answer, list):
                answer = answer.pop(0) if len(answer) > 1 else answer[0]
            lines = [f"NAT with {NATDISCOVERY[v]} {kind}!" for v, kind in zip(answer, ("Mapping", "Filtering"))
                     if v != "none"]  # no reply: natdiscovery prints no verdict
            return Done("\n".join(lines) + "\n")
        if command[0] == "ip":
            return Done()
        if command[0] == "ping":
            return Done(returncode=0 if self.bypass else 1)
        raise AssertionError(f"unexpected exec on {service}: {command}")

    def parallel(self, jobs):
        return {name: fn() for name, fn in jobs.items()}

    def public_ip(self, name: str) -> str:
        if name in self.public:
            return self.public[name]
        p = topology.peer(name)
        return p.public_ip(self.upstream[p.number - 1] == "cgnat")


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
