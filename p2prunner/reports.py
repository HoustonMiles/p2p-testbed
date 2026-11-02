"""
Reports on finished runs.

  uv run python -m p2prunner hops RUN [RERUN ...]
      Experiment A: did port-hopping hold, per NAT pairing?

  uv run python -m p2prunner compare BASE NEW [NEW_RERUN ...]
      Two runs of the same configurations side by side, e.g. punch_path
      (raw) against direct_path (Noise + hopping) for Experiment B.

  uv run python -m p2prunner mesh RUN [RERUN ...]
      Mesh trials: how much of each mesh came up, as predicted, how fast.

All run with plain python3 as well: standard library only.
"""

from __future__ import annotations

from p2prunner.results import (
    both,
    by_config,
    config_key,
    fmt,
    label,
    load_run,
    load_runs,
    median,
    number,
    print_table,
    share,
    side_values,
)

# =============================================================================
# hops
# =============================================================================
HOP_HEADER = ["config", "n", "ok", "punched", "possible", "connected", "clean",
              "hops_ok_a", "hops_ok_b", "ping_loss", "rtt_p50", "!"]


def hops(paths: list[str]) -> int:
    """Per configuration, the share of trials where the tools ran (ok), both
    sides punched, both NATs keep ports (possible), the first hop worked
    both ways (connected), and no hop was missed (clean). "!" marks a
    configuration where connected and possible disagree: the port check
    should predict hop success exactly."""
    groups = by_config(load_runs(paths))
    table = sorted((hop_row(trials) for trials in groups.values()), key=lambda r: r[0])
    print_table(HOP_HEADER, table)
    print_errors([r for trials in groups.values() for r in trials if not r.get("ok")])
    return 0


def hop_row(trials: list[dict]) -> list[str]:
    connected = [both(r, "hop_connected") for r in trials]
    possible = [both(r, "hop_possible") for r in trials]
    clean = [c and missed_hops(r) == 0 for c, r in zip(connected, trials)]
    return [
        label(trials[0]), str(len(trials)),
        fmt(share([bool(r.get("ok")) for r in trials])),
        fmt(share([both(r, "punched") for r in trials])),
        fmt(share(possible)), fmt(share(connected)), fmt(share(clean)),
        fmt(median([v for r in trials if (v := number(r, "a_hop_hops_ok")) is not None])),
        fmt(median([v for r in trials if (v := number(r, "b_hop_hops_ok")) is not None])),
        fmt(median(side_values(trials, "ping_loss"))),
        fmt(median(side_values(trials, "rtt_ms_p50"))),
        "!" if connected != possible else "",
    ]


def missed_hops(row: dict) -> float:
    return (number(row, "a_hop_hops_missed") or 0) + (number(row, "b_hop_hops_missed") or 0)


def print_errors(bad: list[dict]) -> None:
    if not bad:
        return
    print(f"\n{len(bad)} trials not ok; errors:")
    for err in sorted({(r["config_id"], r.get("a_error") or "", r.get("b_error") or "") for r in bad}):
        print("  ", *err)


# =============================================================================
# compare
# =============================================================================
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


def compare(base_path: str, new_paths: list[str], ignore: tuple[str, ...] = ()) -> int:
    """Per configuration in both runs, the median over both sides of each
    metric, as BASE > NEW, counting only trials where the path was up. The
    last line is the median of the per-configuration changes. With ignore,
    each NEW configuration is matched with the BASE one that differs only
    in those settings."""
    base = by_config(load_run(base_path), ignore)
    matched = match(base, load_runs(new_paths), ignore)
    if not matched:
        print("No configurations in common (are these runs of the same settings?)")
        return 1
    up = [(b, [r for r in b if path_up(r)], [r for r in n if path_up(r)], ignored(n[0], ignore)) for b, n in matched]
    metrics = [m for m in METRICS if any(side_values(b_up + n_up, m[0]) for _, b_up, n_up, _ in up)]
    rows, changes = compare_rows(up, metrics)
    print_table(["config", "n"] + [m[1] for m in metrics], rows, pooled_row(metrics, changes))
    print("\nn = trials with the path up (BASE > NEW); cells are medians over both sides, BASE > NEW.")
    if skipped := len(base) - len({id(b) for b, _ in matched}):
        print(f"{skipped} BASE configurations not in NEW (skipped).")
    return 0


