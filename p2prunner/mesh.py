"""
The mesh trial: every peer of the configuration runs punch in one room and
links with every other peer (a full mesh).

    trial = "mesh"
    [params]
    args = "-direct -hop-interval 1s -pings 20 -rate-mbit 0"   # punch flags for every peer
    stagger_ms = 0                                            # default 0
    timeout_s = 120                                           # default 120

stagger_ms spaces the starts: peer a starts first, b stagger_ms later, c
after another stagger_ms, and so on, each joining the mesh the earlier ones
are already running. 0 starts everyone at the same moment: the heaviest
load a mesh sees while it forms. The peers wait for the last one at the
rendezvous for as long as the spacing needs (-rendezvous-timeout, which
args can override).

$seed, $trial and $<param> in args are filled in as for the command trial;
the trial seed is the room. In direct mode, peers without a key in ./keys
get one first.

Each peer prints one JSON row per link. They go to links.jsonl, one row per
link end: `peer` and `member` name its two ends, `outcome` and `predicted`
describe the pair (see pair_outcome and nat.predicted). The trial row gets
the mesh-level columns of mesh_metrics.
"""

from __future__ import annotations

import itertools
import json
import statistics

from p2prunner import nat
from p2prunner.trials import fill

# What a pair of links (both ends) can come to, from best to worst.
OUTCOMES = ("hop", "punch", "none", "error")


def mesh(tb, cfg, params, ctx) -> dict:
    names = [p.name for p in cfg.peers]
    args = fill(str(params.get("args", "")), params, ctx)
    direct = "-direct" in args.split()
    if direct:
        ensure_keys(tb, names)
    timeout = float(params.get("timeout_s", 120))
    commands = peer_commands(names, ctx.seed, args, float(params.get("stagger_ms", 0)) / 1000)
    jobs = {n: (lambda n=n, cmd=cmd, delay=delay: tb.sh(f"peer-{n}", cmd, timeout=timeout + delay))
            for n, (cmd, delay) in commands.items()}
    rows = read_rows(tb.parallel(jobs, all_at_once=True), ctx.raw)
    pairs = pair_rows(names, rows)
    ctx.links.extend(link_rows(cfg, pairs, direct))
    return mesh_metrics(cfg, pairs, rows, direct)


def peer_commands(names: list[str], room: int, args: str, stagger_s: float) -> dict[str, tuple[str, float]]:
    """Each peer's shell command and how long it waits before starting."""
    wait = 15 + stagger_s * (len(names) - 1)  # the first peer waits for the last at the rendezvous
    commands = {}
    for k, name in enumerate(names):
        cmd = f"punch -name {name} -room {room} -peers {len(names)} -quiet -rendezvous-timeout {wait:g}s {args}"
        delay = k * stagger_s
        commands[name] = (f"sleep {delay:.3f}; {cmd}" if delay else cmd, delay)
    return commands


def ensure_keys(tb, names: list[str]) -> None:
    """Static keys for the peers that have none in ./keys (shared by every
    peer at /keys), made before any peer starts and reads the directory."""
    for name in names:
        if not (tb.root / "keys" / f"{name}.key").exists():
            tb.exec(f"peer-{name}", ["punch", "-genkey", f"/keys/{name}"])


def read_rows(outputs: dict, raw: dict) -> dict[str, list[dict]]:
    """Each peer's link rows: every JSON object it printed, one per line."""
    rows = {}
    for name, res in outputs.items():
        raw[f"stderr_{name}"] = res.stderr[-5000:]
        raw[f"exit_{name}"] = res.returncode
        rows[name] = [r for r in map(parse, res.stdout.splitlines()) if r is not None]
    return rows


def parse(line: str) -> dict | None:
    try:
        row = json.loads(line)
    except json.JSONDecodeError:
        return None
    return row if isinstance(row, dict) else None


def pair_rows(names: list[str], rows: dict[str, list[dict]]) -> dict[tuple[str, str], tuple]:
    """For each pair (a, b) of peers, a's row for b and b's row for a; None
    for an end that printed none."""
    name_of = {r[0]["id"]: n for n, r in rows.items() if r}
    ends = {(n, name_of.get(r["peer_id"])): r for n, rs in rows.items() for r in rs}
    return {(a, b): (ends.get((a, b)), ends.get((b, a))) for a, b in itertools.combinations(names, 2)}


