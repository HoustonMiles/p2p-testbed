"""
Trial types: the measurement a single trial performs.

Each trial function is called as

    metrics = trial_fn(tb, cfg, params, ctx)

  tb      the Testbed (to run commands in containers)
  cfg     the Config being tested (cfg.factors has the NAT types etc.)
  params  the [params] table from the experiment file
  ctx     TrialContext: this trial's number and seed, plus a `raw` dict for
          anything worth keeping in full (saved to raw/<config>/<trial>.json)

It returns a flat dict of metrics. The key "ok" (True/False) says whether
the trial succeeded; any other keys become columns in the results.

Available types:
  nat_verify  RFC 5780 mapping/filtering check from both peers
  ping        round-trip time from each peer to a WAN host, through NAT + shaping
  command     run your own command on peer-a and/or peer-b (the hook for the
              Go client); each prints one JSON line of results at the end
"""

from __future__ import annotations

import json
import re
import statistics
from dataclasses import dataclass, field
from string import Template

STUN_IP = "203.0.113.100"


@dataclass
class TrialContext:
    trial: int                    # trial number within its configuration (0-based)
    seed: int                     # random seed for this trial
    raw: dict = field(default_factory=dict)


# =============================================================================
# Shared helpers
# =============================================================================
def percentile(sorted_values: list[float], p: float) -> float | None:
    """p-th percentile (0-100) with linear interpolation, like numpy's default."""
    if not sorted_values:
        return None
    k = (len(sorted_values) - 1) * p / 100
    lo, hi = int(k), min(int(k) + 1, len(sorted_values) - 1)
    return sorted_values[lo] + (sorted_values[hi] - sorted_values[lo]) * (k - lo)


def summarize(samples: list[float], prefix: str) -> dict:
    """Mean / median / p95 / min / max / standard deviation of a list of samples."""
    s = sorted(samples)
    return {
        f"{prefix}_mean": statistics.fmean(s) if s else None,
        f"{prefix}_p50": percentile(s, 50),
        f"{prefix}_p95": percentile(s, 95),
        f"{prefix}_min": s[0] if s else None,
        f"{prefix}_max": s[-1] if s else None,
        # Standard deviation of RTT: a simple, commonly used jitter measure.
        f"{prefix}_stdev": statistics.stdev(s) if len(s) > 1 else None,
    }


# =============================================================================
# nat_verify: RFC 5780 behaviour discovery (also used by `p2prunner verify`)
# =============================================================================
# What each NAT type should measure as (RFC 4787 names):
#   EI = endpoint-independent, AD = address-dependent,
#   APD = address-and-port-dependent
EXPECTED = {
    "full_cone":       ("EI", "EI"),
    "restricted":      ("EI", "AD"),
    "port_restricted": ("EI", "APD"),
    "symmetric":       ("APD", "APD"),
}
RANK = {"EI": 0, "AD": 1, "APD": 2}
WORDS = {"Endpoint Independent": "EI", "Address Dependent": "AD",
         "Address and Port Dependent": "APD"}


def expected_behaviour(cfg, side: str) -> tuple[str, str]:
    """Expected (mapping, filtering) for one side. Behind CGNAT, the pair of
    NATs behaves like the stricter of the two, separately for each."""
    m, f = EXPECTED[cfg.factors[f"nat_{side}"]]
    if cfg.factors[f"upstream_{side}"] == "cgnat":
        cm, cf = EXPECTED[cfg.factors[f"cgn_{side}"]]
        m = max(m, cm, key=RANK.get)
        f = max(f, cf, key=RANK.get)
    return m, f


def measure_behaviour(tb, side: str, raw: dict) -> tuple[str, str]:
    result = tb.exec(f"peer-{side}", ["timeout", "60", "turnutils_natdiscovery",
                                      "-m", "-f", STUN_IP], timeout=90)
    raw[f"natdiscovery_{side}"] = result.stdout[-4000:]
    m = re.search(r"^NAT with (.*) Mapping!", result.stdout, re.M)
    f = re.search(r"^NAT with (.*) Filtering!", result.stdout, re.M)
    return (WORDS.get(m.group(1), "?") if m else "none",
            WORDS.get(f.group(1), "?") if f else "none")


def nat_verify(tb, cfg, params, ctx) -> dict:
    measured = tb.parallel({s: (lambda s=s: measure_behaviour(tb, s, ctx.raw)) for s in "ab"})
    metrics, ok = {}, True
    for side in "ab":
        exp_m, exp_f = expected_behaviour(cfg, side)
        got_m, got_f = measured[side]
        metrics.update({
            f"mapping_{side}": got_m, f"filtering_{side}": got_f,
            f"expected_mapping_{side}": exp_m, f"expected_filtering_{side}": exp_f,
        })
        ok &= (got_m, got_f) == (exp_m, exp_f)
    metrics["ok"] = ok
    return metrics


# =============================================================================
# ping: RTT from one peer to a WAN host (default: the STUN server)
# =============================================================================
PING_LINE = re.compile(r"icmp_seq=(\d+).*?time=([\d.]+) ms")


