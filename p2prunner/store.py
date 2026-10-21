"""
Results folder layout (one folder per run of an experiment):

  results/<experiment name>/<start time>/
    experiment.toml   exact copy of the experiment file that was run
    meta.json         when, on what (Docker/kernel versions, git commit), seed
    configs.jsonl     one line per configuration start (incl. NAT verification)
    trials.jsonl      one line per trial: configuration columns + metrics
    trials.csv        the same table as CSV, written when the run finishes
    links.jsonl       mesh trials: one line per link end, with its trial's
                      config_id and trial (written before the trial's line)
    raw/<config>/<trial>.json   full details (e.g. every RTT sample, tool output)

JSONL = one JSON object per line. Lines are appended and flushed as soon as
each trial ends, so a crash or Ctrl+C loses at most the trial in progress,
and `--resume` continues from where it stopped. Load it in pandas with
    pd.read_json("trials.jsonl", lines=True)
"""

from __future__ import annotations

import csv
import json
import time
from pathlib import Path


class ResultStore:
    def __init__(self, run_dir: Path):
        self.dir = run_dir
        self.trials_path = run_dir / "trials.jsonl"
        self.configs_path = run_dir / "configs.jsonl"
        self.links_path = run_dir / "links.jsonl"

    @classmethod
    def create(cls, results_root: Path, exp_name: str, experiment_text: str, meta: dict) -> "ResultStore":
        run_dir = results_root / exp_name / time.strftime("%Y%m%d-%H%M%S")
        run_dir.mkdir(parents=True, exist_ok=False)
        (run_dir / "experiment.toml").write_text(experiment_text)
        (run_dir / "meta.json").write_text(json.dumps(meta, indent=2))
        return cls(run_dir)

    # --- writing ---------------------------------------------------------
    @staticmethod
    def _append(path: Path, row: dict) -> None:
        with path.open("a") as f:
            f.write(json.dumps(row) + "\n")
            f.flush()

    def add_trial(self, row: dict) -> None:
        self._append(self.trials_path, row)

    def add_links(self, rows: list[dict]) -> None:
        for row in rows:
            self._append(self.links_path, row)

    def add_config_event(self, row: dict) -> None:
        self._append(self.configs_path, row)

    def save_raw(self, config_id: str, trial: int, raw: dict) -> None:
        if not raw:
            return
        folder = self.dir / "raw" / config_id
        folder.mkdir(parents=True, exist_ok=True)
        (folder / f"{trial:05d}.json").write_text(json.dumps(raw))

    def drop_unfinished_links(self, done: set[tuple[str, int]]) -> None:
        """Before a resume: drop the links of a trial that was interrupted
        before its own line was written, since it runs again."""
        if not self.links_path.exists():
            return
        with self.links_path.open() as f:
            rows = [json.loads(line) for line in f if line.strip()]
        kept = [r for r in rows if (r["config_id"], r["trial"]) in done]
        self.links_path.write_text("".join(json.dumps(r) + "\n" for r in kept))

    # --- reading ---------------------------------------------------------
    def rows(self) -> list[dict]:
        if not self.trials_path.exists():
            return []
        with self.trials_path.open() as f:
            return [json.loads(line) for line in f if line.strip()]

    def done(self) -> set[tuple[str, int]]:
        """(config_id, trial) pairs already recorded, for --resume."""
        return {(r["config_id"], r["trial"]) for r in self.rows()}

    def write_csv(self) -> Path:
        rows = self.rows()
        path = self.dir / "trials.csv"
        columns: list[str] = []
        for r in rows:                       # union of all keys, in first-seen order
            columns += [k for k in r if k not in columns]
        with path.open("w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=columns)
            writer.writeheader()
            writer.writerows(rows)
        return path
