import tempfile
import unittest
from pathlib import Path

from p2prunner import experiment
from p2prunner.experiment import PEER_FACTORS, expand_matrix


class TrialParamsTest(unittest.TestCase):
    """A key of [params] in the matrix varies a trial setting per configuration."""

    def setUp(self):
        self.configs = expand_matrix({"stagger_ms": [0, 500], "net_profile": ["coast"]},
                                     {"stagger_ms": 0, "args": "-direct"})

    def test_each_configuration_carries_its_value(self):
        self.assertEqual([c.params for c in self.configs], [{"stagger_ms": 0}, {"stagger_ms": 500}])
        self.assertEqual([c.label for c in self.configs],
                         ["stagger_ms=0, net_profile=coast", "stagger_ms=500, net_profile=coast"])
        self.assertEqual(self.configs[1].row()["stagger_ms"], 500)

    def test_not_a_testbed_setting(self):
        self.assertNotIn("stagger_ms", self.configs[0].factors)
        self.assertNotIn("STAGGER_MS", self.configs[0].env)
        self.assertNotEqual(self.configs[0].id.split("-")[1], self.configs[1].id.split("-")[1])

    def test_a_key_neither_testbed_nor_param_is_refused(self):
        with self.assertRaisesRegex(ValueError, "unknown setting 'stagger'"):
            expand_matrix({"stagger": [0]}, {"stagger_ms": 0})


class LateFactorTest(unittest.TestCase):
    def test_relay_delay_is_a_testbed_setting_outside_older_ids(self):
        plain, delayed = expand_matrix({"net_profile": ["coast"]})[0], expand_matrix({"relay_delay_ms": [20]})[0]
        self.assertNotIn("relay_delay_ms", plain.factors)
        self.assertEqual((plain.env["RELAY_DELAY_MS"], delayed.env["RELAY_DELAY_MS"]), ("0", "20"))
        self.assertEqual(delayed.label, "relay_delay_ms=20")


class ExpandMatrixTest(unittest.TestCase):
    def test_every_combination(self):
        configs = expand_matrix({
            "net_profile": ["local", "coast", "transatlantic"],
            "pairing": [{"name": "realistic", "nat_a": "port_restricted"},
                        {"name": "hardest", "nat_a": "symmetric", "upstream_b": "cgnat"}],
        })
        self.assertEqual(len(configs), 6)
        hardest = [c for c in configs if c.bundles == {"pairing": "hardest"}]
        self.assertEqual({c.factors["upstream_b"] for c in hardest}, {"cgnat"})

    def test_unset_settings_take_testbed_defaults(self):
        (cfg,) = expand_matrix({"net_profile": ["coast"]})
        self.assertEqual(cfg.factors["nat_a"], PEER_FACTORS["nat"][1])
        self.assertEqual(cfg.env["NET_PROFILE"], "coast")

    def test_same_settings_same_id_hash(self):
        a = expand_matrix({"net_profile": ["local", "coast"]})[1]
        b = expand_matrix({"net_profile": ["coast"]})[0]
        self.assertEqual(a.id.split("-")[1], b.id.split("-")[1])
        self.assertNotEqual(a.id, b.id)  # the index part differs

    def test_old_two_peer_configurations_keep_their_ids(self):
        # The IDs of punch_path before mesh step 4, so old and new runs still match.
        exp = experiment.load(Path(__file__).resolve().parent.parent / "experiments/punch_path.toml")
        self.assertEqual([c.id for c in exp.configs],
                         ["c00-a905c1", "c01-6170f7", "c02-c49d0b", "c03-b04809", "c04-322adb", "c05-d14e18"])

    def test_more_peers_with_nat_types_round_robin(self):
        (cfg,) = expand_matrix({"peers": [5], "nats": [["full_cone", "symmetric"]], "nat_e": ["restricted"]})
        self.assertEqual([cfg.factors[f"nat_{p.name}"] for p in cfg.peers],
                         ["full_cone", "symmetric", "full_cone", "symmetric", "restricted"])
        self.assertEqual(cfg.env["NAT_E_TYPE"], "restricted")
        self.assertIn("nats=", cfg.label)
        self.assertIn("peers=5", cfg.label)

    def test_per_peer_link_settings_fall_back_to_the_global_one(self):
        (cfg,) = expand_matrix({"peers": [3], "net_profile": ["coast"], "uplink_mbit_c": [20]})
        self.assertEqual((cfg.env["NET_PROFILE"], cfg.env["NET_PROFILE_A"]), ("coast", ""))
        self.assertEqual(cfg.env["UPLINK_MBIT_C"], "20")

    def test_only_peers_behind_cgnat_get_a_cgn(self):
        (cfg,) = expand_matrix({"peers": [4], "upstream_c": ["cgnat"]})
        self.assertEqual(cfg.cgn_peers, {"c"})

    def test_peer_counts_and_lists_are_checked(self):
        for bad in ({"peers": [1]}, {"peers": [256]}, {"peers": ["4"]}, {"nats": [["fullcone"]]}, {"nats": [[]]},
                    {"peers": [3], "nat_d": ["full_cone"]}):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                expand_matrix(bad)

    def test_typos_are_caught(self):
        with self.assertRaises(ValueError):
            expand_matrix({"nat_c": ["full_cone"]})
        with self.assertRaises(ValueError):
            expand_matrix({"nat_a": ["fullcone"]})
        with self.assertRaises(ValueError):
            expand_matrix({"upstream_a": ["lte"]})
        with self.assertRaises(ValueError):
            expand_matrix({"net_profile": []})


class LoadTest(unittest.TestCase):
    def test_required_settings(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "x.toml"
            path.write_text('name = "x"\ntrial = "ping"\n')
            with self.assertRaises(ValueError):
                experiment.load(path)

    def test_repository_experiments_load(self):
        """Every experiment file in the repository parses and expands."""
        files = sorted(Path(__file__).resolve().parent.parent.glob("experiments/*.toml"))
        self.assertTrue(files)
        for path in files:
            with self.subTest(path.name):
                self.assertTrue(experiment.load(path).configs)


if __name__ == "__main__":
    unittest.main()
