import json
import tempfile
import unittest
from pathlib import Path

from p2prunner import nat
from p2prunner.experiment import expand_matrix
from p2prunner.mesh import mesh
from p2prunner.trials import TrialContext
from tests.fakes import Done

TYPES = ("full_cone", "restricted", "port_restricted", "symmetric")


def config(**matrix):
    (cfg,) = expand_matrix({k: [v] for k, v in matrix.items()})
    return cfg


class PredictedTest(unittest.TestCase):
    """The RFC 4787 prediction, against what the testbed measured for pairs
    (punch_matrix: 24 of 32 configurations punch; Experiment A: hopping
    holds exactly where no NAT is symmetric)."""

    def test_punch_fails_only_symmetric_against_port_filtering(self):
        fails = {(a, b) for a in TYPES for b in TYPES
                 if nat.predicted(config(nat_a=a, nat_b=b), "a", "b", direct=False) == "none"}
        self.assertEqual(fails, {("symmetric", "port_restricted"), ("port_restricted", "symmetric"),
                                 ("symmetric", "symmetric")})

    def test_cgn_counts_as_its_stricter_layer(self):
        cfg = config(nat_a="symmetric", nat_b="full_cone", upstream_b="cgnat", cgn_b="port_restricted")
        self.assertEqual(nat.predicted(cfg, "a", "b", direct=False), "none")

    def test_hop_needs_both_mappings_endpoint_independent(self):
        hop = {(a, b) for a in TYPES for b in TYPES
               if nat.predicted(config(nat_a=a, nat_b=b), "a", "b", direct=True) == "hop"}
        self.assertEqual(hop, {(a, b) for a in TYPES[:3] for b in TYPES[:3]})


def temp_dir(test: unittest.TestCase) -> Path:
    return Path(test.enterContext(tempfile.TemporaryDirectory()))


def link(me: str, member: str, **columns) -> dict:
    """A punch output row of peer me for member, hop-connected by default."""
    row = {"name": me, "id": f"id-{me}", "peer_id": f"id-{member}", "members": 3, "error": "",
           "punched": True, "hop_connected": True, "start_unix_ms": 1000.0, "ready_ms": 200.0}
    row.update(columns)
    return row


class Peers:
    """Peers that print the given rows; records what the trial asked of them."""

    def __init__(self, root: Path, rows: dict[str, list[dict]], keys: tuple = ()):
        self.root, self.rows, self.commands, self.genkeys, self.at_once = root, rows, {}, [], None
        (self.root / "keys").mkdir()
        for name in keys:
            (self.root / "keys" / f"{name}.key").write_text("k")

    def sh(self, service, script, timeout=0):
        name = service.removeprefix("peer-")
        self.commands[name] = script
        return Done("".join(json.dumps(r) + "\n" for r in self.rows[name]) + "not json\n")

    def exec(self, service, command, timeout=0):
        self.genkeys.append(command)
        return Done()

    def parallel(self, jobs, all_at_once=False):
        self.at_once = all_at_once
        return {name: fn() for name, fn in jobs.items()}


