import csv
import json
import tempfile
import unittest
from pathlib import Path

from p2prunner.store import ResultStore


class ResultStoreTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def store(self) -> ResultStore:
        return ResultStore.create(self.root, "exp", "the experiment file text", {"peers": 2})

    def test_create_writes_meta_and_experiment(self):
        s = self.store()
        self.assertEqual(json.loads((s.dir / "meta.json").read_text())["peers"], 2)
        self.assertEqual((s.dir / "experiment.toml").read_text(), "the experiment file text")

    def test_rows_round_trip_and_done(self):
        s = self.store()
        s.add_trial({"config_id": "c00", "trial": 0, "ok": True})
        s.add_trial({"config_id": "c00", "trial": 1, "ok": False})
        self.assertEqual([r["trial"] for r in s.rows()], [0, 1])
        self.assertEqual(s.done(), {("c00", 0), ("c00", 1)})

    def test_rows_empty_before_any_trial(self):
        self.assertEqual(self.store().rows(), [])

    def test_save_raw_is_per_config_and_trial(self):
        s = self.store()
        s.save_raw("c00", 3, {"stdout_a": "hello"})
        raw = json.loads((s.dir / "raw" / "c00" / "00003.json").read_text())
        self.assertEqual(raw["stdout_a"], "hello")

    def test_drop_unfinished_links_keeps_only_done_trials(self):
        s = self.store()
        s.add_links([{"config_id": "c00", "trial": 0, "peer": "a"},
                     {"config_id": "c00", "trial": 1, "peer": "a"}])
        s.drop_unfinished_links({("c00", 0)})          # trial 1 never finished
        kept = [json.loads(line) for line in s.links_path.read_text().splitlines()]
        self.assertEqual([r["trial"] for r in kept], [0])

    def test_write_csv_unions_columns_across_rows(self):
        s = self.store()
        s.add_trial({"config_id": "c00", "trial": 0, "a_rtt": 5.0})
        s.add_trial({"config_id": "c00", "trial": 1, "b_loss": 0.1})  # a different column
        path = s.write_csv()
        with path.open() as f:
            reader = csv.DictReader(f)
            self.assertEqual(set(reader.fieldnames), {"config_id", "trial", "a_rtt", "b_loss"})
            first = next(reader)
            self.assertEqual(first["a_rtt"], "5.0")
            self.assertEqual(first["b_loss"], "")       # missing column is blank, not an error


if __name__ == "__main__":
    unittest.main()
