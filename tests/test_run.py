"""The `run` command end to end, with a fake testbed and a fake trial."""

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from p2prunner import cli
from p2prunner.testbed import TestbedError

EXPERIMENT = """
name = "t"
trial = "fake"
runs = 4
block_size = 2
verify_nat = false
[matrix]
net_profile = ["local", "coast"]
"""


class FakeTestbed:
    def __init__(self, fail_up=False):
        self.fail_up = fail_up
        self.started = []

    def versions(self):
        return {"git_dirty": False}

    def up(self, cfg):
        env = cfg.env
        if self.fail_up:
            raise TestbedError("compose failed")
        self.started.append(env["NET_PROFILE"])

    def reset_state(self):
        pass


class RunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.exp_file = self.root / "t.toml"
        self.exp_file.write_text(EXPERIMENT)
        self.calls = 0

    def tearDown(self):
        self.tmp.cleanup()

    def run_cli(self, tb, trial, *extra):
        argv = ["run", *extra, "--results", str(self.root / "results")]
        if "--resume" not in extra:
            argv.insert(1, str(self.exp_file))
        with mock.patch.object(cli, "Testbed", return_value=tb), \
                mock.patch.dict(cli.TRIALS, {"fake": trial}), \
                contextlib.redirect_stdout(io.StringIO()):
            return cli.main(argv)

    def run_dir(self) -> Path:
        (run,) = (self.root / "results" / "t").iterdir()
        return run

    def rows(self) -> list[dict]:
        return [json.loads(line) for line in (self.run_dir() / "trials.jsonl").read_text().splitlines()]

    def counting_trial(self, interrupt_at=None, ok=True):
        def trial(tb, cfg, params, ctx):
            self.calls += 1
            if self.calls == interrupt_at:
                raise KeyboardInterrupt
            return {"ok": ok, "value": ctx.seed}
        return trial

    def test_full_run(self):
        self.assertEqual(self.run_cli(FakeTestbed(), self.counting_trial()), 0)
        rows = self.rows()
        self.assertEqual(len(rows), 8)
        self.assertEqual({(r["config_id"], r["trial"]) for r in rows}.__len__(), 8)
        self.assertTrue(all(r["ok"] for r in rows))
        self.assertTrue((self.run_dir() / "trials.csv").exists())
        self.assertEqual(json.loads((self.run_dir() / "meta.json").read_text())["runs"], 4)

    def test_interrupt_then_resume(self):
        code = self.run_cli(FakeTestbed(), self.counting_trial(interrupt_at=4))
        self.assertEqual(code, 130)
        self.assertEqual(len(self.rows()), 3)
        code = self.run_cli(FakeTestbed(), self.counting_trial(), "--resume", str(self.run_dir()))
        self.assertEqual(code, 0)
        keys = [(r["config_id"], r["trial"]) for r in self.rows()]
        self.assertEqual(len(keys), 8)
        self.assertEqual(len(set(keys)), 8)  # nothing ran twice

    def test_configuration_that_cannot_start(self):
        self.assertEqual(self.run_cli(FakeTestbed(fail_up=True), self.counting_trial()), 0)
        rows = self.rows()
        self.assertEqual(len(rows), 8)
        self.assertTrue(all(not r["ok"] and "failed to start" in r["error"] for r in rows))
        self.assertEqual(self.calls, 0)

    def test_testbed_restarts_only_when_the_configuration_changes(self):
        # One-trial blocks give a schedule where the same configuration
        # often runs twice in a row; those must not restart the testbed.
        self.exp_file.write_text(EXPERIMENT.replace("block_size = 2", "block_size = 1"))
        exp = cli.experiment_mod.load(self.exp_file)
        order = [ci for ci, _ in cli.build_schedule(exp)]
        switches = 1 + sum(1 for a, b in zip(order, order[1:]) if a != b)
        self.assertLess(switches, len(order))  # the schedule does repeat a configuration
        tb = FakeTestbed()
        self.run_cli(tb, self.counting_trial())
        self.assertEqual(len(tb.started), switches)

    def test_stops_after_twenty_failures_in_a_row(self):
        self.exp_file.write_text(EXPERIMENT.replace("runs = 4", "runs = 15"))
        self.assertEqual(self.run_cli(FakeTestbed(), self.counting_trial(ok=False)), 1)
        self.assertEqual(len(self.rows()), 20)

    def test_trial_exception_is_recorded_not_fatal(self):
        def broken(tb, cfg, params, ctx):
            raise RuntimeError("bad output")
        self.assertEqual(self.run_cli(FakeTestbed(), broken), 0)
        self.assertTrue(all("RuntimeError: bad output" in r["error"] for r in self.rows()))

    def test_unknown_trial_type(self):
        self.exp_file.write_text(EXPERIMENT.replace('"fake"', '"nope"'))
        self.assertEqual(self.run_cli(FakeTestbed(), self.counting_trial()), 2)


if __name__ == "__main__":
    unittest.main()
