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
get one first. With -relay in args every link goes through the TURN relay,
so every pair should connect whatever its NATs. With -handoff (and
-direct) each pair should hop where RFC 4787 says it can, and go through
the relay everywhere else.

With break_at_s set, the named peers' NATs start dropping their direct
traffic that many seconds in (p2prunner/breaks.py): in handoff mode their
hopping links should move to the relay. The trial row then also gets
pivot_metrics.

Each peer prints one JSON row per link. They go to links.jsonl, one row per
link end: `peer` and `member` name its two ends, `outcome` and `predicted`
describe the pair (see pair_outcome and nat.predicted). The trial row gets
the mesh-level columns of mesh_metrics.
"""

from __future__ import annotations

import itertools
import json
import statistics
from dataclasses import dataclass

from p2prunner import nat
from p2prunner.breaks import HopBreak
from p2prunner.trials import fill

# What a pair of links (both ends) can come to. split: in handoff mode,
# one end hopping and the other on the relay.
OUTCOMES = ("hop", "relay", "punch", "none", "split", "error")


@dataclass(frozen=True)
class Mode:
    """How the trial's peers link: punch's -direct, -relay and -handoff."""
    direct: bool
    relayed: bool
    handoff: bool = False

    @classmethod
    def of(cls, args: str) -> "Mode":
        flags = args.split()
        return cls(direct="-direct" in flags, relayed="-relay" in flags, handoff="-handoff" in flags)

    @property
    def usable(self) -> tuple[str, ...]:
        """The outcomes that make a pair usable in this mode."""
        if self.handoff:
            return ("hop", "relay")
        return ("relay",) if self.relayed else ("hop",) if self.direct else ("punch",)

    def predicted(self, cfg, a: str, b: str) -> str:
        """Through the relay every pair connects; otherwise RFC 4787 decides,
        and in handoff mode the relay takes every pair that cannot hop."""
        if self.handoff:
            return "hop" if nat.predicted(cfg, a, b, direct=True) == "hop" else "relay"
        return "relay" if self.relayed else nat.predicted(cfg, a, b, self.direct)


def mesh(tb, cfg, params, ctx) -> dict:
    names = [p.name for p in cfg.peers]
    args = fill(str(params.get("args", "")), params, ctx)
    mode = Mode.of(args)
    if mode.direct:
        ensure_keys(tb, names)
    timeout = float(params.get("timeout_s", 120))
    commands = peer_commands(names, ctx.seed, args, float(params.get("stagger_ms", 0)) / 1000)
    jobs = {n: (lambda n=n, cmd=cmd, delay=delay: tb.sh(f"peer-{n}", cmd, timeout=timeout + delay))
            for n, (cmd, delay) in commands.items()}
    hop_break = HopBreak.of(params)
    outputs, broke_at = run_peers(tb, jobs, hop_break)
    rows = read_rows(outputs, ctx.raw)
    pairs = pair_rows(names, rows)
    ctx.links.extend(link_rows(cfg, pairs, mode))
    metrics = mesh_metrics(cfg, pairs, rows, mode)
    if hop_break:
        metrics.update(pivot_metrics(pairs, hop_break, broke_at))
    return metrics


def run_peers(tb, jobs: dict, hop_break: HopBreak | None) -> tuple[dict, float | None]:
    """Run every peer at once, and the break alongside if there is one;
    returns the peers' outputs and when the break took effect (Unix ms)."""
    if hop_break is None:
        return tb.parallel(jobs, all_at_once=True), None
    try:
        outputs = tb.parallel({**jobs, "break": lambda: hop_break.apply(tb)}, all_at_once=True)
    finally:
        hop_break.restore(tb)
    return outputs, outputs.pop("break")


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


def pair_outcome(ends: tuple, mode: Mode) -> str:
    """hop: both ends hop-connected; relay: both ready through the relay;
    punch: both punched (and, in direct mode, could not hop); none: no
    path; error: an end failed or is missing. In handoff mode each end's
    path column says which, and split means the ends disagree."""
    if any(e is None or e.get("error") for e in ends):
        return "error"
    if mode.handoff:
        return handoff_outcome(ends)
    if mode.relayed:
        return "relay" if both(ends, "ready_ms") else "none"
    if mode.direct and both(ends, "hop_connected"):
        return "hop"
    return "punch" if both(ends, "punched") else "none"


