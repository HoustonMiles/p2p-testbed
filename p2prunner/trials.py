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
  nat_verify  RFC 5780 mapping/filtering check from every peer
  ping        round-trip time from each peer to a WAN host, through NAT + shaping
  command     run your own command on peer-a and/or peer-b (the hook for the
              Go client); each prints one JSON line of results at the end
  mesh        punch on every peer at once, one full mesh (see mesh.py)
  flood       run the peers' throughput while the attacker floods the
              victim's address; score the victim's goodput (Experiment C)
"""

from __future__ import annotations

import json
import re
import statistics
from dataclasses import dataclass, field
from string import Template

from p2prunner.nat import expected_behaviour
from p2prunner.topology import STUN_IP


@dataclass
class TrialContext:
    trial: int                    # trial number within its configuration (0-based)
    seed: int                     # random seed for this trial
    raw: dict = field(default_factory=dict)
    links: list = field(default_factory=list)  # rows for links.jsonl (mesh trials)


def fill(template: str, params: dict, ctx: TrialContext) -> str:
    """A command template with $seed, $trial and $<param> filled in."""
    values = {k: v for k, v in params.items() if not isinstance(v, (dict, list))}
    values.update(seed=ctx.seed, trial=ctx.trial)
    return Template(template).safe_substitute(values)


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
# natdiscovery's words for the RFC 4787 behaviours (see nat.py)
WORDS = {"Endpoint Independent": "EI", "Address Dependent": "AD",
         "Address and Port Dependent": "APD"}


def measure_behaviour(tb, side: str, raw: dict) -> tuple[str, str]:
    result = tb.exec(f"peer-{side}", ["timeout", "60", "turnutils_natdiscovery",
                                      "-m", "-f", STUN_IP], timeout=90)
    raw[f"natdiscovery_{side}"] = result.stdout[-4000:]
    m = re.search(r"^NAT with (.*) Mapping!", result.stdout, re.M)
    f = re.search(r"^NAT with (.*) Filtering!", result.stdout, re.M)
    return (WORDS.get(m.group(1), "?") if m else "none",
            WORDS.get(f.group(1), "?") if f else "none")


def measure_all(tb, names: list[str], raw: dict) -> dict[str, tuple[str, str]]:
    """Every peer's behaviour, measured at once. A peer whose test got no
    reply at all ("none": a lost packet, likely with many peers testing over
    lossy links) is tested once more on its own; a wrong answer is not."""
    measured = tb.parallel({s: (lambda s=s: measure_behaviour(tb, s, raw)) for s in names})
    for side in [s for s in names if "none" in measured[s]]:
        raw[f"retried_{side}"] = measured[side]
        measured[side] = measure_behaviour(tb, side, raw)
    return measured


def nat_verify(tb, cfg, params, ctx) -> dict:
    names = [p.name for p in cfg.peers]
    measured = measure_all(tb, names, ctx.raw)
    metrics, ok = {}, True
    for side in names:
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
    def from_params(cls, params: dict) -> PingPlan:
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

    Every peer pings at the same time, so result columns are prefixed with
    its name (e.g. a_rtt_ms_mean). Each home router delays only its own
    outgoing traffic, so the expected RTT to a WAN host is HALF the
    profile's peer-to-peer RTT (local 5 ms, coast 35 ms, transatlantic 75 ms).

    params (all optional):
      sources    containers to ping from       (default every peer)
      target     IP to ping                    (default the STUN server)
      count      number of pings               (default 50)
      interval_s seconds between pings         (default 0.2)
      warmup     first N replies to discard    (default 5; e.g. ARP lookups)
    """
    sources = params.get("sources", [f"peer-{p.name}" for p in cfg.peers])
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
    jobs = {}
    for side in "ab":
        if template := params.get(f"cmd_{side}"):
            cmd = fill(template, params, ctx)
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




