"""
Reading result folders for the reports (standard library only, so the
reports run without the analysis dependencies).

A configuration is identified across experiments by the hash part of its
config_id ("a905c1" in "c00-a905c1"): the same settings give the same hash
in any experiment, so runs of different experiments line up.
"""

from __future__ import annotations

import statistics
from pathlib import Path

from p2prunner.store import ResultStore


def config_key(row: dict) -> str:
    return row["config_id"].rsplit("-", 1)[-1]


def load_runs(paths: list[str | Path]) -> list[dict]:
    """Trials of the first run, with each later run replacing the trials of
    the configurations it contains (for reruns of some configurations)."""
    rows = load_run(paths[0])
    for path in paths[1:]:
        rerun = load_run(path)
        replaced = {config_key(r) for r in rerun}
        rows = [r for r in rows if config_key(r) not in replaced] + rerun
    return rows


def load_run(path: str | Path) -> list[dict]:
    store = ResultStore(Path(path))
    if not store.trials_path.exists():
        raise FileNotFoundError(f"no trials.jsonl in {path}")
    return store.rows()


def by_config(rows: list[dict]) -> dict[str, list[dict]]:
    """Trials grouped by configuration key, in first-seen order."""
    groups: dict[str, list[dict]] = {}
    for r in rows:
        groups.setdefault(config_key(r), []).append(r)
    return groups


def label(row: dict) -> str:
    if row.get("nat_a"):
        parts = [f"{row['nat_a']}/{row['nat_b']}", f"b:{row.get('upstream_b', 'wan')}"]
    else:
        parts = [str(row.get("pairing", row["config_id"]))]
    if row.get("net_profile"):
        parts.append(row["net_profile"])
    return " ".join(parts)


def both(row: dict, column: str) -> bool:
    """True when the a_ and b_ columns are both true; missing counts as false."""
    return bool(row.get(f"a_{column}")) and bool(row.get(f"b_{column}"))


def number(row: dict, column: str) -> float | None:
    v = row.get(column)
    return float(v) if isinstance(v, (int, float)) and not isinstance(v, bool) else None


def side_values(rows: list[dict], column: str) -> list[float]:
    """Every a_<column> and b_<column> number in rows."""
    return [v for r in rows for side in "ab" if (v := number(r, f"{side}_{column}")) is not None]


def median(values: list[float]) -> float | None:
    return statistics.median(values) if values else None


def share(flags: list[bool]) -> float:
    return sum(flags) / len(flags) if flags else 0.0


def fmt(v: float | None) -> str:
    if v is None:
        return "-"
    return f"{v:.3f}" if abs(v) < 1 else f"{v:.2f}"


def print_table(header: list[str], rows: list[list[str]], footer: list[str] | None = None) -> None:
    """First column left-aligned, the rest right-aligned."""
    everything = [header] + rows + ([footer] if footer else [])
    widths = [max(len(r[i]) for r in everything) for i in range(len(header))]

    def line(r: list[str]) -> str:
        return "  ".join(c.ljust(w) if i == 0 else c.rjust(w) for i, (c, w) in enumerate(zip(r, widths)))

    print(line(header))
    for r in rows:
        print(line(r))
    if footer:
        print("-" * len(line(header)))
        print(line(footer))
