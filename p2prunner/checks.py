"""
Testbed checks: the smoke test and the RFC 5780 NAT verification.

  uv run python -m p2prunner smoke          # wiring: NAT happens, no bypass (seconds)
  uv run python -m p2prunner verify         # NAT behaviour of the running testbed (~10 s)
  uv run python -m p2prunner verify --all   # every NAT type, as home router and as CGN (~1 min)

The NAT expectations and the measurement are the ones the `nat_verify`
trial uses, so these checks and the experiments can never disagree.
"""

from __future__ import annotations

import re
import subprocess

from p2prunner.experiment import FACTORS, Config
from p2prunner.testbed import Testbed, TestbedError
from p2prunner.trials import STUN_IP, expected_behaviour, measure_behaviour

# TODO(paper §5 Simulation Environment): these mirror docker-compose.yml;
# read them from there so the addresses are defined once.
SIDES = {
    "a": {"peer": "192.168.10.10", "wan": "203.0.113.10", "cgn_wan": "203.0.113.11"},
    "b": {"peer": "192.168.20.10", "wan": "203.0.113.20", "cgn_wan": "203.0.113.21"},
}
REQUIRED = ("nat-a", "nat-b", "peer-a", "peer-b", "stun")
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
    running = tb.compose("ps", "--status", "running", "--services").stdout.split()
    missing = [s for s in REQUIRED if s not in running]
    if missing:
        return (f"Not running: {', '.join(missing)}. Start the testbed first:\n"
                "  docker compose up -d --build --wait")
    return None


def running_config(tb: Testbed) -> Config:
    """The configuration the testbed is running now, read from the routers."""
    factors = {key: default for key, (_, default) in FACTORS.items()}
    for side in "ab":
        nat_type, upstream = tb.sh(f"nat-{side}", "echo $NAT_TYPE $UPSTREAM").stdout.split()
        factors[f"nat_{side}"], factors[f"upstream_{side}"] = nat_type, upstream
        if upstream == "cgnat":
            factors[f"cgn_{side}"] = tb.sh(f"cgn-{side}", "echo $NAT_TYPE").stdout.strip()
    return Config(index=0, factors=factors, bundles={})


def describe(cfg: Config, side: str) -> str:
    desc = cfg.factors[f"nat_{side}"]
    if cfg.factors[f"upstream_{side}"] == "cgnat":
        desc += f" behind CGN {cfg.factors[f'cgn_{side}']}"
    return desc


# =============================================================================
# smoke
# =============================================================================
def smoke(tb: Testbed) -> int:
    """For each side: the peer reaches the WAN, STUN sees the expected public
    IP (so NAT happened), and a WAN host cannot route around the NAT."""
    cfg = running_config(tb)
    report = Report()
    for side in "ab":
        print(f"Side {side.upper()}: {describe(cfg, side)}")
        public = expected_public_ip(cfg, side)
        check_stun(tb, side, public, report)
        check_no_bypass(tb, side, public, report)
    return report.finish("All checks passed.")


def expected_public_ip(cfg: Config, side: str) -> str:
    key = "cgn_wan" if cfg.factors[f"upstream_{side}"] == "cgnat" else "wan"
    return SIDES[side][key]


def check_stun(tb: Testbed, side: str, public: str, report: Report) -> None:
    # The client goes on to RFC 5780 requests a filtering NAT may rightly
    # block, so its run time is capped and only the first reply is read.
    out = tb.sh(f"peer-{side}", f"timeout 3 turnutils_stunclient {STUN_IP} 2>/dev/null").stdout
    m = re.search(r"UDP reflexive addr: *([0-9.]+):([0-9]+)", out)
    if not m:
        report.check(False, f"peer-{side} got no STUN reply")
        return
    seen = f"{m.group(1)}:{m.group(2)}"
    report.check(m.group(1) == public, f"STUN sees peer-{side} as {seen} (expected IP {public})")


def check_no_bypass(tb: Testbed, side: str, public: str, report: Report) -> None:
    """Route the peer's private subnet via its public IP from the STUN host
    and ping the peer: the NAT must drop it."""
    peer = SIDES[side]["peer"]
    subnet = peer.rsplit(".", 1)[0] + ".0/24"
    if tb.exec("stun", ["ip", "route", "replace", subnet, "via", public]).returncode != 0:
        report.check(False, "could not add the test route on the STUN host (check skipped)")
        return
    reached = tb.exec("stun", ["ping", "-c", "1", "-W", "1", peer]).returncode == 0
    tb.exec("stun", ["ip", "route", "del", subnet])
    report.check(not reached, f"WAN host cannot reach private {peer} directly")


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
    measured = tb.parallel({s: (lambda s=s: measure_behaviour(tb, s, raw)) for s in "ab"})
    for side in "ab":
        exp_m, exp_f = expected_behaviour(cfg, side)
        got_m, got_f = measured[side]
        detail = f"mapping={got_m} filtering={got_f}"
        if (got_m, got_f) != (exp_m, exp_f):
            detail += f" (expected {exp_m}/{exp_f})"
        report.check((got_m, got_f) == (exp_m, exp_f), f"side {side.upper()}  {describe(cfg, side):<46} {detail}")


def verify_every_type(tb: Testbed, report: Report) -> None:
    """Each NAT type twice per run: as side A's home router, and as side B's
    CGN behind a full cone home router (so the CGN decides)."""
    for nat_type in TYPES:
        print(f"Configuration: {nat_type}")
        factors = {key: default for key, (_, default) in FACTORS.items()}
        factors.update(nat_a=nat_type, upstream_a="wan", nat_b="full_cone",
                       upstream_b="cgnat", cgn_b=nat_type)
        cfg = Config(index=0, factors=factors, bundles={})
        try:
            tb.up(cfg.env)
        except TestbedError:
            report.check(False, "testbed did not start (see: docker compose logs)")
            continue
        verify_running(tb, cfg, report)
    # Plain `up`, not tb.up, so the user's .env settings come back.
    print("Restoring the default configuration...")
    tb.compose("up", "-d", "--wait", "--force-recreate", timeout=300)

