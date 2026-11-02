"""
Presentation figures from the newest run of each experiment, for the
progress meeting. Writes PNGs to analysis/figures/ and prints a cost table.

    uv run --group analysis python analysis/meeting_figures.py

Covers the results the notebook does not: Experiment A (hop matrix), the
direct-vs-relay cost, the mesh scaling, and the volumetric attack. Each
figure is skipped with a note if its experiment has not been run.
"""

from __future__ import annotations

from pathlib import Path

import matplotlib.pyplot as plt
import pandas as pd

ROOT = Path(__file__).resolve().parent.parent
RESULTS = ROOT / "results"
FIGURES = Path(__file__).resolve().parent / "figures"

SERIES = ["#2a78d6", "#eb6834"]
INK, INK_2, GRID = "#0b0b0b", "#52514e", "#e4e3df"
plt.rcParams.update({
    "figure.dpi": 120, "axes.edgecolor": GRID, "axes.labelcolor": INK_2,
    "axes.titlecolor": INK, "axes.titleweight": "bold", "axes.titlesize": 12,
    "axes.grid": True, "axes.axisbelow": True, "grid.color": GRID, "grid.linewidth": 0.8,
    "axes.spines.top": False, "axes.spines.right": False,
    "xtick.color": INK_2, "ytick.color": INK_2, "legend.frameon": False,
})
NAT_ORDER = ["full_cone", "restricted", "port_restricted", "symmetric"]


def latest(experiment: str) -> pd.DataFrame | None:
    runs = sorted((RESULTS / experiment).glob("*/trials.jsonl"))
    if not runs:
        print(f"skip {experiment}: no results yet")
        return None
    df = pd.read_json(runs[-1], lines=True)
    print(f"{experiment}: {len(df)} trials from {runs[-1].parent.name}")
    return df


def save(fig, name: str) -> None:
    FIGURES.mkdir(exist_ok=True)
    path = FIGURES / name
    fig.tight_layout()
    fig.savefig(path, bbox_inches="tight")
    plt.close(fig)
    print(f"  wrote {path.relative_to(ROOT)}")


def hop_matrix() -> None:
    """Experiment A: share of trials where port-hopping held, per pairing
    (side B on the WAN). Green = hops, red = does not."""
    df = latest("hop_matrix")
    if df is None:
        return
    df = df[df.get("upstream_b", "wan") == "wan"].copy()
    df["held"] = (df.a_hop_connected.fillna(False).astype(bool)
                  & df.b_hop_connected.fillna(False).astype(bool))
    grid = (df.pivot_table(index="nat_a", columns="nat_b", values="held", aggfunc="mean")
              .reindex(index=NAT_ORDER, columns=NAT_ORDER))
    fig, ax = plt.subplots(figsize=(5.2, 4.4))
    ax.imshow(grid.values, cmap="RdYlGn", vmin=0, vmax=1, aspect="equal")
    ax.set_xticks(range(4), [n.replace("_", "\n") for n in NAT_ORDER])
    ax.set_yticks(range(4), [n.replace("_", "\n") for n in NAT_ORDER])
    for i in range(4):
        for j in range(4):
            v = grid.values[i, j]
            ax.text(j, i, "-" if pd.isna(v) else f"{v:.0%}", ha="center", va="center",
                    color=INK, fontsize=10, fontweight="bold")
    ax.set_title("Experiment A: where port-hopping holds")
    ax.set_xlabel("peer B NAT")
    ax.set_ylabel("peer A NAT")
    ax.grid(False)
    save(fig, "fig_hop_matrix.png")


def cost_table() -> None:
    """Experiment B: median RTT and goodput for the raw, direct and relay
    paths on the coast profile (printed, and a small bar chart)."""
    arms = {"raw (plain UDP)": "punch_path", "direct (Noise+hopping)": "direct_path",
            "relay (TURN)": "relay_path"}
    rows = []
    for label, exp in arms.items():
        df = latest(exp)
        if df is None:
            continue
        d = df[df.get("net_profile") == "coast"]
        if d.empty:
            d = df
        rows.append({"path": label,
                     "rtt_p50_ms": d["a_rtt_ms_p50"].median(),
                     "goodput_mbit": d["a_rx_mbit"].median()})
    if not rows:
        return
    t = pd.DataFrame(rows)
    print("\nExperiment B cost (coast profile, median over trials):")
    print(t.to_string(index=False, float_format=lambda v: f"{v:.2f}"))

    fig, (a1, a2) = plt.subplots(1, 2, figsize=(8.6, 3.8))
    x = range(len(t))
    b1 = a1.bar(x, t.rtt_p50_ms, color=SERIES[0], width=0.6)
    a1.bar_label(b1, fmt="%.1f", padding=2, color=INK, fontweight="bold")
    a1.set_xticks(x, t.path, rotation=20, ha="right")
    a1.set_title("RTT (ms, lower better)")
    a1.set_ylim(0, t.rtt_p50_ms.max() * 1.18)
    b2 = a2.bar(x, t.goodput_mbit, color=SERIES[1], width=0.6)
    a2.bar_label(b2, fmt="%.1f", padding=2, color=INK, fontweight="bold")
    a2.set_xticks(x, t.path, rotation=20, ha="right")
    a2.set_title("Goodput (Mbit/s, higher better)")
    a2.set_ylim(0, t.goodput_mbit.max() * 1.18)
    save(fig, "fig_cost.png")


