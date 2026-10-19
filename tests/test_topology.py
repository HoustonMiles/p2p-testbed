import unittest

from p2prunner import topology


class NamesTest(unittest.TestCase):
    def test_spreadsheet_style_names(self):
        self.assertEqual([topology.peer_name(k) for k in (1, 2, 26, 27, 52, 53, 255)],
                         ["a", "b", "z", "aa", "az", "ba", "iu"])

    def test_names_round_trip(self):
        for k in range(1, topology.MAX_PEERS + 1):
            self.assertEqual(topology.peer_number(topology.peer_name(k)), k)

    def test_out_of_range(self):
        for bad in (0, topology.MAX_PEERS + 1):
            with self.assertRaises(ValueError):
                topology.peer_name(bad)
        for bad in ("A", "a1", "", "iv"):
            with self.assertRaises(ValueError):
                topology.peer_number(bad)


class AddressesTest(unittest.TestCase):
    def test_every_address_is_unique(self):
        seen = {topology.STUN_IP, topology.STUN_ALT_IP, topology.RENDEZVOUS_IP, topology.RELAY_IP}
        for p in topology.peers(topology.MAX_PEERS):
            for addr in (p.peer_ip, p.router_lan_ip, p.router_carrier_ip, p.cgn_inside_ip, p.wan_ip, p.cgn_wan_ip):
                self.assertNotIn(addr, seen, f"{addr} of peer {p.name} is taken")
                seen.add(addr)

    def test_peer_c(self):
        c = topology.peer("c")
        self.assertEqual((c.peer_ip, c.router_lan_ip, c.lan_subnet), ("10.200.3.10", "10.200.3.2", "10.200.3.0/24"))
        self.assertEqual((c.public_ip(False), c.public_ip(True)), ("198.18.3.10", "198.18.3.11"))


if __name__ == "__main__":
    unittest.main()