def pair_outcome(ends: tuple, direct: bool) -> str:
    """hop: both ends hop-connected; punch: both punched (and, in direct
    mode, could not hop); none: no path; error: an end failed or is missing."""
    if any(e is None or e.get("error") for e in ends):
        return "error"
    if direct and all(e.get("hop_connected") for e in ends):
        return "hop"
    return "punch" if all(e.get("punched") for e in ends) else "none"


def link_rows(cfg, pairs: dict, direct: bool) -> list[dict]:
    """links.jsonl rows: each end's own row, named, with its pair's outcome
    and prediction and each end's expected NAT behaviour."""
    out = []
    for (a, b), ends in pairs.items():
        pair = {"outcome": pair_outcome(ends, direct), "predicted": nat.predicted(cfg, a, b, direct)}
        for (me, other), row in zip(((a, b), (b, a)), ends):
            if row is not None:
                out.append({"peer": me, "member": other, **pair, **behaviour(cfg, me, "peer"),
                            **behaviour(cfg, other, "member"), **row})
    return out


def behaviour(cfg, name: str, prefix: str) -> dict:
    mapping, filtering = nat.expected_behaviour(cfg, name)
    return {f"{prefix}_mapping": mapping, f"{prefix}_filtering": filtering}


def mesh_metrics(cfg, pairs: dict, rows: dict, direct: bool) -> dict:
    """The trial row: how much of the mesh came up, how much as RFC 4787
    predicts, and how long it took (time_to_* from the first peer's start)."""
    outcomes = {p: pair_outcome(ends, direct) for p, ends in pairs.items()}
    usable = [p for p, o in outcomes.items() if o == ("hop" if direct else "punch")]
    reported = [n for n, r in rows.items() if r]
    metrics = {
        "peers": len(rows), "peers_reported": len(reported),
        "members_min": min((r[0]["members"] for r in rows.values() if r), default=0),
        **pair_counts(cfg, outcomes, direct), "pairs_usable": len(usable),
        "time_to_usable_ms": ready_span(rows, [pairs[p] for p in usable]),
        **join_times({p: pairs[p] for p in usable}),
    }
    metrics["time_to_full_mesh_ms"] = metrics["time_to_usable_ms"] if len(usable) == len(pairs) else None
    metrics["ok"] = len(reported) == len(rows) and metrics["pairs_error"] == 0
    return metrics


def pair_counts(cfg, outcomes: dict, direct: bool) -> dict:
    """pairs, pairs per outcome, and the pairs whose outcome RFC 4787 predicts."""
    return {
        "pairs": len(outcomes),
        **{f"pairs_{o}": sum(v == o for v in outcomes.values()) for o in OUTCOMES},
        "pairs_as_predicted": sum(o == nat.predicted(cfg, a, b, direct) for (a, b), o in outcomes.items()),
    }


def ready_span(rows: dict, ends: list[tuple]) -> float | None:
    """From the first peer's start until the last of these pairs was ready
    at both ends (every peer's start_unix_ms puts them on one clock)."""
    if not ends:
        return None
    first = min(r[0]["start_unix_ms"] for r in rows.values() if r)
    last = max(e["start_unix_ms"] + e["ready_ms"] for pair in ends for e in pair)
    return round(last - first, 3)


def join_times(usable: dict) -> dict:
    """How long each peer took to join the members already running: from
    its start until its usable pairs with the peers that started before it
    were ready at both ends. The first peer to start has none."""
    joins: dict[str, float] = {}
    for (a, b), ends in usable.items():
        start = {a: ends[0]["start_unix_ms"], b: ends[1]["start_unix_ms"]}
        newer = max(start, key=start.get)
        ready = max(e["start_unix_ms"] + e["ready_ms"] for e in ends)
        joins[newer] = max(joins.get(newer, 0.0), ready - start[newer])
    times = sorted(joins.values())
    return {"join_ms_p50": round(statistics.median(times), 3) if times else None,
            "join_ms_max": round(times[-1], 3) if times else None}
