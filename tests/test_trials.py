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


class PingTrialTest(unittest.TestCase):
    """Pings from both peers; the first replies are warm-up and not counted."""

    def ping_output(self, seqs):
        return "".join(f"64 bytes from 203.0.113.100: icmp_seq={s} ttl=63 time={10 + s}.0 ms\n" for s in seqs)

    def test_warmup_and_loss(self):
        outputs = {"peer-a": self.ping_output(range(1, 11)),          # all 10 replies
                   "peer-b": self.ping_output([1, 2, 3, 5, 6, 7, 8])}  # 4, 9, 10 lost

        class Peers:
            def exec(self, service, cmd, timeout=0):
                return Done(outputs[service])

            def parallel(self, jobs):
                return {k: f() for k, f in jobs.items()}

        (cfg,) = expand_matrix({"net_profile": ["local"]})
        ctx = TrialContext(trial=0, seed=1)
        m = trials.ping(Peers(), cfg, {"count": 10, "warmup": 2, "interval_s": 0.1}, ctx)
        self.assertTrue(m["ok"])
        self.assertEqual((m["pings_sent"], m["warmup"]), (10, 2))
        self.assertEqual(m["a_rtt_ms_min"], 13.0)             # seq 1-2 are warm-up
        self.assertEqual(m["a_loss"], 0.0)
        self.assertAlmostEqual(m["b_loss"], 1 - 5 / 8)        # 5 of 8 after warm-up
        self.assertEqual(m["b_pings_received"], 7)
        self.assertEqual(len(ctx.raw["rtt_ms_peer-a"]), 10)  # raw keeps every sample


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
