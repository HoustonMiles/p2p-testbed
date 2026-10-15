import contextlib
import io
import unittest

from p2prunner import reports, results
from tests.fakes import ResultFolders, sides, trial


def report(fn, *args) -> str:
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        fn(*args)
    return out.getvalue()


def report_code(fn, *args) -> int:
    with contextlib.redirect_stdout(io.StringIO()):
        return fn(*args)


def hop_columns(out: str) -> dict:
    """The first data row of the hops table, by column name. The label
    ("nat/nat b:upstream profile") is three words."""
    values = out.splitlines()[1].split()
    return dict(zip(reports.HOP_HEADER[1:], values[3:]))


class LoadRunsTest(unittest.TestCase):
    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def test_rerun_replaces_only_its_configurations(self):
        first = self.folders.write("first", [trial("c00-aaa111", ok=False), trial("c01-bbb222")])
        rerun = self.folders.write("rerun", [trial("c00-aaa111", ok=True)])
        rows = results.load_runs([first, rerun])
        self.assertEqual(sorted(r["config_id"] for r in rows), ["c00-aaa111", "c01-bbb222"])
        self.assertTrue(all(r["ok"] for r in rows))

    def test_configurations_match_across_experiments_by_hash(self):
        self.assertEqual(results.config_key({"config_id": "c07-a905c1"}),
                         results.config_key({"config_id": "c00-a905c1"}))

    def test_missing_run_folder_is_reported(self):
        with self.assertRaises(FileNotFoundError):
            results.load_run(self.folders.root / "nope")


class ValuesTest(unittest.TestCase):
    def test_booleans_are_not_numbers(self):
        self.assertIsNone(results.number({"x": True}, "x"))
        self.assertEqual(results.number({"x": 3}, "x"), 3.0)

    def test_side_values_collect_both_sides(self):
        rows = [{"a_rtt": 1.0, "b_rtt": 3.0}, {"a_rtt": None, "b_rtt": 5}]
        self.assertEqual(results.side_values(rows, "rtt"), [1.0, 3.0, 5.0])


class HopsTest(unittest.TestCase):
    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def test_clean_hopping(self):
        ok = trial("c00-aaa111", **sides(punched=True, hop_possible=True, hop_connected=True,
                                         hop_hops_ok=5, hop_hops_missed=0))
        cols = hop_columns(report(reports.hops, [self.folders.write("run", [ok, ok])]))
        self.assertEqual(cols["n"], "2")
        for name in ("ok", "punched", "possible", "connected", "clean"):
            self.assertEqual(cols[name], "1.00", name)
        self.assertNotIn("!", cols)

    def test_missed_hop_is_not_clean(self):
        row = trial("c00-aaa111", **sides(punched=True, hop_possible=True, hop_connected=True),
                    a_hop_hops_missed=1)
        cols = hop_columns(report(reports.hops, [self.folders.write("run", [row])]))
        self.assertEqual((cols["connected"], cols["clean"]), ("1.00", "0.000"))

    def test_disagreeing_port_check_is_flagged(self):
        row = trial("c00-aaa111", **sides(punched=True, hop_possible=False, hop_connected=True))
        cols = hop_columns(report(reports.hops, [self.folders.write("run", [row])]))
        self.assertEqual(cols.get("!"), "!")

    def test_errors_are_listed(self):
        row = trial("c00-aaa111", ok=False, b_error="peer did not reach step 4")
        out = report(reports.hops, [self.folders.write("run", [row])])
        self.assertIn("peer did not reach step 4", out)


class CompareTest(unittest.TestCase):
    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def test_medians_and_change(self):
        base = self.folders.write("base", [trial("c00-aaa111", **sides(punched=True, rx_mbit=48.0))])
        new = self.folders.write("new", [trial("c05-aaa111", **sides(punched=True, direct=True,
                                                                     hop_connected=True, rx_mbit=47.0))])
        out = report(reports.compare, base, [new])
        self.assertIn("48.00 > 47.00", out)
        self.assertIn("-1.00", out.splitlines()[-3])

    def test_trials_without_a_path_are_left_out(self):
        base = self.folders.write("base", [trial("c00-aaa111", **sides(punched=True, rx_mbit=48.0))])
        new = self.folders.write("new", [
            trial("c00-aaa111", **sides(punched=True, direct=True, hop_connected=True, rx_mbit=47.0)),
            trial("c00-aaa111", **sides(punched=True, direct=True, hop_connected=False, rx_mbit=1.0)),
        ])
        out = report(reports.compare, base, [new])
        self.assertIn("1>1", out)
        self.assertIn("48.00 > 47.00", out)

    def test_no_common_configurations(self):
        base = self.folders.write("base", [trial("c00-aaa111")])
        new = self.folders.write("new", [trial("c00-bbb222")])
        self.assertEqual(report_code(reports.compare, base, [new]), 1)


if __name__ == "__main__":
    unittest.main()