class MeshTrialTest(unittest.TestCase):
    """Three peers: a-b hop-connected, a-c punched but not hopping, b-c
    failed at c's end."""

    def setUp(self):
        self.cfg = config(peers=3, nat_c="restricted")
        self.peers = Peers(temp_dir(self), {
            "a": [link("a", "b"), link("a", "c", hop_connected=False, ready_ms=None)],
            "b": [link("b", "a", start_unix_ms=1100.0, ready_ms=250.0), link("b", "c", punched=False)],
            "c": [link("c", "a", hop_connected=False, ready_ms=None),
                  link("c", "b", punched=False, error="noise handshake: not done")],
        }, keys=("a", "b"))
        self.ctx = TrialContext(trial=0, seed=77)
        self.metrics = mesh(self.peers, self.cfg, {"args": "-direct -hop-interval 1s"}, self.ctx)

    def test_every_peer_at_once_in_one_room(self):
        self.assertTrue(self.peers.at_once)
        self.assertEqual(self.peers.commands["b"],
                         "punch -name b -room 77 -peers 3 -quiet -rendezvous-timeout 15s -direct -hop-interval 1s")

    def test_keys_made_only_where_missing(self):
        self.assertEqual(self.peers.genkeys, [["punch", "-genkey", "/keys/c"]])

    def test_mesh_columns(self):
        m = self.metrics
        self.assertEqual((m["pairs"], m["pairs_usable"], m["pairs_hop"], m["pairs_punch"], m["pairs_error"]),
                         (3, 1, 1, 1, 1))
        self.assertEqual(m["time_to_usable_ms"], 1350.0 - 1000.0)  # b's end of a-b was ready last
        self.assertIsNone(m["time_to_full_mesh_ms"])
        self.assertFalse(m["ok"])

    def test_link_rows_name_both_ends(self):
        rows = {(r["peer"], r["member"]): r for r in self.ctx.links}
        self.assertEqual(len(rows), 6)
        self.assertEqual(rows["b", "c"]["outcome"], "error")
        self.assertEqual((rows["a", "b"]["outcome"], rows["a", "b"]["predicted"]), ("hop", "hop"))
        self.assertEqual(rows["c", "a"]["peer_filtering"], "AD")


class FullMeshTest(unittest.TestCase):
    def test_time_to_full_mesh_when_every_pair_is_usable(self):
        rows = {n: [link(n, m) for m in "abc" if m != n] for n in "abc"}
        rows["c"] = [dict(r, start_unix_ms=1500.0) for r in rows["c"]]
        m = mesh(Peers(temp_dir(self), rows, keys=tuple("abc")), config(peers=3), {"args": "-direct"}, TrialContext(0, 1))
        self.assertEqual((m["pairs_usable"], m["pairs_as_predicted"], m["ok"]), (3, 3, True))
        self.assertEqual(m["time_to_full_mesh_ms"], 1500.0 + 200.0 - 1000.0)



class RelayedMeshTest(unittest.TestCase):
    def test_every_pair_is_predicted_to_connect(self):
        rows = {n: [link(n, m, punched=False, hop_connected=False) for m in "abc" if m != n] for n in "abc"}
        cfg = config(peers=3, nat_a="symmetric", nat_b="symmetric")
        m = mesh(Peers(temp_dir(self), rows, keys=tuple("abc")), cfg, {"args": "-relay -direct"}, TrialContext(0, 1))
        self.assertEqual((m["pairs_relay"], m["pairs_usable"], m["pairs_as_predicted"], m["ok"]), (3, 3, 3, True))


class StaggerTest(unittest.TestCase):
    """Peers start stagger_ms apart, in name order, and wait long enough at
    the rendezvous for the last; join times count only earlier members."""

    def test_starts_are_spaced(self):
        peers = Peers(temp_dir(self), {n: [] for n in "abc"})
        mesh(peers, config(peers=3), {"args": "-pings 5", "stagger_ms": 500}, TrialContext(0, 9))
        self.assertEqual(peers.commands["a"], "punch -name a -room 9 -peers 3 -quiet -rendezvous-timeout 16s -pings 5")
        self.assertTrue(peers.commands["c"].startswith("sleep 1.000; punch -name c "))

    def test_join_time_is_from_the_newcomer_s_start(self):
        start = {"a": 1000.0, "b": 2000.0, "c": 3000.0}
        rows = {n: [link(n, m, start_unix_ms=start[n], ready_ms=300.0 if n == "c" else 100.0)
                    for m in "abc" if m != n] for n in "abc"}
        m = mesh(Peers(temp_dir(self), rows, keys=tuple("abc")), config(peers=3), {"args": "-direct"},
                 TrialContext(0, 1))
        # b joins a: ready at 2100 (a's end at 1100); c joins both: ready at 3300.
        self.assertEqual((m["join_ms_p50"], m["join_ms_max"]), (200.0, 300.0))
        self.assertEqual(m["time_to_full_mesh_ms"], 2300.0)


if __name__ == "__main__":
    unittest.main()
