"""Compare two experiment runs, configuration by configuration.

    python3 scripts/compare_runs.py BASE NEW [NEW_RERUN ...]

BASE is the raw UDP baseline, NEW the direct channel, e.g.

    # Experiment B: what do Noise and port-hopping cost?
    python3 scripts/compare_runs.py results/punch_path/<run> results/direct_path/<run>

    # Does hopping add ping loss? (hop_matrix plus its one-way rerun)
    python3 scripts/compare_runs.py results/punch_matrix/<run> \\
        results/hop_matrix/<run> results/hop_matrix_oneway/<run>

Configurations are matched by the hash part of config_id ("a905c1" in
"c00-a905c1"), which is the same for the same settings in any experiment.
Extra NEW runs replace NEW's trials for the configurations they contain.

Only trials where the path was up count: both sides punched, and for the
direct channel also hop_connected on both sides. Each cell is the median
over both sides' values in those trials, shown as BASE > NEW. The last line
pools every configuration: the median of the per-configuration changes.

Standard library only (no numpy/pandas).
"""

import json
import statistics
import sys
from pathlib import Path

# column, short name, unit
METRICS = [
    ("rtt_ms_p50", "rtt_p50", "ms"),
    ("rtt_ms_p95", "rtt_p95", "ms"),
    ("rtt_ms_stdev", "jitter", "ms"),
    ("ping_loss", "ping_loss", ""),
    ("rx_mbit", "rx_mbit", ""),
    ("rx_ip_mbit", "rx_ip_mbit", ""),
    ("rx_loss", "rx_loss", ""),
    ("punch_ms", "punch", "ms"),
    ("handshake_ms", "handshake", "ms"),
    ("hop_drain_ms_avg", "drain_avg", "ms"),
    ("hop_drain_cutoffs", "cutoffs", ""),
    ("hop_late_opens", "late_opens", ""),
]


def load(run_dir: str) -> list[dict]:
    path = Path(run_dir) / "trials.jsonl"
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    for r in rows:
        r["key"] = r["config_id"].rsplit("-", 1)[-1]
    return rows


def merge(paths: list[str]) -> list[dict]:
    rows = load(paths[0])
    for p in paths[1:]:
        rerun = load(p)
        keys = {r["key"] for r in rerun}
        rows = [r for r in rows if r["key"] not in keys] + rerun
    return rows


def path_up(r: dict) -> bool:
    if not (r.get("ok") and r.get("a_punched") and r.get("b_punched")):
        return False
    if r.get("a_direct") or r.get("b_direct"):
        return bool(r.get("a_hop_connected") and r.get("b_hop_connected"))
    return True


def values(rows: list[dict], col: str) -> list[float]:
    out = []
    for r in rows:
        for side in "ab":
            v = r.get(f"{side}_{col}")
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                out.append(float(v))
    return out


def label(r: dict) -> str:
    if r.get("nat_a"):
        parts = [f"{r['nat_a']}/{r['nat_b']}", f"b:{r.get('upstream_b', 'wan')}"]
    else:
        parts = [str(r.get("pairing", "?"))]
    if r.get("net_profile"):
        parts.append(r["net_profile"])
    return " ".join(parts)


def fmt(v) -> str:
    if v is None:
        return "-"
    return f"{v:.3f}" if abs(v) < 1 else f"{v:.2f}"


def main(base_path: str, new_paths: list[str]) -> None:
    base, new = load(base_path), merge(new_paths)
    by_key = lambda rows: {k: [r for r in rows if r["key"] == k] for k in dict.fromkeys(r["key"] for r in rows)}
    base_g, new_g = by_key(base), by_key(new)
    common = [k for k in base_g if k in new_g]
    if not common:
        sys.exit("no configurations in common (are these runs of the same settings?)")

    # Keep only metrics that have values in both runs somewhere.
    metrics = [m for m in METRICS
               if values([r for r in base if path_up(r)], m[0]) or values([r for r in new if path_up(r)], m[0])]

    header = ["config", "n"] + [m[1] for m in metrics]
    table, deltas = [], {m[0]: [] for m in metrics}
    for k in common:
        b_up = [r for r in base_g[k] if path_up(r)]
        n_up = [r for r in new_g[k] if path_up(r)]
        row = [label(base_g[k][0]), f"{len(b_up)}>{len(n_up)}"]
        for col, _, _ in metrics:
            bv, nv = values(b_up, col), values(n_up, col)
            bm = statistics.median(bv) if bv else None
            nm = statistics.median(nv) if nv else None
            if bm is not None and nm is not None:
                deltas[col].append(nm - bm)
            row.append(f"{fmt(bm)} > {fmt(nm)}")
        table.append(row)

    pooled = ["all: median change", ""]
    for col, _, unit in metrics:
        d = deltas[col]
        pooled.append(f"{'+' if d and statistics.median(d) >= 0 else ''}{fmt(statistics.median(d))} {unit}".strip() if d else "-")

    rows = [header] + table + [pooled]
    widths = [max(len(r[i]) for r in rows) for i in range(len(header))]
    line = lambda r: "  ".join(c.ljust(w) if i == 0 else c.rjust(w) for i, (c, w) in enumerate(zip(r, widths)))
    print(line(header))
    for r in table:
        print(line(r))
    print("-" * len(line(header)))
    print(line(pooled))
    print("\nn = trials with the path up (BASE > NEW); cells are medians over both sides, BASE > NEW.")

    skipped = [k for k in base_g if k not in new_g]
    if skipped:
        print(f"{len(skipped)} BASE configurations not in NEW (skipped).")


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2:])