# =============================================================================
# flood: volumetric attack (Experiment C), direct-plain vs direct-hopping
# =============================================================================
def flood(tb, cfg, params, ctx) -> dict:
    """Runs the peers' throughput (cmd_a / cmd_b, like `command`) while the
    attacker floods the victim, then scores the victim's goodput over the
    attack window.

    Common params: victim (default a), flood_multiple (offered load as a
    multiple of the victim's downlink; 0 is the no-attack control),
    flood_at, flood_duration, timeout_s.

    attack = "volumetric" (default): flood a fixed port (flood_port). The
    flood fills the downlink before the NAT, so it does not need the right
    port; hopping cannot escape it.

    attack = "reactive": the scripted attacker, an oracle told the victim's
    active port but reacting after react_delay_x hop intervals. The victim
    writes its receive-port schedule to a shared volume and the attacker
    replays it with that delay. hop_interval_ms sets the interval. Hopping
    escapes it once react_delay_x >= 1 (the attacker is always a hop behind).
    """
    victim = str(params.get("victim", "a"))
    multiple = float(params.get("flood_multiple", 0))
    mode = str(params.get("attack", "volumetric"))
    rate = multiple * victim_downlink(cfg, victim) if multiple > 0 else 0.0
    at, dur = params.get("flood_at", "2s"), params.get("flood_duration", "20s")
    timeout = float(params.get("timeout_s", 90))

    # "reactive" delivers over the WAN (through the NAT); "inject" delivers on
    # the victim's LAN (no NAT in the path), to measure port precision alone.
    reactive = mode in ("reactive", "inject")
    target_kind = str(params.get("flood_target", "victim"))   # volumetric: "victim" or "relay"
    # "attack_target", not "flood_target": the flood result's own "target" field
    # becomes the flood_target column (the address hit), and would clobber this.
    metrics: dict = {"victim": victim, "attack": mode, "flood_multiple": multiple, "flood_mbit": round(rate, 3),
                     "attack_target": target_kind}
    if reactive and multiple > 0:
        # Drop any earlier trial's schedule from the shared volume FIRST, so the
        # attacker (which loads the file as soon as it starts) blocks for this
        # trial's fresh one instead of replaying stale ports and missing every
        # hop. Without this, only the first trial on a clean volume can land.
        tb.sh(f"peer-{victim}", f"rm -f /sched/{victim}.txt")
        params = {**params, f"cmd_{victim}": params[f"cmd_{victim}"] + f" -hop-schedule-out /sched/{victim}.txt"}
        delay_ms = float(params.get("react_delay_x", 0)) * float(params["hop_interval_ms"])
        metrics["react_delay_ms"] = round(delay_ms, 1)
    jobs = command_jobs(tb, params, ctx)
    if multiple > 0:
        if reactive:
            target = victim_lan_ip(victim) if mode == "inject" else victim_ip(cfg, victim)
            cmd = (f"flood -target-ip {target} -schedule /sched/{victim}.txt "
                   f"-react-delay {delay_ms:g}ms -mbit {rate:g} -at {at} -duration {dur}")
        elif target_kind == "relay":
            # The relay hides the victim's address, so an attacker that cannot
            # learn it floods the relay instead; coturn drops the junk on the
            # uncapped WAN and the victim's link never sees it.
            cmd = (f"flood -target {relay_target()} "
                   f"-mbit {rate:g} -at {at} -duration {dur}")
        else:
            cmd = (f"flood -target {victim_ip(cfg, victim)}:{int(params.get('flood_port', 40000))} "
                   f"-mbit {rate:g} -at {at} -duration {dur}")
        jobs["flood"] = lambda cmd=cmd: tb.sh("attacker", cmd, timeout=timeout)

    results = tb.parallel(jobs, all_at_once=True)
    ok = True
    for side in "ab":
        if side in results:
            ok = read_side(side, results[side], metrics, ctx.raw) and ok
    if "flood" in results:
        ctx.raw["stdout_flood"] = results["flood"].stdout[-5000:]
        if fj := last_json(results["flood"].stdout):
            metrics.update({f"flood_{k}": v for k, v in fj.items()})
        else:
            ok, metrics["flood_error"] = False, "no JSON from the attacker"
    metrics.update(attack_goodput(victim, metrics, ctx.raw))
    if mode == "reactive" and multiple > 0:   # inject bypasses the NAT, so its state is moot
        metrics.update(victim_nat_state(tb, victim))
    metrics["ok"] = ok
    return metrics