def match(base: dict, new_rows: list[dict], ignore: tuple[str, ...]) -> list[tuple[list, list]]:
    """(BASE trials, NEW trials) for each NEW configuration that has a BASE one."""
    groups = by_config(new_rows).values()
    return [(base[key], trials) for trials in groups if (key := config_key(trials[0], ignore)) in base]


def ignored(row: dict, ignore: tuple[str, ...]) -> str:
    """The NEW configuration's values of the ignored settings, for its label."""
    return "".join(f" {k}={row[k]}" for k in ignore if k in row)


def path_up(row: dict) -> bool:
    """Both sides punched, and in direct mode the first hop worked both
    ways; relayed, both sides ready through the relay."""
    if not row.get("ok"):
        return False
    if row.get("a_relayed") or row.get("b_relayed"):
        return number(row, "a_ready_ms") is not None and number(row, "b_ready_ms") is not None
    if not both(row, "punched"):
        return False
    if row.get("a_direct") or row.get("b_direct"):
        return both(row, "hop_connected")
    return True


def compare_rows(up: list, metrics: list) -> tuple[list[list[str]], dict[str, list[float]]]:
    rows, changes = [], {m[0]: [] for m in metrics}
    for base, b_up, n_up, extra in up:
        row = [label(base[0]) + extra, f"{len(b_up)}>{len(n_up)}"]
        for column, _, _ in metrics:
            bm, nm = median(side_values(b_up, column)), median(side_values(n_up, column))
            if bm is not None and nm is not None:
                changes[column].append(nm - bm)
            row.append(f"{fmt(bm)} > {fmt(nm)}")
        rows.append(row)
    return rows, changes


def pooled_row(metrics: list, changes: dict[str, list[float]]) -> list[str]:
    row = ["all: median change", ""]
    for column, _, unit in metrics:
        m = median(changes[column])
        row.append("-" if m is None else f"{'+' if m >= 0 else ''}{fmt(m)} {unit}".strip())
    return row


# =============================================================================
# mesh
# =============================================================================
MESH_HEADER = ["config", "n", "ok", "pairs", "usable", "as_predicted", "full", "ready_p50_ms", "ready_max_ms",
               "join_p50_ms", "!"]


def mesh(paths: list[str]) -> int:
    """Per configuration: the share of trials that ran cleanly (ok), pairs
    per mesh and the median that came up usable, the share of all pairs
    whose outcome RFC 4787 predicts (as_predicted), the share of trials that
    formed a full mesh, how long the usable pairs took to be ready
    (time_to_usable_ms, from the first peer's start), and the median time
    a peer took to join the members already running (join_ms_p50). "!"
    marks a configuration where some pair did not do as predicted.
    Configurations come in the experiment's order."""
    groups = sorted(by_config(load_runs(paths)).values(), key=lambda trials: trials[0]["config_id"])
    print_table(MESH_HEADER, [mesh_row(trials) for trials in groups])
    return 0


def mesh_row(trials: list[dict]) -> list[str]:
    pairs = sum(r.get("pairs", 0) for r in trials)
    predicted = sum(r.get("pairs_as_predicted", 0) for r in trials)
    ready = sorted(column(trials, "time_to_usable_ms"))
    return [
        trials[0].get("config_label", trials[0]["config_id"]), str(len(trials)),
        fmt(share([bool(r.get("ok")) for r in trials])),
        str(trials[0].get("pairs", "-")),
        fmt(median(column(trials, "pairs_usable"))),
        fmt(predicted / pairs if pairs else None),
        fmt(share([r.get("time_to_full_mesh_ms") is not None for r in trials])),
        fmt(median(ready)), fmt(ready[-1] if ready else None),
        fmt(median(column(trials, "join_ms_p50"))),
        "!" if predicted != pairs else "",
    ]


def column(trials: list[dict], name: str) -> list[float]:
    """The numbers in one column of the trials, skipping the missing."""
    return [v for r in trials if (v := number(r, name)) is not None]


# =============================================================================
# attack
# =============================================================================
ATTACK_HEADER = ["config", "n", "mult", "mbit", "delay", "before", "during", "min", "after", "rx_mbit", "rx_loss"]


