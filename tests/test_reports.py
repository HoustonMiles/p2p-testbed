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

    def test_relayed_trials_count_when_both_sides_are_ready(self):
        base = self.folders.write("base", [trial("c00-aaa111", **sides(punched=True, direct=True, hop_connected=True,
                                                                      rtt_ms_p50=70.0))])
        new = self.folders.write("new", [
            trial("c00-aaa111", **sides(relayed=True, direct=True, ready_ms=300.0, rtt_ms_p50=71.0)),
            trial("c00-aaa111", **sides(relayed=True, direct=True, ready_ms=None, rtt_ms_p50=900.0)),
        ])
        out = report(reports.compare, base, [new])
        self.assertIn("1>1", out)
        self.assertIn("70.00 > 71.00", out)

    def test_ignore_lines_up_configurations_that_differ_in_one_setting(self):
        base = self.folders.write("base", [trial("c00-aaa111", **sides(punched=True, rtt_ms_p50=70.0))])
        new = self.folders.write("new", [
            trial("c00-bbb222", relay_delay_ms="10", **sides(punched=True, rtt_ms_p50=90.0)),
            trial("c01-ccc333", relay_delay_ms="40", **sides(punched=True, rtt_ms_p50=150.0)),
        ])
        self.assertEqual(report_code(reports.compare, base, [new]), 1)  # nothing in common by ID
        out = report(reports.compare, base, [new], ("relay_delay_ms",))
        self.assertIn("relay_delay_ms=10", out)
        self.assertIn("70.00 > 150.00", out)

    def test_no_common_configurations(self):
        base = self.folders.write("base", [trial("c00-aaa111")])
        new = self.folders.write("new", [trial("c00-bbb222")])
        self.assertEqual(report_code(reports.compare, base, [new]), 1)



class MeshReportTest(unittest.TestCase):
    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def mesh_trial(self, config_id: str, label: str, **columns) -> dict:
        row = {"config_id": config_id, "config_label": label, "ok": True, "pairs": 6, "pairs_usable": 6,
               "pairs_as_predicted": 6, "time_to_usable_ms": 400.0, "time_to_full_mesh_ms": 400.0,
               "join_ms_p50": 300.0}
        row.update(columns)
        return row

    def columns(self, out: str, label_words: int) -> list[dict]:
        rows = []
        for line in out.splitlines()[1:]:
            values = line.split()
            rows.append(dict(zip(reports.MESH_HEADER[1:], values[label_words:])))
        return rows

    def test_full_and_partial_meshes(self):
        run = self.folders.write("mesh", [
            self.mesh_trial("c00-aaa111", "peers=4"),
            self.mesh_trial("c00-aaa111", "peers=4", time_to_usable_ms=600.0, time_to_full_mesh_ms=600.0),
            self.mesh_trial("c01-bbb222", "peers=4b", ok=False, pairs_usable=4, pairs_as_predicted=5,
                            time_to_full_mesh_ms=None),
        ])
        full, partial = self.columns(report(reports.mesh, [run]), 1)
        self.assertEqual((full["n"], full["full"], full["ready_p50_ms"], full["ready_max_ms"], full["join_p50_ms"],
                          full.get("!")), ("2", "1.00", "500.00", "600.00", "300.00", None))
        self.assertEqual((partial["ok"], partial["usable"], partial["as_predicted"], partial["full"], partial["!"]),
                         ("0.000", "4.00", "0.833", "0.000", "!"))


if __name__ == "__main__":
    unittest.main()


class AttackReportTest(unittest.TestCase):
    """Experiment C volumetric: the victim's goodput as the flood grows,
    the arm (plain/hopping) and the multiple shown per row."""

    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def row(self, cid, arm, mult, during, rx_mbit):
        return trial(cid, direct_mode=arm, flood_multiple=mult, flood_mbit=mult * 50.0,
                     gp_goodput_before_mbit=40.0, gp_goodput_during_mbit=during,
                     gp_goodput_min_mbit=during, gp_goodput_after_mbit=40.0,
                     a_rx_mbit=rx_mbit, a_rx_loss=round(1 - rx_mbit / 40, 3))

    def test_arm_and_multiple_distinguish_rows(self):
        run = self.folders.write("attack_volume", [
            self.row("c00-aaa", "plain", 0, 40.0, 40.0),
            self.row("c04-bbb", "plain", 2, 2.0, 8.0),
            self.row("c05-ccc", "hopping", 2, 3.0, 9.0),
        ])
        out = report(reports.attack, [run])
        self.assertIn("plain", out)
        self.assertIn("hopping", out)
        # The control keeps its goodput; both 2x arms collapse (hopping no better).
        lines = [ln for ln in out.splitlines() if "coast" in ln]
        self.assertEqual(len(lines), 3)
        self.assertTrue(any("40.00" in ln and "plain" in ln and " 0.000 " in ln for ln in lines))

    def test_relay_flood_arm_is_marked(self):
        # Two relayed arms share the mode string; the one flooding the relay
        # (victim hidden) is marked so it reads apart from the exposed one.
        hidden = trial("c07-hid", direct_mode="-relay -direct", attack_target="relay",
                       flood_multiple=2, flood_mbit=100.0, gp_goodput_during_mbit=39.8,
                       gp_goodput_min_mbit=39.0, a_rx_mbit=39.8, a_rx_loss=0.003)
        exposed = trial("c08-exp", direct_mode="-relay -direct", attack_target="victim",
                        flood_multiple=2, flood_mbit=100.0, gp_goodput_during_mbit=31.5,
                        gp_goodput_min_mbit=12.0, a_rx_mbit=32.0, a_rx_loss=0.18)
        out = report(reports.attack, [self.folders.write("attack_relay", [hidden, exposed])])
        marked = [ln for ln in out.splitlines() if "floods the relay" in ln]
        self.assertEqual(len(marked), 1)              # only the hidden arm is marked
        self.assertIn("39.8", marked[0])              # and it keeps its goodput