def handoff_outcome(ends: tuple) -> str:
    paths = {e.get("path") or "" for e in ends}
    if len(paths) > 1:
        return "split" if paths == {"hop", "relay"} else "none"
    path = paths.pop()
    return path if path in ("hop", "relay") and both(ends, "ready_ms") else "none"


def both(ends: tuple, column: str) -> bool:
    """The column is set (true, or a number) at both ends of the pair."""
    return all(e.get(column) is not None and e.get(column) is not False for e in ends)


def link_rows(cfg, pairs: dict, mode: Mode) -> list[dict]:
    """links.jsonl rows: each end's own row, named, with its pair's outcome
    and prediction and each end's expected NAT behaviour."""
    out = []
    for (a, b), ends in pairs.items():
        pair = {"outcome": pair_outcome(ends, mode), "predicted": mode.predicted(cfg, a, b)}
        for (me, other), row in zip(((a, b), (b, a)), ends):
            if row is not None:
                out.append({"peer": me, "member": other, **pair, **behaviour(cfg, me, "peer"),
                            **behaviour(cfg, other, "member"), **row})
    return out


def behaviour(cfg, name: str, prefix: str) -> dict:
    mapping, filtering = nat.expected_behaviour(cfg, name)
    return {f"{prefix}_mapping": mapping, f"{prefix}_filtering": filtering}


def mesh_metrics(cfg, pairs: dict, rows: dict, mode: Mode) -> dict:
    """The trial row: how much of the mesh came up, how much as RFC 4787
    predicts, and how long it took (time_to_* from the first peer's start)."""
    outcomes = {p: pair_outcome(ends, mode) for p, ends in pairs.items()}
    usable = [p for p, o in outcomes.items() if o in mode.usable]
    reported = [n for n, r in rows.items() if r]
    metrics = {
        "peers": len(rows), "peers_reported": len(reported),
        "members_min": min((r[0]["members"] for r in rows.values() if r), default=0),
        **pair_counts(cfg, outcomes, mode), "pairs_usable": len(usable),
        "time_to_usable_ms": ready_span(rows, [pairs[p] for p in usable]),
        **join_times({p: pairs[p] for p in usable}),
    }
    metrics["time_to_full_mesh_ms"] = metrics["time_to_usable_ms"] if len(usable) == len(pairs) else None
    metrics["ok"] = len(reported) == len(rows) and metrics["pairs_error"] == 0
    return metrics


def pair_counts(cfg, outcomes: dict, mode: Mode) -> dict:
    """pairs, pairs per outcome, and the pairs whose outcome RFC 4787 predicts."""
    return {
        "pairs": len(outcomes),
        **{f"pairs_{o}": sum(v == o for v in outcomes.values()) for o in OUTCOMES},
        "pairs_as_predicted": sum(o == mode.predicted(cfg, a, b) for (a, b), o in outcomes.items()),
    }


def pivot_metrics(pairs: dict, hop_break: HopBreak, broke_at: float) -> dict:
    """How the hopping pairs with a broken end moved to the relay:
    pairs_to_pivot of them, pairs_pivoted (both ends; counting any pair),
    pivots_unexpected (link ends that moved on a pair without a broken
    end; should be 0), pivot_ms_p50 / _max (from the break until the second
    end moved) and pivot_lost_pings_p50 (pings lost per link end while
    moving)."""
    hopping = {p: ends for p, ends in pairs.items() if all(e and e.get("path") == "hop" for e in ends)}
    pivoted = {p: ends for p, ends in hopping.items() if both(ends, "pivot_ms")}
    unexpected = sum(bool(e.get("pivoted")) for p, ends in hopping.items() if not hop_break.touches(*p) for e in ends)
    times = sorted(max(e["start_unix_ms"] + e["pivot_ms"] for e in ends) - broke_at for ends in pivoted.values())
    lost = sorted(e["pings_sent"] - e["pings_recv"] for ends in pivoted.values() for e in ends)
    return {
        "break_unix_ms": round(broke_at, 3),
        "pairs_to_pivot": sum(hop_break.touches(*p) for p in hopping),
        "pairs_pivoted": len(pivoted),
        "pivots_unexpected": unexpected,
        "pivot_ms_p50": round(statistics.median(times), 3) if times else None,
        "pivot_ms_max": round(times[-1], 3) if times else None,
        "pivot_lost_pings_p50": statistics.median(lost) if lost else None,
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
