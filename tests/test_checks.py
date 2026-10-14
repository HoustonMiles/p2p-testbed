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
        self.assertIn("203.0.113.21", out)  # B is behind its CGN

    def test_wrong_public_ip_fails(self):
        code, out = run_quietly(checks.smoke, FakeTestbed(public={"a": "1.2.3.4"}))
        self.assertEqual(code, 1)
        self.assertIn("FAIL  STUN sees peer-a as 1.2.3.4", out)

    def test_nat_bypass_fails(self):
        code, out = run_quietly(checks.smoke, FakeTestbed(bypass=True))
        self.assertEqual(code, 1)
        self.assertEqual(out.count("FAIL  WAN host cannot reach"), 2)


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

    def test_all_types_in_order_then_restore(self):
        tb = FakeTestbed()
        run_quietly(checks.verify, tb, True)
        set_types = [u["NAT_A_TYPE"] for u in tb.ups if isinstance(u, dict)]
        self.assertEqual(set_types, list(checks.TYPES))
        for env in tb.ups[:-1]:
            self.assertEqual((env["NAT_B_UPSTREAM"], env["CGN_B_TYPE"]), ("cgnat", env["NAT_A_TYPE"]))
        self.assertEqual(tb.ups[-1][0], "compose")  # plain `up` keeps the user's .env


class PreflightTest(unittest.TestCase):
    def test_reports_stopped_services(self):
        with mock.patch("subprocess.run", return_value=Done()):
            problem = checks.preflight(FakeTestbed(running=False))
        self.assertIn("nat-a", problem)

    def test_all_types_does_not_need_a_running_testbed(self):
        with mock.patch("subprocess.run", return_value=Done()):
            self.assertIsNone(checks.preflight(FakeTestbed(running=False), need_running=False))

    def test_reports_missing_docker(self):
        with mock.patch("subprocess.run", return_value=Done(returncode=1)):
            self.assertIn("Docker daemon", checks.preflight(FakeTestbed()))


if __name__ == "__main__":
    unittest.main()