@dataclass
class PingPlan:
    target: str
    count: int
    interval: float
    warmup: int  # the first replies, discarded (e.g. ARP lookups)

    @classmethod
    def from_params(cls, params: dict) -> "PingPlan":
        return cls(target=params.get("target", STUN_IP), count=int(params.get("count", 50)),
                   interval=float(params.get("interval_s", 0.2)), warmup=int(params.get("warmup", 5)))


def ping_once(tb, source: str, plan: PingPlan, raw: dict) -> dict:
    result = tb.exec(source, ["ping", "-n", "-c", str(plan.count), "-i", str(plan.interval),
                              "-W", "2", plan.target], timeout=plan.count * plan.interval + 30)
    replies = {int(seq): float(ms) for seq, ms in PING_LINE.findall(result.stdout)}
    raw[f"rtt_ms_{source}"] = [replies[s] for s in sorted(replies)]
    samples = [replies[s] for s in sorted(replies) if s > plan.warmup]
    sent_after_warmup = max(plan.count - plan.warmup, 0)
    return {
        "pings_received": len(replies),
        "loss": 1 - len(samples) / sent_after_warmup if sent_after_warmup else None,
        **summarize(samples, "rtt_ms"),
    }


def ping(tb, cfg, params, ctx) -> dict:
    """Measures each side's access path: home NAT (+ CGN) + latency shaping.

    Both peers ping at the same time, so result columns are prefixed with
    a_ and b_ (e.g. a_rtt_ms_mean). Each home router delays only its own
    outgoing traffic, so the expected RTT to a WAN host is HALF the
    profile's peer-to-peer RTT (local 5 ms, coast 35 ms, transatlantic 75 ms).

    params (all optional):
      sources    containers to ping from       (default ["peer-a", "peer-b"])
      target     IP to ping                    (default the STUN server)
      count      number of pings               (default 50)
      interval_s seconds between pings         (default 0.2)
      warmup     first N replies to discard    (default 5; e.g. ARP lookups)
    """
    sources = params.get("sources", ["peer-a", "peer-b"])
    plan = PingPlan.from_params(params)
    results = tb.parallel({src: (lambda src=src: ping_once(tb, src, plan, ctx.raw)) for src in sources})
    metrics = {"target": plan.target, "pings_sent": plan.count, "warmup": plan.warmup}
    for src, res in results.items():
        side = src.removeprefix("peer-")          # "peer-a" -> "a"
        metrics.update({f"{side}_{k}": v for k, v in res.items()})
    metrics["ok"] = all(res["rtt_ms_mean"] is not None for res in results.values())
    return metrics


# =============================================================================
# command: run your own program on the peers (hook for the Go client)
# =============================================================================
def command(tb, cfg, params, ctx) -> dict:
    """Runs cmd_a on peer-a and cmd_b on peer-b at the same time.

    params:
      cmd_a, cmd_b   shell commands (either may be omitted). These
                     placeholders are filled in first:
                       $seed   this trial's random seed
                       $trial  trial number within the configuration
                       $<any other param name>, e.g. $hop_interval_ms
      timeout_s      give up after this many seconds (default 60)

    Contract: each command prints ONE JSON object on its last line of output.
    Its keys become result columns, prefixed with a_ or b_. The trial is ok
    if every command exits with status 0 and prints valid JSON.
    """
    results = tb.parallel(command_jobs(tb, params, ctx))
    metrics, ok = {}, True
    for side, res in results.items():
        side_ok = read_side(side, res, metrics, ctx.raw)
        ok = ok and side_ok
    metrics["ok"] = ok
    return metrics


def command_jobs(tb, params: dict, ctx: TrialContext) -> dict:
    """One job per side that has a command, with placeholders filled in."""
    timeout = float(params.get("timeout_s", 60))
    values = {k: v for k, v in params.items() if not isinstance(v, (dict, list))}
    values.update(seed=ctx.seed, trial=ctx.trial)
    jobs = {}
    for side in "ab":
        if template := params.get(f"cmd_{side}"):
            cmd = Template(template).safe_substitute(values)
            jobs[side] = (lambda side=side, cmd=cmd: tb.sh(f"peer-{side}", cmd, timeout=timeout))
    if not jobs:
        raise ValueError("command trial needs cmd_a and/or cmd_b in [params]")
    return jobs


def read_side(side: str, res, metrics: dict, raw: dict) -> bool:
    """Adds one side's JSON result to metrics (lists and objects go to raw,
    keeping the table flat). Returns whether that side succeeded."""
    raw[f"stdout_{side}"] = res.stdout[-20000:]
    raw[f"stderr_{side}"] = res.stderr[-5000:]
    metrics[f"{side}_exit"] = res.returncode
    data = last_json(res.stdout)
    if data is None:
        metrics[f"{side}_error"] = "no JSON object on the last line of output"
        return False
    for key, value in data.items():
        target = raw if isinstance(value, (list, dict)) else metrics
        target[f"{side}_{key}"] = value
    return res.returncode == 0


def last_json(stdout: str) -> dict | None:
    lines = [line for line in stdout.splitlines() if line.strip()]
    try:
        data = json.loads(lines[-1]) if lines else None
    except json.JSONDecodeError:
        return None
    return data if isinstance(data, dict) else None


TRIALS = {"nat_verify": nat_verify, "ping": ping, "command": command}

