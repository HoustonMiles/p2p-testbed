"""
Goodput over time, for Experiment C: what a link kept during an attack and
how long it took to recover.

Each peer row carries its throughput as a rate per interval:

    series_ms            the interval (50 ms by default; null: off)
    tx_series_start_ms   when the first interval starts, since the peer's start
    tx_series_mbit       what it sent, Mbit/s per interval
    rx_series_start_ms, rx_series_mbit
                         the same for what arrived

start_unix_ms puts every peer's series on one clock, so an attack's start
and end (Unix ms, as the runner records them) line up with both ends.

Standard library only, like the reports.
"""

from __future__ import annotations

import statistics


def timeline(row: dict, side: str = "rx") -> list[tuple[float, float]]:
    """(start of the interval in Unix ms, Mbit/s) for one peer row's tx or
    rx series; empty when the row has none."""
    every, start, mbit = row.get("series_ms"), row.get(f"{side}_series_start_ms"), row.get(f"{side}_series_mbit")
    if not every or start is None or not mbit:
        return []
    t0 = row["start_unix_ms"] + start
    return [(t0 + i * every, v) for i, v in enumerate(mbit)]


def under_attack(points: list[tuple[float, float]], every: float, attack_start: float, attack_end: float,
                 level: float = 0.9, hold: int = 3) -> dict:
    """Goodput before, during and after an attack, and the recovery time.

    Intervals count toward a window only when they lie wholly inside it.
    before is the median rate before the attack; recovered means the rate
    stayed at or above level x before for hold intervals in a row, and
    recovery_ms is from the attack's start to the first of them, counted
    only after the rate first fell below that line (0 if it never did;
    None if it fell and never came back while the series lasted)."""
    before = [v for t, v in points if t + every <= attack_start]
    during = [v for t, v in points if t >= attack_start and t + every <= attack_end]
    after = [v for t, v in points if t >= attack_end]
    base = statistics.median(before) if before else None
    return {
        "goodput_before_mbit": base,
        "goodput_during_mbit": statistics.mean(during) if during else None,
        "goodput_min_mbit": min(during) if during else None,
        "goodput_after_mbit": statistics.median(after) if after else None,
        "recovery_ms": recovery(points, attack_start, base * level, hold) if base else None,
    }


def recovery(points: list[tuple[float, float]], attack_start: float, line: float, hold: int) -> float | None:
    since = [(t, v) for t, v in points if t >= attack_start]
    fell = next((i for i, (_, v) in enumerate(since) if v < line), None)
    if fell is None:
        return 0.0
    run = 0
    for i in range(fell, len(since)):
        run = run + 1 if since[i][1] >= line else 0
        if run == hold:
            return round(since[i - hold + 1][0] - attack_start, 3)
    return None
