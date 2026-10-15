import unittest
from collections import Counter

from p2prunner import cli
from p2prunner.experiment import Experiment, expand_matrix


def small_experiment(runs=10, block_size=5, seed=1) -> Experiment:
    exp = Experiment(name="t", trial="ping", runs=runs, block_size=block_size, seed=seed)
    exp.configs = expand_matrix({"net_profile": ["local", "coast", "transatlantic"]})
    return exp


class ScheduleTest(unittest.TestCase):
    def test_every_trial_exactly_once(self):
        exp = small_experiment()
        counts = Counter((ci, t) for ci, trials in cli.build_schedule(exp) for t in trials)
        self.assertEqual(len(counts), exp.total_trials)
        self.assertEqual(set(counts.values()), {1})

    def test_blocks_keep_their_size(self):
        blocks = cli.build_schedule(small_experiment(runs=10, block_size=5))
        self.assertEqual({len(trials) for _, trials in blocks}, {5})

    def test_order_depends_only_on_the_seed(self):
        self.assertEqual(cli.build_schedule(small_experiment(seed=7)),
                         cli.build_schedule(small_experiment(seed=7)))
        self.assertNotEqual(cli.build_schedule(small_experiment(seed=7)),
                            cli.build_schedule(small_experiment(seed=8)))


class TrialSeedTest(unittest.TestCase):
    def test_reproducible_and_distinct(self):
        self.assertEqual(cli.trial_seed(1, "c00-aaa", 3), cli.trial_seed(1, "c00-aaa", 3))
        seeds = {cli.trial_seed(1, "c00-aaa", t) for t in range(100)}
        self.assertEqual(len(seeds), 100)

    def test_fits_a_signed_32_bit_int(self):
        self.assertTrue(all(0 <= cli.trial_seed(1, "c", t) < 2**31 for t in range(100)))


class FormatTest(unittest.TestCase):
    def test_durations(self):
        self.assertEqual(cli.fmt_duration(75), "1m15s")
        self.assertEqual(cli.fmt_duration(3 * 3600 + 120), "3h02m")


if __name__ == "__main__":
    unittest.main()
