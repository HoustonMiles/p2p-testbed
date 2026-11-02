import unittest

from p2prunner import results
from tests.fakes import ResultFolders, trial


class ValueHelpersTest(unittest.TestCase):
    def test_number_rejects_booleans_and_text(self):
        self.assertEqual(results.number({"x": 3}, "x"), 3.0)
        self.assertIsNone(results.number({"x": True}, "x"))   # bool is not a number here
        self.assertIsNone(results.number({"x": "nope"}, "x"))
        self.assertIsNone(results.number({}, "x"))

    def test_both_needs_both_sides_true(self):
        self.assertTrue(results.both({"a_ok": True, "b_ok": True}, "ok"))
        self.assertFalse(results.both({"a_ok": True}, "ok"))   # missing b counts as false

    def test_side_values_collects_a_and_b(self):
        rows = [{"a_rtt": 1.0, "b_rtt": 2.0}, {"a_rtt": 3.0}]
        self.assertEqual(sorted(results.side_values(rows, "rtt")), [1.0, 2.0, 3.0])

    def test_median_and_share_handle_empty(self):
        self.assertIsNone(results.median([]))
        self.assertEqual(results.median([1.0, 3.0]), 2.0)
        self.assertEqual(results.share([]), 0.0)
        self.assertEqual(results.share([True, False, True, True]), 0.75)

    def test_fmt_switches_precision_at_one(self):
        self.assertEqual(results.fmt(None), "-")
        self.assertEqual(results.fmt(0.5), "0.500")   # < 1: three decimals
        self.assertEqual(results.fmt(48.27), "48.27")  # >= 1: two decimals


class GroupingTest(unittest.TestCase):
    def test_config_key_is_the_hash_part(self):
        self.assertEqual(results.config_key({"config_id": "c03-ab12cd"}), "ab12cd")

    def test_by_config_groups_in_first_seen_order(self):
        rows = [trial("c00-aaa"), trial("c01-bbb"), trial("c00-aaa")]
        groups = results.by_config(rows)
        self.assertEqual(list(groups), ["aaa", "bbb"])
        self.assertEqual(len(groups["aaa"]), 2)


class LoadRunsTest(unittest.TestCase):
    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def test_missing_run_is_reported(self):
        with self.assertRaises(FileNotFoundError):
            results.load_run(self.folders.root / "does-not-exist")

    def test_later_run_replaces_only_its_configurations(self):
        base = self.folders.write("exp", [trial("c00-aaa", rx_mbit=1.0), trial("c01-bbb", rx_mbit=2.0)])
        rerun = self.folders.write("exp_rerun", [trial("c00-aaa", rx_mbit=9.0)])
        merged = results.load_runs([base, rerun])
        by = {results.config_key(r): r["rx_mbit"] for r in merged}
        self.assertEqual(by, {"aaa": 9.0, "bbb": 2.0})   # c00 replaced, c01 kept


if __name__ == "__main__":
    unittest.main()
