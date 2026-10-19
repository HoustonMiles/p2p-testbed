"""
Testbed checks: the smoke test and the RFC 5780 NAT verification.

  uv run python -m p2prunner smoke          # wiring: NAT happens, no bypass (seconds)
  uv run python -m p2prunner verify         # NAT behaviour of the running testbed (~10 s)
  uv run python -m p2prunner verify --all   # every NAT type, as home router and as CGN (~1 min)

They check every peer that is running, however many. The NAT expectations
and the measurement are the ones the `nat_verify` trial uses, so these
checks and the experiments can never disagree.
"""

from __future__ import annotations

import re
import subprocess

from p2prunner import topology
from p2prunner.experiment import Config, resolve
from p2prunner.testbed import Testbed, TestbedError
from p2prunner.trials import STUN_IP, expected_behaviour, measure_behaviour

TYPES = ("full_cone", "restricted", "port_restricted", "symmetric")


class Report:
    """Collects PASS/FAIL lines and turns them into an exit status."""

    def __init__(self):
        self.failures = 0

    def check(self, ok: bool, message: str) -> None:
        print(f"  {'PASS' if ok else 'FAIL'}  {message}")
        self.failures += not ok

    def finish(self, all_ok: str) -> int:
        print()
        if self.failures:
            print(f"{self.failures} check(s) failed.")
            return 1
        print(all_ok)
        return 0


def preflight(tb: Testbed, need_running: bool = True) -> str | None:
    """Why the checks cannot run, or None. Without this, a stopped testbed
    would make the bypass check pass for the wrong reason."""
    try:
        if subprocess.run(["docker", "info"], capture_output=True, timeout=30).returncode != 0:
            return "Cannot reach the Docker daemon. Is the docker service running?"
    except (OSError, subprocess.TimeoutExpired):
        return "Cannot run docker. Is it installed?"
    if not need_running:
        return None
    running = tb.services()
    missing = [s for s in ("stun", "peer-a", "nat-a", "peer-b", "nat-b") if s not in running]
    missing += [f"nat-{p}" for p in tb.running_peers() if f"nat-{p}" not in running]
    if missing:
        return (f"Not running: {', '.join(missing)}. Start the testbed first:\n"
                "  docker compose up -d --build --wait   (or: p2prunner up --peers N)")
    return None


def running_config(tb: Testbed) -> Config:
    """The configuration the testbed is running now, read from the routers."""
    names = tb.running_peers()
    settings: dict = {"peers": len(names)}
    for name in names:
        nat_type, upstream = tb.sh(f"nat-{name}", "echo $NAT_TYPE $UPSTREAM").stdout.split()
        settings[f"nat_{name}"], settings[f"upstream_{name}"] = nat_type, upstream
        if upstream == "cgnat":
            settings[f"cgn_{name}"] = tb.sh(f"cgn-{name}", "echo $NAT_TYPE").stdout.strip()
    return Config(index=0, factors=resolve(settings), bundles={})


def describe(cfg: Config, name: str) -> str:
    desc = cfg.factors[f"nat_{name}"]
    if cfg.factors[f"upstream_{name}"] == "cgnat":
        desc += f" behind CGN {cfg.factors[f'cgn_{name}']}"
    return desc


# =============================================================================
# smoke
# =============================================================================
def smoke(tb: Testbed) -> int:
    """For each peer: it reaches the WAN, STUN sees the expected public IP
    (so NAT happened), and a WAN host cannot route around the NAT."""
    cfg = running_config(tb)
    report = Report()
    for p in cfg.peers:
        print(f"Peer {p.name}: {describe(cfg, p.name)}")
        public = p.public_ip(cfg.factors[f"upstream_{p.name}"] == "cgnat")
        check_stun(tb, p.name, public, report)
        check_no_bypass(tb, p, public, report)
    return report.finish("All checks passed.")


def check_stun(tb: Testbed, name: str, public: str, report: Report) -> None:
    # The client goes on to RFC 5780 requests a filtering NAT may rightly
    # block, so its run time is capped and only the first reply is read.
    out = tb.sh(f"peer-{name}", f"timeout 3 turnutils_stunclient {STUN_IP} 2>/dev/null").stdout
    m = re.search(r"UDP reflexive addr: *([0-9.]+):([0-9]+)", out)
    if not m:
        report.check(False, f"peer-{name} got no STUN reply")
        return
    seen = f"{m.group(1)}:{m.group(2)}"
    report.check(m.group(1) == public, f"STUN sees peer-{name} as {seen} (expected IP {public})")


def check_no_bypass(tb: Testbed, p: topology.Peer, public: str, report: Report) -> None:
    """Route the peer's private subnet via its public IP from the STUN host
    and ping the peer: the NAT must drop it."""
    if tb.exec("stun", ["ip", "route", "replace", p.lan_subnet, "via", public]).returncode != 0:
        report.check(False, "could not add the test route on the STUN host (check skipped)")
        return
    reached = tb.exec("stun", ["ping", "-c", "1", "-W", "1", p.peer_ip]).returncode == 0
    tb.exec("stun", ["ip", "route", "del", p.lan_subnet])
    report.check(not reached, f"WAN host cannot reach private {p.peer_ip} directly")


# =============================================================================
# verify
# =============================================================================
def verify(tb: Testbed, every_type: bool) -> int:
    report = Report()
    if every_type:
        verify_every_type(tb, report)
    else:
        verify_running(tb, running_config(tb), report)
    return report.finish("All NAT behaviour checks passed.")


def verify_running(tb: Testbed, cfg: Config, report: Report) -> None:
    raw: dict = {}
    names = [p.name for p in cfg.peers]
    measured = tb.parallel({n: (lambda n=n: measure_behaviour(tb, n, raw)) for n in names})
    for name in names:
        exp_m, exp_f = expected_behaviour(cfg, name)
        got_m, got_f = measured[name]
        detail = f"mapping={got_m} filtering={got_f}"
        if (got_m, got_f) != (exp_m, exp_f):
            detail += f" (expected {exp_m}/{exp_f})"
        report.check((got_m, got_f) == (exp_m, exp_f), f"peer {name:<3} {describe(cfg, name):<46} {detail}")


def every_type_config() -> Config:
    """Eight peers in one configuration: each NAT type as a home router on
    the WAN (a-d), and as a CGN behind a full cone home router, so that the
    CGN decides (e-h)."""
    settings: dict = {"peers": 2 * len(TYPES)}
    for i, nat_type in enumerate(TYPES):
        home, behind = topology.peer_name(i + 1), topology.peer_name(len(TYPES) + i + 1)
        settings[f"nat_{home}"] = nat_type
        settings.update({f"nat_{behind}": "full_cone", f"upstream_{behind}": "cgnat", f"cgn_{behind}": nat_type})
    return Config(index=0, factors=resolve(settings), bundles={})


def verify_every_type(tb: Testbed, report: Report) -> None:
    cfg = every_type_config()
    print(f"Every NAT type at once: {len(cfg.peers)} peers")
    try:
        tb.up(cfg)
    except TestbedError as e:
        report.check(False, f"testbed did not start: {str(e).strip().splitlines()[-1]}")
        return
    verify_running(tb, cfg, report)
    # Back to the committed stack, with the user's .env settings.
    print("Restoring the default configuration...")
    tb.down()
    tb.compose("up", "-d", "--wait", timeout=300)