class AttackReactiveReportTest(unittest.TestCase):
    """The reactive experiment uses the `arm` bundle and sweeps react_delay_x;
    the report shows the arm and the delay column."""

    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def row(self, cid, arm, delay, during):
        return trial(cid, arm=arm, react_delay_x=delay, flood_multiple=0.5, flood_mbit=25.0,
                     gp_goodput_before_mbit=40.0, gp_goodput_during_mbit=during,
                     gp_goodput_min_mbit=during, gp_goodput_after_mbit=40.0,
                     a_rx_mbit=during, a_rx_loss=round(1 - during / 40, 3))

    def test_shows_arm_and_delay(self):
        run = self.folders.write("attack_react", [
            self.row("c00-aaa", "hopping", 0.0, 2.0),    # attacker keeps up: goodput sinks
            self.row("c01-bbb", "hopping", 1.0, 39.0),   # a hop behind: goodput preserved
            self.row("c02-ccc", "raw", 0.0, 2.0),        # fixed port: always hit
        ])
        out = report(reports.attack, [run])
        self.assertIn("delay", out.splitlines()[0])      # the new column
        self.assertIn("hopping", out)
        self.assertIn("raw", out)
        # the hopping row at delay 1.0 keeps most goodput; at delay 0 it does not
        kept = [ln for ln in out.splitlines() if "hopping" in ln and "39." in ln]
        self.assertEqual(len(kept), 1)


class HitsReportTest(unittest.TestCase):
    """Reactive hit-rate: attacker packets reaching a live hop socket
    (hop_unrouted + hop_foreign) fall as the reaction delay grows, which the
    goodput column does not show."""

    def setUp(self):
        self.folders = ResultFolders()

    def tearDown(self):
        self.folders.cleanup()

    def row(self, cid, delay, unrouted, during):
        return trial(cid, arm="hopping", react_delay_x=delay, flood_multiple=1.0,
                     flood_packets=100000, a_hop_unrouted=unrouted, a_hop_foreign=0,
                     gp_goodput_during_mbit=during)

    def test_hit_rate_falls_with_delay(self):
        run = self.folders.write("attack_react", [
            self.row("c00-aaa", 0.0, 40000, 34.0),   # keeps up: many hits
            self.row("c01-bbb", 1.0, 2000, 34.0),    # a hop behind: few hits
        ])
        out = report(reports.hits, [run])
        self.assertIn("hit_rate", out.splitlines()[0])
        d0 = next(ln for ln in out.splitlines() if "40000.00" in ln)   # delay 0
        d1 = next(ln for ln in out.splitlines() if "2000.00" in ln)    # delay 1
        self.assertIn("0.400", d0)   # high hit-rate when the attacker keeps up
        self.assertIn("0.020", d1)   # near zero when a hop behind
        self.assertIn("34.00", d0)   # goodput is the same either way (link-bound)
        self.assertIn("34.00", d1)

    def test_raw_fixed_port_hits_are_counted_via_rx_stray(self):
        # The raw arm has no hopper, so its injected packets land on the one
        # fixed socket and are counted as rx_stray; the report must include it.
        run = self.folders.write("attack_inject_lan", [
            self.row("c00-aaa", 0.0, 40000, 34.0),   # hopping
            trial("c09-raw", arm="raw", react_delay_x=0.0, flood_multiple=1.0,
                  flood_packets=100000, a_rx_stray=95000, gp_goodput_during_mbit=34.0),
        ])
        out = report(reports.hits, [run])
        self.assertIn("hopping", out)
        raw = next(ln for ln in out.splitlines() if "raw" in ln)
        self.assertIn("95000.00", raw)   # the fixed-port flood lands (rx_stray)
        self.assertIn("0.950", raw)      # ~every packet hits the static port