def mesh_scale() -> None:
    """Mesh: median join time and share of pairs usable as the group grows
    (realistic, all port-restricted)."""
    df = latest("mesh_scale")
    if df is None:
        return
    d = df[df.get("nat_mix", "realistic") == "realistic"].copy()
    d["peers"] = d["peers"].astype(int)
    g = (d.groupby("peers").agg(join_ms=("join_ms_p50", "median"),
                                usable=("pairs_usable", "sum"), pairs=("pairs", "sum"))
            .reset_index().sort_values("peers"))
    g["usable_frac"] = g["usable"] / g["pairs"]
    fig, (a1, a2) = plt.subplots(1, 2, figsize=(8.6, 3.6))
    a1.plot(g["peers"], g["join_ms"], "-o", color=SERIES[0])
    for px, py in zip(g["peers"], g["join_ms"]):
        a1.annotate(f"{py:.0f}", (px, py), textcoords="offset points", xytext=(0, 8),
                    ha="center", color=INK, fontsize=9, fontweight="bold")
    a1.set_title("Time to join (ms)")
    a1.set_xlabel("peers")
    a1.set_ylim(0, g["join_ms"].max() * 2)          # flat line sits mid-plot, baseline at 0
    a2.plot(g["peers"], g["usable_frac"], "-o", color=SERIES[1])
    a2.set_title("Pairs usable (share)")
    a2.set_xlabel("peers")
    a2.set_ylim(0, 1.25)
    save(fig, "fig_mesh.png")


def attack() -> None:
    """Volumetric attack: victim goodput as the flood grows, direct-plain
    against direct-hopping. They overlap: hopping does not help."""
    df = latest("attack_volume")
    if df is None:
        return
    df = df.copy()
    df["arm"] = df["direct_mode"].map(lambda s: "hopping" if "hop-force" not in str(s) else "plain")
    g = (df.groupby(["arm", "flood_multiple"]).agg(goodput=("a_rx_mbit", "median")).reset_index())
    fig, ax = plt.subplots(figsize=(6.0, 4.0))
    for i, arm in enumerate(["plain", "hopping"]):
        s = g[g.arm == arm].sort_values("flood_multiple")
        ax.plot(s.flood_multiple, s.goodput, "-o", color=SERIES[i], label=arm)
    ax.set_title("Volumetric flood: hopping does not help")
    ax.set_xlabel("flood size (x the downlink)")
    ax.set_ylabel("victim goodput (Mbit/s)")
    ax.set_ylim(bottom=0)
    ax.legend()
    save(fig, "fig_attack.png")


def attack_react() -> None:
    """Reactive attack: the attacker's hit-rate on the victim's live socket
    falls as its reaction delay grows (hopping working), while goodput stays
    flat (link-bound). Hopping arm only."""
    df = latest("attack_react")
    if df is None:
        return
    df = df.copy()
    df["arm"] = df.get("arm", "")
    d = df[(df["arm"] == "hopping") & df["a_hop_unrouted"].notna()]
    if d.empty:
        print("skip attack_react: no hopping rows with hit counts")
        return
    d = d.assign(reached=d["a_hop_unrouted"].fillna(0) + d.get("a_hop_foreign", 0).fillna(0))
    d = d.assign(hit_rate=d["reached"] / d["flood_packets"])
    g = (d.groupby("react_delay_x").agg(hit=("hit_rate", "median"),
                                        good=("gp_goodput_during_mbit", "median"))
            .reset_index().sort_values("react_delay_x"))
    fig, (a1, a2) = plt.subplots(1, 2, figsize=(8.6, 3.6))
    a1.plot(g["react_delay_x"], g["hit"], "-o", color=SERIES[1])
    a1.set_title("Attacker hit-rate (lower better)")
    a1.set_xlabel("reaction delay (hop intervals)")
    a1.set_ylim(bottom=0)
    a2.plot(g["react_delay_x"], g["good"], "-o", color=SERIES[0])
    a2.set_title("Goodput under attack (Mbit/s)")
    a2.set_xlabel("reaction delay (hop intervals)")
    a2.set_ylim(0, max(g["good"].max() * 1.3, 1))
    save(fig, "fig_attack_react.png")


REACHED_COLS = ["a_hop_unrouted", "a_hop_foreign", "a_hop_auth_failed", "a_rx_stray"]


