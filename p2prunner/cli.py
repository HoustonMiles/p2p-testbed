"""
Command line for the experiment runner.

  uv run python -m p2prunner plan experiments/path_baseline.toml
      Show the configurations, number of trials and a rough time estimate.

  uv run python -m p2prunner run experiments/path_baseline.toml
      Run it. Results go to results/<name>/<start time>/.

  uv run python -m p2prunner run --resume results/path_baseline/20261010-010203
      Continue an interrupted run (same experiment, skips finished trials).

  uv run python -m p2prunner smoke
  uv run python -m p2prunner verify [--all]
      Check the running testbed (see p2prunner/checks.py).

  uv run python -m p2prunner up --peers 4 [--cgn b,d]
  uv run python -m p2prunner down
  uv run python -m p2prunner compose --peers 4 [--cgn b,d]
      Start a testbed of N peers by hand (settings from your shell or .env,
      e.g. NAT_C_TYPE=symmetric), stop it, or print its compose file.

  uv run python -m p2prunner hops RUN [RERUN ...]
  uv run python -m p2prunner compare BASE NEW [NEW_RERUN ...]
  uv run python -m p2prunner mesh RUN [RERUN ...]
      Reports on finished runs (see p2prunner/reports.py).

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
from dataclasses import dataclass, field
from pathlib import Path

from p2prunner import checks, reports
from p2prunner import compose as compose_file
from p2prunner import experiment as experiment_mod
from p2prunner.mesh import mesh
from p2prunner.store import ResultStore
from p2prunner.testbed import Testbed, TestbedError
from p2prunner.trials import TrialContext, command, nat_verify, ping

REPO_ROOT = Path(__file__).resolve().parent.parent

# Trial types by name (see trials.py and mesh.py).
TRIALS = {"nat_verify": nat_verify, "ping": ping, "command": command, "mesh": mesh}

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
@dataclass
class Run:
    """One run of an experiment and the state shared by its steps."""
    tb: Testbed
    exp: experiment_mod.Experiment
    store: ResultStore
    done: set                      # (config_id, trial) already recorded
    trial_fn: callable
    remaining: int = 0
    finished: int = 0
    failures_in_a_row: int = 0
    current: int | None = None     # index of the configuration the testbed is running
    started: float = field(default_factory=time.monotonic)


def cmd_run(args) -> int:
    tb = Testbed(REPO_ROOT)
    opened = resume_run(args) if args.resume else new_run(args, tb)
    if opened is None:
        return 2
    exp, store, done = opened
    trial_fn = TRIALS.get(exp.trial)
    if trial_fn is None:
        print(f"Unknown trial type '{exp.trial}'. Known: {', '.join(TRIALS)}")
        return 2
    run = Run(tb, exp, store, done, trial_fn)
    run.remaining = sum(1 for ci, ts in build_schedule(exp) for t in ts
                        if (exp.configs[ci].id, t) not in done)
    print(f"{len(exp.configs)} configurations, {run.remaining} trials to run. "
          f"Ctrl+C stops safely; resume with --resume {store.dir}\n")
    try:
        if not run_schedule(run):
            return 1
    except KeyboardInterrupt:
        print(f"\nStopped. Continue later with:\n"
              f"  uv run python -m p2prunner run --resume {store.dir}")
        return 130
    finish(run)
    return 0


def resume_run(args):
    store = ResultStore(Path(args.resume))
    exp = experiment_mod.load(store.dir / "experiment.toml")
    exp.runs = json.loads((store.dir / "meta.json").read_text())["runs"]
    done = store.done()
    store.drop_unfinished_links(done)
    print(f"Resuming {store.dir} ({len(done)} trials already done)")
    return exp, store, done


def new_run(args, tb):
    if not args.experiment:
        print("Give an experiment file, or --resume <results folder>.")
        return None
    exp = experiment_mod.load(args.experiment)
    if args.runs:
        exp.runs = args.runs
    # meta.json records the runs actually used (including a --runs
    # override); --resume reads it back from there.
    meta = {
        "experiment": exp.name, "trial_type": exp.trial, "runs": exp.runs,
        "seed": exp.seed, "started": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "host": platform.node(), "python": platform.python_version(),
        **tb.versions(),
    }
    if meta["git_dirty"]:
        print("Note: the repository has uncommitted changes; meta.json records this.")
    store = ResultStore.create(REPO_ROOT / args.results, exp.name, exp.source_text, meta)
    print(f"Results: {store.dir}")
    return exp, store, set()


def run_schedule(run: Run) -> bool:
    """Runs every block not yet done. False: stopped after 20 failures in a row."""
    for ci, trials in build_schedule(run.exp):
        cfg = run.exp.configs[ci]
        todo = [t for t in trials if (cfg.id, t) not in run.done]
        if not todo:
            continue
        if run.current != ci and not switch_config(run, cfg, todo):
            continue
        for t in todo:
            if not record_trial(run, cfg, t):
                print("\n20 failures in a row; stopping so you can check the testbed.")
                return False
    return True


def switch_config(run: Run, cfg, todo: list[int]) -> bool:
    """Brings up cfg. If it will not start, records its trials as failed."""
    if start_config(run, cfg):
        run.current = cfg.index
        return True
    run.current = None
    for t in todo:  # recorded, so the skipped trials are visible
        run.store.add_trial({**base_row(run.exp, cfg, t), "ok": False,
                             "error": "configuration failed to start or verify"})
    run.finished += len(todo)
    return False


def record_trial(run: Run, cfg, trial: int) -> bool:
    """Runs one trial and prints progress. False: 20 failures in a row."""
    row = run_trial(run, cfg, trial)
    run.finished += 1
    run.failures_in_a_row = 0 if row["ok"] else run.failures_in_a_row + 1
    elapsed = time.monotonic() - run.started
    eta = elapsed / run.finished * (run.remaining - run.finished)
    status = "ok  " if row["ok"] else "FAIL"
    print(f"[{run.finished}/{run.remaining}] {status} {cfg.id} trial {trial:>4}  "
          f"({row['duration_s']:.1f}s)  ETA {fmt_duration(eta)}"
          + (f"  error: {row['error']}" if row.get("error") else ""))
    if row.get("testbed_error"):
        run.current = None  # recreate the testbed before the next trial
    return run.failures_in_a_row < 20


def finish(run: Run) -> None:
    csv_path = run.store.write_csv()
    print_summary(run.store)
    print(f"\nDone in {fmt_duration(time.monotonic() - run.started)}. Results: {run.store.dir}\n"
          f"  {run.store.trials_path.name}  (pandas: pd.read_json(path, lines=True))\n"
          f"  {csv_path.name}")
    if run.current is not None:
        print(f"\nThe testbed is still running {run.exp.configs[run.current].label}.\n"
              "  uv run python -m p2prunner down    returns to the committed two-peer stack")


def base_row(exp, cfg, trial: int) -> dict:
    return {"experiment": exp.name, "trial_type": exp.trial, **cfg.row(),
            "trial": trial, "seed": trial_seed(exp.seed, cfg.id, trial)}


def start_config(run: Run, cfg) -> bool:
    """Brings up a configuration and, if the experiment asks, verifies its
    NAT behaviour. Tries twice before giving up on it."""
    for attempt in (1, 2):
        print(f"--- starting {cfg.id}: {cfg.label}" + (" (retry)" if attempt == 2 else ""))
        event = {"time": time.strftime("%Y-%m-%dT%H:%M:%S"), **cfg.row(), "attempt": attempt}
        if bring_up(run, cfg, event):
            return True
    print(f"    skipping {cfg.id} for this block")
    return False


def bring_up(run: Run, cfg, event: dict) -> bool:
    try:
        run.tb.up(cfg)
    except TestbedError as e:
        run.store.add_config_event({**event, "started": False, "error": str(e)[-500:]})
        print(f"    could not start: {str(e).strip().splitlines()[-1]}")
        return False
    if not run.exp.verify_nat or run.exp.trial == "nat_verify":
        run.store.add_config_event({**event, "started": True})
        return True
    check = nat_verify(run.tb, cfg, {}, TrialContext(trial=-1, seed=0))
    run.store.add_config_event({**event, "started": True, "verified": check["ok"], **check})
    if not check["ok"]:
        print(f"    NAT verification FAILED: {check}")
        return False
    print("    NAT behaviour verified (RFC 5780)")
    return True


def run_trial(run: Run, cfg, trial: int) -> dict:
    row = base_row(run.exp, cfg, trial)
    ctx = TrialContext(trial=trial, seed=row["seed"])
    row["started"] = time.strftime("%Y-%m-%dT%H:%M:%S")
    t0 = time.monotonic()
    try:
        run.tb.reset_state()
        row.update(run.trial_fn(run.tb, cfg, {**run.exp.params, **cfg.params}, ctx))
        row.setdefault("ok", True)
    except TestbedError as e:
        row.update(ok=False, error=str(e)[-300:], testbed_error=True)
    except Exception as e:  # a bug or unexpected output: record it, keep going
        row.update(ok=False, error=f"{type(e).__name__}: {e}"[-300:])
        ctx.raw["traceback"] = traceback.format_exc()
    row["duration_s"] = round(time.monotonic() - t0, 3)
    keys = {k: row[k] for k in ("config_id", "trial", "seed")}
    run.store.add_links([{**keys, **link} for link in ctx.links])
    run.store.add_trial(row)
    run.store.save_raw(cfg.id, trial, ctx.raw)
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
# smoke / verify
# =============================================================================
def cmd_smoke(args) -> int:
    tb = Testbed(REPO_ROOT)
    if problem := checks.preflight(tb):
        print(problem)
        return 2
    return checks.smoke(tb)


def cmd_verify(args) -> int:
    tb = Testbed(REPO_ROOT)
    if problem := checks.preflight(tb, need_running=not args.all):
        print(problem)
        return 2
    return checks.verify(tb, every_type=args.all)


def cmd_compose(args) -> int:
    print(compose_file.render(args.peers, cgn_names(args.cgn)), end="")
    return 0


def cmd_up(args) -> int:
    tb = Testbed(REPO_ROOT)
    try:
        tb.use_stack(args.peers, cgn_names(args.cgn))
    except TestbedError as e:
        print(e)
        return 1
    result = tb.compose("up", "-d", "--wait", "--remove-orphans", timeout=120 + 5 * args.peers)
    print(result.stderr[-2000:] if result.returncode else f"Started {args.peers} peers ({tb.compose_path}).")
    return result.returncode


def cmd_down(args) -> int:
    Testbed(REPO_ROOT).down()
    print("Stopped. The committed two-peer docker-compose.yml is in use again.")
    return 0


def cgn_names(text: str) -> set[str]:
    return {name.strip() for name in text.split(",") if name.strip()}


def cmd_hops(args) -> int:
    return reports.hops(args.runs)


def cmd_compare(args) -> int:
    return reports.compare(args.base, args.new, tuple(args.ignore))


def cmd_mesh(args) -> int:
    return reports.mesh(args.runs)


# =============================================================================
def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except (ValueError, FileNotFoundError) as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="p2prunner", description="P2P testbed experiment runner")
    sub = parser.add_subparsers(dest="command", required=True)
    add_experiment_commands(sub)
    add_testbed_commands(sub)
    add_report_commands(sub)
    return parser


def add_experiment_commands(sub) -> None:
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


def add_testbed_commands(sub) -> None:
    s = sub.add_parser("smoke", help="check the running testbed's wiring (seconds)")
    s.set_defaults(func=cmd_smoke)

    v = sub.add_parser("verify", help="RFC 5780 check of the NAT behaviour")
    v.add_argument("--all", action="store_true",
                   help="cycle through every NAT type (as home router and as CGN), then restore")
    v.set_defaults(func=cmd_verify)

    for name, func, help_text in (("compose", cmd_compose, "print the compose file for N peers"),
                                  ("up", cmd_up, "start a testbed of N peers by hand")):
        u = sub.add_parser(name, help=help_text)
        u.add_argument("--peers", type=int, default=2, help="how many peers (default 2)")
        u.add_argument("--cgn", default="", metavar="NAMES",
                       help="peers that get a CGN to sit behind, e.g. b,d (then NAT_B_UPSTREAM=cgnat)")
        u.set_defaults(func=func)
    d = sub.add_parser("down", help="stop the testbed and go back to the committed two-peer stack")
    d.set_defaults(func=cmd_down)


def add_report_commands(sub) -> None:
    h = sub.add_parser("hops", help="Experiment A: hop success per NAT pairing")
    h.add_argument("runs", nargs="+", metavar="RUN",
                   help="results folder; later folders replace the configurations they rerun")
    h.set_defaults(func=cmd_hops)

    c = sub.add_parser("compare", help="two runs of the same configurations side by side")
    c.add_argument("base", metavar="BASE", help="results folder of the baseline, e.g. punch_path")
    c.add_argument("new", nargs="+", metavar="NEW", help="results folder(s) to compare against it")
    c.add_argument("--ignore", action="append", default=[], metavar="SETTING",
                   help="match configurations that differ only in this setting, e.g. relay_delay_ms (repeatable)")
    c.set_defaults(func=cmd_compare)

    m = sub.add_parser("mesh", help="mesh trials: how much of each mesh came up, as predicted, how fast")
    m.add_argument("runs", nargs="+", metavar="RUN",
                   help="results folder; later folders replace the configurations they rerun")
    m.set_defaults(func=cmd_mesh)

