import tempfile
import unittest
from pathlib import Path

from p2prunner import experiment
from p2prunner.experiment import FACTORS, expand_matrix


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
        self.assertEqual(cfg.factors["nat_a"], FACTORS["nat_a"][1])
        self.assertEqual(cfg.env["NET_PROFILE"], "coast")

    def test_same_settings_same_id_hash(self):
        a = expand_matrix({"net_profile": ["local", "coast"]})[1]
        b = expand_matrix({"net_profile": ["coast"]})[0]
        self.assertEqual(a.id.split("-")[1], b.id.split("-")[1])
        self.assertNotEqual(a.id, b.id)  # the index part differs

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
