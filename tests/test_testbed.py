import tempfile
import unittest
from pathlib import Path

from p2prunner import compose, topology
from p2prunner.testbed import GENERATED, Testbed, TestbedError, check_arp_limit


class ArpLimitTest(unittest.TestCase):
    def limit_file(self, value: str) -> Path:
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        path = Path(d.name) / "gc_thresh3"
        path.write_text(value)
        return path

    def test_measured_in_the_testbed(self):
        # The sandbox's punches failed at 32 peers with the default 1024, not at 16.
        default = self.limit_file("1024\n")
        check_arp_limit(topology.wan_neighbours(16, 0), default)
        with self.assertRaisesRegex(TestbedError, "prepare-host.sh"):
            check_arp_limit(topology.wan_neighbours(32, 0), default)

    def test_prepare_host_covers_the_largest_testbed(self):
        script = (Path(__file__).parent.parent / "testbed/prepare-host.sh").read_text()
        limit = int(script.split("gc_thresh3=")[1].split()[0])
        check_arp_limit(topology.wan_neighbours(topology.MAX_PEERS, topology.MAX_PEERS), self.limit_file(str(limit)))

    def test_unreadable_limit_is_not_checked(self):
        check_arp_limit(10**9, Path("/nonexistent/gc_thresh3"))


class UseStackTest(unittest.TestCase):
    """Docker cannot always move a running container onto a new network,
    so a stack of another shape must stop before it is replaced."""

    def setUp(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        self.tb = Testbed(Path(d.name))
        (self.tb.root / "docker-compose.yml").write_text(compose.default())
        self.calls = []
        self.tb.compose = lambda *args, **kw: self.calls.append(args[0])

    def test_same_shape_keeps_running(self):
        self.tb.use_stack(2, {"a", "b"})
        self.tb.use_stack(2, {"a", "b"})
        self.assertEqual(self.calls, [])
        self.assertEqual((self.tb.root / GENERATED).read_text(), compose.default())

    def test_other_shape_stops_first(self):
        self.tb.use_stack(4, set())
        self.tb.use_stack(4, {"c"})
        self.assertEqual(self.calls, ["down", "down"])
        self.assertIn("cgn-c:", (self.tb.root / GENERATED).read_text())


if __name__ == "__main__":
    unittest.main()
