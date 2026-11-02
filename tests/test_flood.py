import json
import unittest

from p2prunner import trials
from p2prunner.experiment import expand_matrix
from p2prunner.trials import TrialContext
from tests.fakes import Done


def config(**matrix):
    (cfg,) = expand_matrix({k: [v] for k, v in matrix.items()})
    return cfg


def peer_row(**cols) -> str:
    """One peer's JSON result line, with a receive series."""
    row = {"peer_id": "b", "start_unix_ms": 100000.0, "series_ms": 50.0,
           "rx_series_start_ms": 1000.0, "rx_mbit": 40.0}
    row.update(cols)
    return "setup log line\n" + json.dumps(row)


class FloodFake:
    """Peers print a result row; the attacker prints a flood window."""

    def __init__(self, a_row: str, b_row: str, flood_json: dict):
        self.a_row, self.b_row, self.flood_json = a_row, b_row, flood_json
        self.commands: dict[str, str] = {}
        self.history: list[tuple[str, str]] = []   # every (service, script), in order

    def sh(self, service, script, timeout=0):
        self.commands[service] = script
        self.history.append((service, script))
        if service == "peer-a":
            return Done(self.a_row)
        if service == "peer-b":
            return Done(self.b_row)
        if service == "attacker":
            return Done(json.dumps(self.flood_json))
        if service.startswith("nat-"):
            return Done("7\n" if "ipset" in script else "123\n")   # mapped ports / dnat pkts
        raise AssertionError(service)

    def parallel(self, jobs, all_at_once=False):
        return {name: fn() for name, fn in jobs.items()}


BASE_PARAMS = {"cmd_a": "punch -name a $direct_mode", "cmd_b": "punch -name b $direct_mode",
               "direct_mode": "-direct -hop-interval 1s", "victim": "a", "flood_port": 40000,
               "flood_at": "2s", "flood_duration": "3s"}


class FloodTrialTest(unittest.TestCase):
    def test_scores_goodput_over_the_attack_window(self):
        # a receives 40 Mbit/s, drops to 0 for the 1 s attack, then recovers.
        series = [40.0] * 20 + [0.0] * 20 + [40.0] * 20
        a_row = peer_row(rx_series_mbit=series)
        b_row = peer_row(peer_id="a")
        flood = {"target": "198.18.1.10:40000", "start_unix_ms": 102000.0,
                 "end_unix_ms": 103000.0, "packets": 1, "bytes": 1, "mbit": 100.0}
        tb = FloodFake(a_row, b_row, flood)
        cfg = config(downlink_mbit="50", net_profile="coast")

        m = trials.flood(tb, cfg, {**BASE_PARAMS, "flood_multiple": 2}, TrialContext(trial=0, seed=1))

        self.assertTrue(m["ok"])
        self.assertEqual(m["flood_mbit"], 100.0)                       # 2 x 50 Mbit/s downlink
        self.assertIn("198.18.1.10:40000", tb.commands["attacker"])    # a's public IP
        self.assertIn("-mbit 100", tb.commands["attacker"])
        self.assertEqual(m["gp_goodput_before_mbit"], 40.0)
        self.assertEqual(m["gp_goodput_during_mbit"], 0.0)
        self.assertEqual(m["gp_goodput_min_mbit"], 0.0)
        self.assertEqual(m["gp_goodput_after_mbit"], 40.0)

    def test_control_runs_no_attacker(self):
        a_row = peer_row(rx_series_mbit=[40.0] * 10)
        tb = FloodFake(a_row, peer_row(peer_id="a"), {})
        cfg = config(downlink_mbit="50", net_profile="coast")

        m = trials.flood(tb, cfg, {**BASE_PARAMS, "flood_multiple": 0}, TrialContext(trial=0, seed=1))

        self.assertTrue(m["ok"])
        self.assertEqual(m["flood_mbit"], 0.0)
        self.assertNotIn("attacker", tb.commands)   # no flood at 0x
        self.assertNotIn("gp_goodput_before_mbit", m)  # no window without a flood

    def test_downlink_is_required(self):
        tb = FloodFake(peer_row(rx_series_mbit=[40.0]), peer_row(), {})
        with self.assertRaises(ValueError):
            trials.flood(tb, config(net_profile="coast"), {**BASE_PARAMS, "flood_multiple": 1},
                         TrialContext(trial=0, seed=1))

    def test_relay_arm_floods_the_relay_not_the_victim(self):
        a_row = peer_row(rx_series_mbit=[40.0] * 10)
        flood = {"target": "198.18.0.102:3478", "start_unix_ms": 1.0, "end_unix_ms": 2.0,
                 "packets": 1, "bytes": 1, "mbit": 100.0}
        tb = FloodFake(a_row, peer_row(peer_id="a"), flood)
        cfg = config(downlink_mbit="50", net_profile="coast")

        m = trials.flood(tb, cfg, {**BASE_PARAMS, "flood_multiple": 1, "flood_target": "relay"},
                         TrialContext(trial=0, seed=1))

        self.assertEqual(m["attack_target"], "relay")
        self.assertIn("-target 198.18.0.102:3478", tb.commands["attacker"])  # the relay, not the peer
        self.assertNotIn("198.18.1.10", tb.commands["attacker"])             # the victim's address is hidden

    def test_targets_the_cgn_address_behind_a_cgn(self):
        a_row = peer_row(rx_series_mbit=[40.0] * 10)
        flood = {"target": "x", "start_unix_ms": 1.0, "end_unix_ms": 2.0, "packets": 1, "bytes": 1, "mbit": 50.0}
        tb = FloodFake(a_row, peer_row(peer_id="a"), flood)
        cfg = config(downlink_mbit="50", upstream_a="cgnat", net_profile="coast")

        trials.flood(tb, cfg, {**BASE_PARAMS, "flood_multiple": 1}, TrialContext(trial=0, seed=1))

        self.assertIn("198.18.1.11:40000", tb.commands["attacker"])   # a's CGN public IP


