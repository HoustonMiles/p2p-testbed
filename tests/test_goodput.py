import unittest

from p2prunner.goodput import timeline, under_attack


def row(mbit: list[float], start_unix_ms: float = 1000.0, start_ms: float = 100.0) -> dict:
    return {"start_unix_ms": start_unix_ms, "series_ms": 50.0, "rx_series_start_ms": start_ms, "rx_series_mbit": mbit}


class TimelineTest(unittest.TestCase):
    def test_puts_intervals_on_the_unix_clock(self):
        self.assertEqual(timeline(row([5.0, 4.0])), [(1100.0, 5.0), (1150.0, 4.0)])

    def test_no_series(self):
        self.assertEqual(timeline({"start_unix_ms": 1000.0, "series_ms": None}), [])
        self.assertEqual(timeline(row([5.0]), side="tx"), [])


class UnderAttackTest(unittest.TestCase):
    # Intervals start at 0, 50, ..., 950 ms; the attack runs 300-600 ms.
    def points(self, mbit: list[float]) -> list[tuple[float, float]]:
        return [(i * 50.0, v) for i, v in enumerate(mbit)]

    def test_dip_and_recovery(self):
        mbit = [10, 10, 10, 10, 10, 10, 1, 0, 2, 4, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10]
        got = under_attack(self.points(mbit), 50.0, 300.0, 600.0)
        self.assertEqual(got["goodput_before_mbit"], 10)
        self.assertEqual(got["goodput_min_mbit"], 0)
        self.assertAlmostEqual(got["goodput_during_mbit"], 26 / 6)  # 1, 0, 2, 4, 9, 10: intervals from 300 to 550 ms
        self.assertEqual(got["goodput_after_mbit"], 10)
        self.assertEqual(got["recovery_ms"], 200.0)  # back at or above 9 from 500 ms, held for 3 intervals

    def test_one_good_interval_is_not_recovery(self):
        mbit = [10] * 6 + [0, 10, 0, 0, 10, 10, 10, 10]
        self.assertEqual(under_attack(self.points(mbit), 50.0, 300.0, 600.0)["recovery_ms"], 200.0)

    def test_never_dropped(self):
        self.assertEqual(under_attack(self.points([10] * 20), 50.0, 300.0, 600.0)["recovery_ms"], 0.0)

    def test_never_recovered(self):
        mbit = [10] * 6 + [0] * 14
        self.assertIsNone(under_attack(self.points(mbit), 50.0, 300.0, 600.0)["recovery_ms"])

    def test_no_data_before_the_attack(self):
        got = under_attack(self.points([0, 0, 10]), 50.0, 0.0, 100.0)
        self.assertIsNone(got["goodput_before_mbit"])
        self.assertIsNone(got["recovery_ms"])


if __name__ == "__main__":
    unittest.main()
