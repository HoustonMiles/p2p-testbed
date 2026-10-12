"""
Command line for the experiment runner.

  uv run python -m p2prunner plan experiments/path_baseline.toml
      Show the configurations, number of trials and a rough time estimate.

  uv run python -m p2prunner run experiments/path_baseline.toml
      Run it. Results go to results/<name>/<start time>/.

  uv run python -m p2prunner run --resume results/path_baseline/20261010-010203
      Continue an interrupted run (same experiment, skips finished trials).

Trial order: each configuration's trials are split into blocks (block_size
trials each), and the blocks of ALL configurations are shuffled with the
experiment's seed. The testbed is recreated only when the configuration
changes, but slow drift over a long run (thermal throttling, background
programs) is spread across configurations instead of hitting one of them.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
import random
import sys
import time
import traceback
from pathlib import Path

from p2prunner import experiment as experiment_mod
from p2prunner.store import ResultStore
from p2prunner.testbed import Testbed, TestbedError
from p2prunner.trials import TRIALS, TrialContext, nat_verify

REPO_ROOT = Path(__file__).resolve().parent.parent

# Rough seconds per trial, for `plan` estimates only.
EST_TRIAL_S = {"nat_verify": 12, "ping": None, "command": None}
EST_CONFIG_START_S = 10      # docker compose up --wait --force-recreate
EST_VERIFY_S = 12            # RFC 5780 check at each configuration start


def trial_seed(base: int, config_id: str, trial: int) -> int:
    """Deterministic per-trial seed: rerunning an experiment reproduces it."""
    digest = hashlib.sha256(f"{base}:{config_id}:{trial}".encode()).digest()
    return int.from_bytes(digest[:4], "big") >> 1


def build_schedule(exp) -> list[tuple[int, list[int]]]:
    """List of (config index, [trial numbers]) blocks in run order."""
    blocks = []
    for cfg in exp.configs:
        trials = list(range(exp.runs))
        for start in range(0, len(trials), exp.block_size):
            blocks.append((cfg.index, trials[start:start + exp.block_size]))
    random.Random(exp.seed).shuffle(blocks)
    return blocks


def estimate_seconds(exp) -> float:
    per_trial = EST_TRIAL_S.get(exp.trial)
    if per_trial is None:
        if exp.trial == "ping":
            p = exp.params
            per_trial = int(p.get("count", 50)) * float(p.get("interval_s", 0.2)) + 2
        else:
            per_trial = float(exp.params.get("est_trial_s", exp.params.get("timeout_s", 60) / 2))
    per_trial += 1  # state reset between trials
    blocks = len(build_schedule(exp))
    start = EST_CONFIG_START_S + (EST_VERIFY_S if exp.verify_nat else 0)
    return exp.total_trials * per_trial + blocks * start


def fmt_duration(seconds: float) -> str:
    seconds = int(seconds)
    h, rem = divmod(seconds, 3600)
    m, s = divmod(rem, 60)
    return f"{h}h{m:02d}m" if h else f"{m}m{s:02d}s"


# =============================================================================
# plan
# =============================================================================
def cmd_plan(args) -> int:
    exp = experiment_mod.load(args.experiment)
    if args.runs:
        exp.runs = args.runs
    print(f"Experiment: {exp.name}  (trial type: {exp.trial})")
    if exp.description:
        print(f"  {exp.description.strip()}")
    print(f"\n{len(exp.configs)} configuration(s) x {exp.runs} run(s) = {exp.total_trials} trials\n")
    for cfg in exp.configs:
        print(f"  {cfg.id}  {cfg.label}")
    print(f"\nEstimated time: about {fmt_duration(estimate_seconds(exp))} "
          "(rough; the run prints a live estimate)")
    return 0


# =============================================================================
# run
# =============================================================================
def cmd_run(args) -> int:
    tb = Testbed(REPO_ROOT)

    if args.resume:
        store = ResultStore(Path(args.resume))
        exp = experiment_mod.load(store.dir / "experiment.toml")
        exp.runs = json.loads((store.dir / "meta.json").read_text())["runs"]
        done = store.done()
        print(f"Resuming {store.dir} ({len(done)} trials already done)")
    else:
        if not args.experiment:
            print("Give an experiment file, or --resume <results folder>.")
            return 2
        exp = experiment_mod.load(args.experiment)
        if args.runs:
            exp.runs = args.runs
        meta = {
            "experiment": exp.name, "trial_type": exp.trial, "runs": exp.runs,
            "seed": exp.seed, "started": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "host": platform.node(), "python": platform.python_version(),
            **tb.versions(),
        }
        if meta["git_dirty"]:
            print("Note: the repository has uncommitted changes; meta.json records this.")
        # meta.json records the runs actually used (including a --runs override);
        # --resume reads it back from there.
        store = ResultStore.create(REPO_ROOT / args.results, exp.name, exp.source_text, meta)
        done = set()
        print(f"Results: {store.dir}")

    trial_fn = TRIALS.get(exp.trial)
    if trial_fn is None:
        print(f"Unknown trial type '{exp.trial}'. Known: {', '.join(TRIALS)}")
        return 2

    schedule = build_schedule(exp)
    remaining = sum(1 for ci, ts in schedule for t in ts
                    if (exp.configs[ci].id, t) not in done)
    print(f"{len(exp.configs)} configurations, {remaining} trials to run. "
          f"Ctrl+C stops safely; resume with --resume {store.dir}\n")

    current = None          # index of the configuration the testbed is running
    finished = 0
    t_run = time.monotonic()
    failures_in_a_row = 0

    try:
        for ci, trials in schedule:
            cfg = exp.configs[ci]
            todo = [t for t in trials if (cfg.id, t) not in done]
            if not todo:
                continue

            # --- bring up this configuration if it isn't already running ---
            if current != ci:
                if not start_config(tb, exp, cfg, store):
                    current = None
                    for t in todo:   # record the skipped trials so they're visible
                        store.add_trial({**base_row(exp, cfg, t), "ok": False,
                                         "error": "configuration failed to start or verify"})
                    finished += len(todo)
                    continue
                current = ci

            # --- run the block's trials ---
            for t in todo:
                row = run_trial(tb, exp, cfg, t, trial_fn, store)
                finished += 1
                failures_in_a_row = 0 if row["ok"] else failures_in_a_row + 1
                elapsed = time.monotonic() - t_run
                eta = elapsed / finished * (remaining - finished)
                status = "ok  " if row["ok"] else "FAIL"
                print(f"[{finished}/{remaining}] {status} {cfg.id} trial {t:>4}  "
                      f"({row['duration_s']:.1f}s)  ETA {fmt_duration(eta)}"
                      + (f"  error: {row['error']}" if row.get("error") else ""))
                if row.get("testbed_error"):
                    current = None   # recreate the testbed before the next trial
                if failures_in_a_row >= 20:
                    print("\n20 failures in a row; stopping so you can check the testbed.")
                    return 1
    except KeyboardInterrupt:
        print(f"\nStopped. Continue later with:\n"
              f"  uv run python -m p2prunner run --resume {store.dir}")
        return 130

    csv_path = store.write_csv()
    print_summary(store)
    print(f"\nDone in {fmt_duration(time.monotonic() - t_run)}. Results: {store.dir}\n"
          f"  {store.trials_path.name}  (pandas: pd.read_json(path, lines=True))\n"
          f"  {csv_path.name}")
    return 0


def base_row(exp, cfg, trial: int) -> dict:
    return {"experiment": exp.name, "trial_type": exp.trial, **cfg.row(),
            "trial": trial, "seed": trial_seed(exp.seed, cfg.id, trial)}


def start_config(tb, exp, cfg, store) -> bool:
    """Bring up a configuration and (optionally) verify its NAT behaviour.
    Tries twice before giving up on it."""
    for attempt in (1, 2):
        print(f"--- starting {cfg.id}: {cfg.label}" + (" (retry)" if attempt == 2 else ""))
        event = {"time": time.strftime("%Y-%m-%dT%H:%M:%S"), **cfg.row(), "attempt": attempt}
        try:
            tb.up(cfg.env)
        except TestbedError as e:
            store.add_config_event({**event, "started": False, "error": str(e)[-500:]})
            print(f"    could not start: {str(e).strip().splitlines()[-1]}")
            continue
        if exp.verify_nat and exp.trial != "nat_verify":
            ctx = TrialContext(trial=-1, seed=0)
            check = nat_verify(tb, cfg, {}, ctx)
            store.add_config_event({**event, "started": True, "verified": check["ok"], **check})
            if not check["ok"]:
                print(f"    NAT verification FAILED: {check}")
                continue
            print("    NAT behaviour verified (RFC 5780)")
        else:
            store.add_config_event({**event, "started": True})
        return True
    print(f"    skipping {cfg.id} for this block")
    return False


def run_trial(tb, exp, cfg, trial: int, trial_fn, store) -> dict:
    row = base_row(exp, cfg, trial)
    ctx = TrialContext(trial=trial, seed=row["seed"])
    row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
    t0 = time.monotonic()
    try:
        tb.reset_state()
        metrics = trial_fn(tb, cfg, exp.params, ctx)
        row.update(metrics)
        row.setdefault("ok", True)
    except TestbedError as e:
        row.update(ok=False, error=str(e)[-300:], testbed_error=True)
    except Exception as e:  # a bug or unexpected output: record it, keep going
        row.update(ok=False, error=f"{type(e).__name__}: {e}"[-300:])
        ctx.raw["traceback"] = traceback.format_exc()
    row["duration_s"] = round(time.monotonic() - t0, 3)
    store.add_trial(row)
    store.save_raw(cfg.id, trial, ctx.raw)
    return row


def print_summary(store) -> None:
    per_config: dict[str, list[bool]] = {}
    labels = {}
    for r in store.rows():
        per_config.setdefault(r["config_id"], []).append(bool(r.get("ok")))
        labels[r["config_id"]] = r.get("config_label", "")
    print("\nSummary (ok / trials per configuration):")
    for cid in sorted(per_config):
        oks = per_config[cid]
        print(f"  {cid}  {sum(oks):>4}/{len(oks):<4}  {labels[cid]}")


# =============================================================================
def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="p2prunner", description="P2P testbed experiment runner")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("plan", help="show configurations and a time estimate")
    p.add_argument("experiment", help="experiment file (.toml)")
    p.add_argument("--runs", type=int, help="override runs per configuration")
    p.set_defaults(func=cmd_plan)

    r = sub.add_parser("run", help="run an experiment")
    r.add_argument("experiment", nargs="?", help="experiment file (.toml)")
    r.add_argument("--runs", type=int, help="override runs per configuration (e.g. a quick test)")
    r.add_argument("--resume", metavar="RESULTS_DIR", help="continue an interrupted run")
    r.add_argument("--results", default="results", help="results folder (default: results/)")
    r.set_defaults(func=cmd_run)

    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except (ValueError, FileNotFoundError) as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2