if __name__ == "__main__":
    unittest.main()


class ReactiveFloodTest(unittest.TestCase):
    def reactive_params(self, **over):
        p = {**BASE_PARAMS, "attack": "reactive", "hop_interval_ms": 1000,
             "flood_multiple": 0.5, "react_delay_x": 0.5}
        p.update(over)
        return p

    def test_builds_reactive_attacker_and_schedule_out(self):
        a_row = peer_row(rx_series_mbit=[40.0] * 10)
        flood = {"target": "198.18.1.10 (reactive)", "start_unix_ms": 1.0, "end_unix_ms": 2.0,
                 "packets": 1, "bytes": 1, "mbit": 25.0}
        tb = FloodFake(a_row, peer_row(peer_id="a"), flood)
        cfg = config(downlink_mbit="50", net_profile="coast")

        m = trials.flood(tb, cfg, self.reactive_params(), TrialContext(trial=0, seed=1))

        self.assertEqual(m["attack"], "reactive")
        self.assertEqual(m["react_delay_ms"], 500.0)                    # 0.5 x 1000 ms
        self.assertEqual(m["flood_mbit"], 25.0)                         # 0.5 x 50 Mbit/s
        self.assertIn("-hop-schedule-out /sched/a.txt", tb.commands["peer-a"])
        atk = tb.commands["attacker"]
        self.assertIn("-schedule /sched/a.txt", atk)
        self.assertIn("-react-delay 500ms", atk)
        self.assertIn("-target-ip 198.18.1.10", atk)
        self.assertEqual(m["victim_mapped_ports"], 7)   # read from the victim's router
        self.assertEqual(m["victim_dnat_pkts"], 123)
        # the stale schedule is cleared BEFORE the peers/attacker launch
        self.assertIn(("peer-a", "rm -f /sched/a.txt"), tb.history)
        self.assertLess(tb.history.index(("peer-a", "rm -f /sched/a.txt")),
                        next(i for i, (s, _) in enumerate(tb.history) if s == "attacker"))

    def test_control_has_no_attacker_or_schedule_out(self):
        tb = FloodFake(peer_row(rx_series_mbit=[40.0]), peer_row(peer_id="a"), {})
        cfg = config(downlink_mbit="50", net_profile="coast")
        trials.flood(tb, cfg, self.reactive_params(flood_multiple=0), TrialContext(trial=0, seed=1))
        self.assertNotIn("attacker", tb.commands)
        self.assertNotIn("-hop-schedule-out", tb.commands["peer-a"])    # no oracle when not attacking

    def test_inject_floods_the_lan_address_and_skips_nat_state(self):
        a_row = peer_row(rx_series_mbit=[40.0] * 10)
        flood = {"target": "10.200.1.10 (reactive)", "start_unix_ms": 1.0, "end_unix_ms": 2.0,
                 "packets": 1, "bytes": 1, "mbit": 25.0}
        tb = FloodFake(a_row, peer_row(peer_id="a"), flood)
        cfg = config(downlink_mbit="50", net_profile="coast")

        m = trials.flood(tb, cfg, self.reactive_params(attack="inject"), TrialContext(trial=0, seed=1))

        self.assertEqual(m["attack"], "inject")
        self.assertIn("-hop-schedule-out /sched/a.txt", tb.commands["peer-a"])  # still an oracle
        self.assertIn("-target-ip 10.200.1.10", tb.commands["attacker"])        # the LAN host, not the WAN IP
        self.assertIn("-react-delay 500ms", tb.commands["attacker"])
        self.assertNotIn("victim_mapped_ports", m)   # LAN bypasses the NAT, so no NAT-state read
        self.assertNotIn("nat-a", tb.commands)
