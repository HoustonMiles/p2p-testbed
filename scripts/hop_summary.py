"""Experiment A summary: did port-hopping hold, per NAT pairing?

Merges a hop_matrix run with reruns (e.g. hop_matrix_oneway): a rerun's
trials replace the original run's trials for the same configuration
(matched by the hash part of config_id, e.g. "b54dac" in "c06-b54dac").

    python3 scripts/hop_summary.py results/hop_matrix/<run> [results/<rerun>/<run> ...]

Standard library only, so it runs with any Python 3.9+ (no numpy/pandas).

Per configuration, the share of trials where:
    ok         the tools ran without error
    punched    both sides punched
    possible   both NATs keep the local port (hopping expected to work)
    connected  the first hop worked both ways, on both sides
    clean      connected, and no hop missed on either side
plus the median hops OK per side, ping loss and RTT over the hopping channel.
Lines marked "!" disagree with the expectation: connected == possible.
"""

import json
import statistics
import sys
from pathlib import Path


def load(run_dir: str) -> list[dict]:
    path = Path(run_dir) / "trials.jsonl"
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    for r in rows:
        r["key"] = r["config_id"].rsplit("-", 1)[-1]
    return rows


def label(r: dict) -> str:
    if r.get("nat_a"):
        return f"{r['nat_a']:>15} / {r['nat_b']:<15} b:{r.get('upstream_b', 'wan')}"
    return str(r.get("pairing", r["config_id"]))


def both(r: dict, name: str) -> bool:
    """True when the a_ and b_ columns are both true (missing = false)."""
    return bool(r.get(f"a_{name}")) and bool(r.get(f"b_{name}"))


def num(r: dict, name: str):
    v = r.get(name)
    return v if isinstance(v, (int, float)) and not isinstance(v, bool) else None


def median(values) -> str:
    values = [v for v in values if v is not None]
    return f"{statistics.median(values):.2f}" if values else "-"


def share(flags: list[bool]) -> str:
    return f"{sum(flags) / len(flags):.2f}"


def main(paths: list[str]) -> None:
    runs = [load(p) for p in paths]
    merged = runs[0]
    for rerun in runs[1:]:
        replaced = {r["key"] for r in rerun}
        merged = [r for r in merged if r["key"] not in replaced] + rerun

    groups: dict[str, list[dict]] = {}
    for r in merged:
        groups.setdefault(r["key"], []).append(r)

    header = ["config", "n", "ok", "punched", "possible", "connected", "clean",
              "hops_ok_a", "hops_ok_b", "ping_loss", "rtt_p50", "!"]
    table = []
    for g in groups.values():
        connected = [both(r, "hop_connected") for r in g]
        possible = [both(r, "hop_possible") for r in g]
        clean = [c and (num(r, "a_hop_hops_missed") or 0) + (num(r, "b_hop_hops_missed") or 0) == 0
                 for c, r in zip(connected, g)]
        table.append([
            label(g[0]),
            str(len(g)),
            share([bool(r.get("ok")) for r in g]),
            share([both(r, "punched") for r in g]),
            share(possible),
            share(connected),
            share(clean),
            median(num(r, "a_hop_hops_ok") for r in g),
            median(num(r, "b_hop_hops_ok") for r in g),
            median([num(r, "a_ping_loss") for r in g] + [num(r, "b_ping_loss") for r in g]),
            median([num(r, "a_rtt_ms_p50") for r in g] + [num(r, "b_rtt_ms_p50") for r in g]),
            "!" if connected != possible else "",
        ])
    table.sort(key=lambda row: row[0])

    widths = [max(len(row[i]) for row in [header] + table) for i in range(len(header))]
    for row in [header] + table:
        print("  ".join(cell.rjust(w) if i else cell.ljust(w)
                        for i, (cell, w) in enumerate(zip(row, widths))))

    bad = [r for r in merged if not r.get("ok")]
    if bad:
        print(f"\n{len(bad)} trials not ok; errors:")
        for err in sorted({(r["config_id"], r.get("a_error") or "", r.get("b_error") or "") for r in bad}):
            print("  ", *err)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    main(sys.argv[1:])