def reached_frac(d: pd.DataFrame) -> pd.Series:
    """Share of the attacker's packets that hit the victim's live receive
    socket: hop counters for the hopping arm, rx_stray for the raw (fixed
    port) arm. Columns a trial lacks are treated as zero."""
    present = [c for c in REACHED_COLS if c in d.columns]
    total = sum((d[c].fillna(0) for c in present), start=pd.Series(0.0, index=d.index))
    return total / d["flood_packets"]


def attack_inject() -> None:
    """Targeted injection (LAN harness, no NAT in the path): the attacker is
    an oracle told the victim's port but reacting after a delay. Port-hopping
    drives its hit-rate from 1 to 0 as the delay passes one hop interval; a
    fixed port (raw) stays pinned at 1. The dashed line is the model
    max(0, 1 - delay). Goodput is flat either way, so this is precision, not
    bandwidth."""
    df = latest("attack_inject_lan")
    if df is None:
        return
    df = df.copy()
    df["arm"] = df.get("arm", "")
    df["hit_rate"] = reached_frac(df)
    g = (df.groupby(["arm", "react_delay_x"])
           .agg(hit=("hit_rate", "median"), good=("gp_goodput_during_mbit", "median"))
           .reset_index())

    fig, (a1, a2) = plt.subplots(1, 2, figsize=(8.8, 3.7))
    for i, arm in enumerate(["raw", "hopping"]):
        s = g[g.arm == arm].sort_values("react_delay_x")
        if not s.empty:
            a1.plot(s.react_delay_x, s.hit, "-o", color=SERIES[i],
                    label="fixed port" if arm == "raw" else "port-hopping")
    xs = sorted(g.react_delay_x.unique())
    a1.plot(xs, [max(0.0, 1 - x) for x in xs], "--", color=INK_2, linewidth=1,
            label="model max(0, 1-D)")
    a1.set_title("Attacker hit-rate (lower better)")
    a1.set_xlabel("reaction delay (hop intervals)")
    a1.set_ylabel("share of flood on the live socket")
    a1.set_ylim(-0.02, 1.05)
    a1.legend()

    for i, arm in enumerate(["raw", "hopping"]):
        s = g[g.arm == arm].sort_values("react_delay_x")
        if not s.empty:
            a2.plot(s.react_delay_x, s.good, "-o", color=SERIES[i],
                    label="fixed port" if arm == "raw" else "port-hopping")
    a2.set_title("Goodput under attack (Mbit/s)")
    a2.set_xlabel("reaction delay (hop intervals)")
    a2.set_ylim(0, max(g["good"].max() * 1.3, 1))
    save(fig, "fig_attack_inject.png")


def attack_relay() -> None:
    """Relayed flood: victim goodput as the flood grows, for three arms. The
    relay defends goodput only by hiding the address -- a flood on the relay
    (victim hidden) leaves goodput at the baseline, while a flood on the
    victim's own address sinks it whether the session is relayed or not."""
    df = latest("attack_relay")
    if df is None:
        return
    df = df.copy()

    def arm(row) -> str:
        relayed = "-relay" in str(row.get("direct_mode", ""))
        hidden = str(row.get("attack_target", "victim")) == "relay"
        if not relayed:
            return "direct (flood victim)"
        return "relay, victim hidden (flood relay)" if hidden else "relay, victim exposed (flood victim)"

    df["arm"] = df.apply(arm, axis=1)
    # During the attack where there is one; the 0x control has no window, so
    # fall back to the whole-run receive rate for its baseline point.
    df["goodput"] = df["gp_goodput_during_mbit"].fillna(df["a_rx_mbit"])
    g = df.groupby(["arm", "flood_multiple"]).agg(goodput=("goodput", "median")).reset_index()

    colors = {"direct (flood victim)": SERIES[1],
              "relay, victim exposed (flood victim)": "#b64d4d",
              "relay, victim hidden (flood relay)": SERIES[0]}
    fig, ax = plt.subplots(figsize=(6.4, 4.2))
    for name, color in colors.items():
        s = g[g.arm == name].sort_values("flood_multiple")
        if not s.empty:
            ax.plot(s.flood_multiple, s.goodput, "-o", color=color, label=name)
    ax.set_title("Relayed flood: the relay defends goodput\nonly by hiding the address")
    ax.set_xlabel("flood size (x the downlink)")
    ax.set_ylabel("victim goodput under attack (Mbit/s)")
    ax.set_ylim(0, max(g["goodput"].max() * 1.2, 1))
    ax.set_xticks(sorted(g.flood_multiple.unique()))
    ax.legend(fontsize=8)
    save(fig, "fig_attack_relay.png")


if __name__ == "__main__":
    print(f"results: {RESULTS}")
    hop_matrix()
    cost_table()
    mesh_scale()
    attack()
    attack_react()
    attack_inject()
    attack_relay()
    print(f"\nFigures in {FIGURES.relative_to(ROOT)}/ (drop into your slides).")