def attack(paths: list[str]) -> int:
    """Experiment C (volumetric): the victim's goodput as the flood grows.

    Per configuration (an arm x a flood multiple): the median over trials of
    the victim's goodput before / during / after the attack, its lowest
    during, and its overall receive rate and loss. Compare a multiple of 0
    (the control) with 1x and 2x, and the plain arm with hopping: a flood
    that fills the link should sink both, because port-hopping cannot escape
    a volumetric flood."""
    groups = by_config(load_runs(paths))
    table = sorted((attack_row(trials) for trials in groups.values()), key=lambda r: r[0])
    print_table(ATTACK_HEADER, table)
    print("\nbefore/during/after/min are the victim's goodput (Mbit/s) over the attack window;")
    print("rx_mbit/rx_loss are its whole-run receive rate and loss. mult is x the downlink,")
    print("delay is the reactive attacker's reaction, in hop intervals ('-' for volumetric).")
    print_errors([r for trials in groups.values() for r in trials if not r.get("ok")])
    return 0


def attack_row(trials: list[dict]) -> list[str]:
    def med(col: str) -> str:
        return fmt(median([v for r in trials if (v := number(r, col)) is not None]))
    first = trials[0]
    arm = first.get("arm") or first.get("direct_mode", "")
    # The relayed arms share a mode string; the one flooding the relay (the
    # victim's address hidden) is marked so it is not confused with the one
    # flooding the victim's own address (the relay's limit).
    suffix = " [floods the relay]" if first.get("attack_target") == "relay" else ""
    delay = fmt(number(first, "react_delay_x")) if "react_delay_x" in first else "-"
    return [
        f"{arm} {label(first)}{suffix}".strip(), str(len(trials)),
        fmt(number(first, "flood_multiple")), med("flood_mbit"), delay,
        med("gp_goodput_before_mbit"), med("gp_goodput_during_mbit"),
        med("gp_goodput_min_mbit"), med("gp_goodput_after_mbit"),
        med("a_rx_mbit"), med("a_rx_loss"),
    ]


# =============================================================================
# hits (reactive attack: how often the attacker reached a live hop socket)
# =============================================================================
HITS_HEADER = ["config", "n", "delay", "mult", "flood_pkts", "reached", "hit_rate", "during"]


def hits(paths: list[str]) -> int:
    """Experiment C (reactive): how well port-hopping keeps the attacker off
    the victim's live socket, which goodput (link-bound) cannot show.

    The victim counts attacker packets that reached its live receive socket.
    In the hopping arm that is three hop counters: hop_unrouted (no matching
    receiver index), hop_foreign (wrong source IP), and hop_auth_failed (right
    source IP but junk that fails the AEAD check). In the raw arm (one fixed
    port, no hopper) the same packets land on the plain socket and are counted
    as rx_stray. reached = their sum; hit_rate = reached / the attacker's
    packets. If port-hopping keeps the attacker off the live socket, the
    hopping arm's reached falls toward zero as the reaction delay passes one
    hop interval, while the raw arm stays near 1 (its port never moves) --
    even though goodput (during) barely moves either way (the link, not the
    socket, is the bottleneck). reached near zero at every delay for BOTH arms
    means the attacker never reached the socket at all (e.g. a NAT dropped the
    flood before it arrived)."""
    groups = by_config(load_runs(paths))
    rows = [hits_row(trials) for trials in groups.values() if has_hopper(trials[0])]
    print_table(HITS_HEADER, sorted(rows, key=lambda r: r[0]))
    print("\nreached = attacker packets that hit the victim's live receive socket")
    print("(hopping arm: hop_unrouted + hop_foreign + hop_auth_failed; raw arm:")
    print("rx_stray); hit_rate = reached / the attacker's packets; during = goodput")
    print("under attack (Mbit/s).")
    return 0


HIT_COLS = ("a_hop_unrouted", "a_hop_foreign", "a_hop_auth_failed", "a_rx_stray")


def has_hopper(row: dict) -> bool:
    return any(number(row, c) is not None for c in HIT_COLS)


def hits_row(trials: list[dict]) -> list[str]:
    def med(col: str) -> str:
        return fmt(median([v for r in trials if (v := number(r, col)) is not None]))

    def reached(r: dict) -> float | None:
        vals = [number(r, c) for c in HIT_COLS]
        return None if all(v is None for v in vals) else sum(v or 0 for v in vals)

    reached_vals = [v for r in trials if (v := reached(r)) is not None]
    rates = [v / p for r in trials if (v := reached(r)) is not None and (p := number(r, "flood_packets"))]
    first = trials[0]
    arm = first.get("arm") or first.get("direct_mode", "")
    return [
        f"{arm} {label(first)}".strip(), str(len(trials)),
        fmt(number(first, "react_delay_x")), fmt(number(first, "flood_multiple")),
        med("flood_packets"), fmt(median(reached_vals)), fmt(median(rates)),
        med("gp_goodput_during_mbit"),
    ]
