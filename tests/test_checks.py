import contextlib
import io
import unittest
from unittest import mock

from p2prunner import checks
from tests.fakes import Done, FakeTestbed


def run_quietly(fn, *args):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = fn(*args)
    return code, out.getvalue()


class SmokeTest(unittest.TestCase):
    def test_healthy_testbed_passes(self):
        code, out = run_quietly(checks.smoke, FakeTestbed())
        self.assertEqual(code, 0, out)
        self.assertIn("198.18.2.11", out)  # b is behind its CGN

    def test_wrong_public_ip_fails(self):
        code, out = run_quietly(checks.smoke, FakeTestbed(public={"a": "1.2.3.4"}))
        self.assertEqual(code, 1)
        self.assertIn("FAIL  STUN sees peer-a as 1.2.3.4", out)

    def test_nat_bypass_fails(self):
        code, out = run_quietly(checks.smoke, FakeTestbed(bypass=True))
        self.assertEqual(code, 1)
        self.assertEqual(out.count("FAIL  WAN host cannot reach"), 2)


class RelaySmokeTest(unittest.TestCase):
    def test_relay_is_checked(self):
        code, out = run_quietly(checks.smoke, FakeTestbed())
        self.assertEqual(code, 0, out)
        self.assertIn("the relay sees peer-a as 198.18.1.10", out)

    def test_relay_down_fails(self):
        code, out = run_quietly(checks.smoke, FakeTestbed(relay=False))
        self.assertEqual(code, 1)
        self.assertIn("no reply from the relay", out)


class VerifyTest(unittest.TestCase):
    def test_matching_behaviour_passes(self):
        code, out = run_quietly(checks.verify, FakeTestbed(), False)
        self.assertEqual(code, 0, out)

    def test_mismatch_fails(self):
        tb = FakeTestbed(behaviour={"b": ("APD", "APD")})
        code, out = run_quietly(checks.verify, tb, False)
        self.assertEqual(code, 1)
        self.assertIn("expected EI/APD", out)

    def test_cgn_makes_the_pair_stricter(self):
        # Full cone at home behind a symmetric CGN behaves like symmetric.
        tb = FakeTestbed(nat=("port_restricted", "full_cone"), cgn="symmetric",
                         behaviour={"b": ("APD", "APD")})
        code, out = run_quietly(checks.verify, tb, False)
        self.assertEqual(code, 0, out)

    def test_all_types_at_once_then_restore(self):
        tb = FakeTestbed()
        run_quietly(checks.verify, tb, True)
        cfg = tb.ups[0]
        homes, behind = cfg.peers[:4], cfg.peers[4:]
        self.assertEqual([cfg.factors[f"nat_{p.name}"] for p in homes], list(checks.TYPES))
        self.assertEqual([cfg.factors[f"cgn_{p.name}"] for p in behind], list(checks.TYPES))
        self.assertEqual(cfg.cgn_peers, {p.name for p in behind})
        self.assertEqual({cfg.factors[f"nat_{p.name}"] for p in behind}, {"full_cone"})
        self.assertEqual([u[0] for u in tb.ups[1:]], ["down", "compose"])  # plain `up` keeps the user's .env

    def test_a_lost_reply_is_tested_again(self):
        tb = FakeTestbed(behaviour={"b": [("EI", "none"), ("EI", "APD")]})
        code, out = run_quietly(checks.verify, tb, False)
        self.assertEqual(code, 0, out)

    def test_a_wrong_answer_is_not_tested_again(self):
        tb = FakeTestbed(behaviour={"b": [("EI", "AD"), ("EI", "APD")]})
        code, out = run_quietly(checks.verify, tb, False)
        self.assertEqual(code, 1)

    def test_every_running_peer_is_checked(self):
        tb = FakeTestbed(nat=("port_restricted",) * 5, behaviour={"e": ("APD", "APD")})
        code, out = run_quietly(checks.verify, tb, False)
        self.assertEqual(code, 1)
        self.assertEqual(out.count("PASS"), 4)
        self.assertIn("peer e", out)


class PreflightTest(unittest.TestCase):
    def test_reports_stopped_services(self):
        with mock.patch("subprocess.run", return_value=Done()):
            problem = checks.preflight(FakeTestbed(running=False))
        self.assertIn("nat-a", problem)

    def test_refuses_a_mix_of_two_stacks(self):
        with mock.patch("subprocess.run", return_value=Done()):
            problem = checks.preflight(FakeTestbed(stray=["nat-c", "peer-c"]))
        self.assertIn("another stack", problem)
        self.assertIn("peer-c", problem)

    def test_all_types_does_not_need_a_running_testbed(self):
        with mock.patch("subprocess.run", return_value=Done()):
            self.assertIsNone(checks.preflight(FakeTestbed(running=False), need_running=False))

    def test_reports_missing_docker(self):
        with mock.patch("subprocess.run", return_value=Done(returncode=1)):
            self.assertIn("Docker daemon", checks.preflight(FakeTestbed()))


if __name__ == "__main__":
    unittest.main()
