import json
import unittest

from p2prunner import trials
from p2prunner.experiment import expand_matrix
from p2prunner.trials import TrialContext
from tests.fakes import Done


class StatsTest(unittest.TestCase):
    def test_percentile_interpolates(self):
        self.assertEqual(trials.percentile([1.0, 2.0, 3.0, 4.0], 50), 2.5)
        self.assertIsNone(trials.percentile([], 50))

    def test_summarize_names_and_values(self):
        s = trials.summarize([3.0, 1.0, 2.0], "rtt")
        self.assertEqual((s["rtt_min"], s["rtt_max"]), (1.0, 3.0))
        self.assertEqual(s["rtt_p50"], 2.0)


class ExpectedBehaviourTest(unittest.TestCase):
    def test_cgnat_takes_the_stricter_of_both(self):
        (cfg,) = expand_matrix({"nat_b": ["full_cone"], "upstream_b": ["cgnat"], "cgn_b": ["restricted"]})
        self.assertEqual(trials.expected_behaviour(cfg, "b"), ("EI", "AD"))
        self.assertEqual(trials.expected_behaviour(cfg, "a"), ("EI", "APD"))


class CommandTrialTest(unittest.TestCase):
    """The command trial reads one JSON object from each peer's last line."""

    def run_command(self, stdout_a, stdout_b, code=0):
        class Peers:
            def sh(self, service, cmd, timeout=0):
                return Done(stdout_a if service == "peer-a" else stdout_b, code)

            def parallel(self, jobs):
                return {k: f() for k, f in jobs.items()}

        (cfg,) = expand_matrix({"net_profile": ["local"]})
        ctx = TrialContext(trial=0, seed=42)
        params = {"cmd_a": "punch -session $seed", "cmd_b": "punch -session $seed"}
        return trials.command(Peers(), cfg, params, ctx), ctx

    def test_columns_get_side_prefixes(self):
        line = json.dumps({"punched": True, "rtt_ms_samples": [1, 2]})
        metrics, ctx = self.run_command("progress\n" + line, line)
        self.assertTrue(metrics["ok"])
        self.assertTrue(metrics["a_punched"] and metrics["b_punched"])
        self.assertEqual(ctx.raw["a_rtt_ms_samples"], [1, 2])  # lists go to raw, not columns
        self.assertNotIn("a_rtt_ms_samples", metrics)

    def test_missing_json_is_not_ok(self):
        metrics, _ = self.run_command("no json here", json.dumps({"x": 1}))
        self.assertFalse(metrics["ok"])
        self.assertIn("a_error", metrics)

    def test_nonzero_exit_is_not_ok(self):
        metrics, _ = self.run_command(json.dumps({"x": 1}), json.dumps({"x": 1}), code=1)
        self.assertFalse(metrics["ok"])


if __name__ == "__main__":
    unittest.main()