def victim_nat_state(tb, victim: str) -> dict:
    """The victim router's cone-filter state after a reactive run, to
    localize where the attacker's packets died: how many ports its full-cone
    set currently maps (victim_mapped_ports), and how many inbound packets
    its cone DNAT rule forwarded over the trial (victim_dnat_pkts). Zero
    forwarded with the hop ports mapped means the rate cap dropped them;
    forwarded but not counted at the peer means a counting gap; no ports
    mapped means the hopper's outbound did not register. Best-effort: a
    router without these (port-restricted) simply yields no columns."""
    out: dict = {}
    try:
        r = tb.sh(f"nat-{victim}",
                  "ipset list tb_mapped 2>/dev/null | sed -n '/Members:/,$p' | tail -n +2 | wc -l")
        if r.returncode == 0 and r.stdout.strip().isdigit():
            out["victim_mapped_ports"] = int(r.stdout.strip())
        r = tb.sh(f"nat-{victim}",
                  "iptables -t nat -L TB_PRE -v -n -x 2>/dev/null | awk '/DNAT/{s+=$1} END{print s+0}'")
        if r.returncode == 0 and r.stdout.strip().isdigit():
            out["victim_dnat_pkts"] = int(r.stdout.strip())
    except Exception:
        pass   # a diagnostic read must never fail the trial
    return out


def victim_downlink(cfg, victim: str) -> float:
    """The victim's downlink cap in Mbit/s (per-peer, else testbed-wide). A
    volumetric flood needs one, so an unset cap is an error."""
    for key in (f"downlink_mbit_{victim}", "downlink_mbit"):
        if (v := str(cfg.factors.get(key, "")).strip()):
            return float(v)
    raise ValueError("flood trial needs a downlink cap (set downlink_mbit)")


def victim_ip(cfg, victim: str) -> str:
    from p2prunner import topology
    return topology.peer(victim).public_ip(victim in cfg.cgn_peers)


def victim_lan_ip(victim: str) -> str:
    """The victim's host address on its own LAN, where a co-resident attacker
    (attack=inject) reaches it with no NAT in the path."""
    from p2prunner import topology
    return topology.peer(victim).peer_ip


def relay_target() -> str:
    """The TURN relay's public address and port: what an attacker floods when
    the relay hides the victim's own address (flood_target=relay)."""
    from p2prunner import topology
    return f"{topology.RELAY_IP}:3478"


def attack_goodput(victim: str, metrics: dict, raw: dict) -> dict:
    """The victim's receive goodput before / during / after the attack window,
    and recovery time, from its rx series and the flood's recorded window."""
    from p2prunner import goodput
    start, end = metrics.get("flood_start_unix_ms"), metrics.get("flood_end_unix_ms")
    if start is None or end is None:
        return {}
    row = {"start_unix_ms": metrics.get(f"{victim}_start_unix_ms"),
           "series_ms": metrics.get(f"{victim}_series_ms"),
           "rx_series_start_ms": metrics.get(f"{victim}_rx_series_start_ms"),
           "rx_series_mbit": raw.get(f"{victim}_rx_series_mbit")}
    points = goodput.timeline(row, "rx")
    if not points:
        return {}
    out = goodput.under_attack(points, row["series_ms"], start, end)
    return {f"gp_{k}": (round(v, 3) if isinstance(v, float) else v) for k, v in out.items()}
