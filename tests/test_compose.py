import re
import unittest
from pathlib import Path

from p2prunner import compose

ROOT = Path(__file__).resolve().parent.parent


def services(text: str) -> list[str]:
    """Top-level service names, in order (two-space indent under services:)."""
    body = text.split("\nservices:\n", 1)[1].split("\nnetworks:\n", 1)[0]
    return re.findall(r"^  ([a-z][a-z-]*):", body, re.M)


class ComposeTest(unittest.TestCase):
    def test_committed_file_is_the_generated_one(self):
        committed = (ROOT / "docker-compose.yml").read_text()
        self.assertEqual(committed, compose.default(),
                         "docker-compose.yml is out of date: regenerate it with\n"
                         "  uv run python -m p2prunner compose --peers 2 --cgn a,b > docker-compose.yml")

    def test_n_peers_with_cgns_only_where_asked(self):
        text = compose.render(4, {"b", "d"})
        self.assertEqual(services(text), ["peer-a", "nat-a", "peer-b", "nat-b", "cgn-b", "peer-c", "nat-c",
                                          "peer-d", "nat-d", "cgn-d", "stun", "rendezvous"])
        self.assertNotIn("carrier_a:", text)  # a has no carrier network (the header diagram still names one)
        self.assertIn("carrier_d:", text)

    def test_per_peer_settings(self):
        text = compose.render(3)
        for var in ("NAT_C_TYPE", "NAT_C_UPSTREAM", "NET_PROFILE_C", "UPLINK_MBIT_C", "DOWNLINK_MBIT_C"):
            self.assertIn("${" + var + ":-", text)
        self.assertIn("DEFAULT_NET_PROFILE: ${NET_PROFILE:-local}", text)
        self.assertNotIn("CGN_C_TYPE", text)  # no CGN for c

    def test_unknown_cgn_peer(self):
        with self.assertRaises(ValueError):
            compose.render(2, {"c"})

    def test_parses_as_yaml_when_available(self):
        try:
            import yaml
        except ImportError:
            self.skipTest("PyYAML not installed; the runner itself does not need it")
        doc = yaml.safe_load(compose.render(30, {"z", "ad"}))
        self.assertEqual(len(doc["services"]), 30 * 2 + 2 + 2)
        self.assertEqual(doc["services"]["nat-ad"]["networks"]["wan"]["ipv4_address"], "198.18.30.10")
        self.assertEqual(doc["services"]["cgn-ad"]["networks"]["wan"]["ipv4_address"], "198.18.30.11")


if __name__ == "__main__":
    unittest.main()
