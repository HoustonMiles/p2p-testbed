"""
Control of the Docker testbed through `docker compose`.

Everything here is the same command you would type by hand, run as a
subprocess from the repository root. A configuration's stack is written to
.testbed/compose.yml (see compose.py) and started from there; without that
file, the committed two-peer docker-compose.yml is used.
"""

from __future__ import annotations

import os
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from p2prunner import compose as compose_file
from p2prunner import topology

GENERATED = Path(".testbed/compose.yml")
MAX_PARALLEL = 32  # docker exec at once; more mostly queues up in the daemon
ARP_LIMIT = Path("/proc/sys/net/ipv4/neigh/default/gc_thresh3")


class TestbedError(RuntimeError):
    """The testbed could not be brought up or a command failed to run."""


class Testbed:
    def __init__(self, root: Path):
        self.root = root

    @property
    def compose_path(self) -> Path:
        """The stack the runner started last, else the committed one."""
        generated = self.root / GENERATED
        return generated if generated.exists() else self.root / "docker-compose.yml"

    # ------------------------------------------------------------------
    # Low-level helpers
    # ------------------------------------------------------------------
    def compose(self, *args: str, env: dict[str, str] | None = None,
                timeout: float = 120) -> subprocess.CompletedProcess:
        """Run `docker compose <args>` in the repository root."""
        full_env = dict(os.environ)
        if env:
            full_env.update(env)
        try:
            cmd = ["docker", "compose", "-f", str(self.compose_path), "--project-directory", str(self.root), *args]
            return subprocess.run(cmd, cwd=self.root, env=full_env,
                                  capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired as e:
            raise TestbedError(f"docker compose {' '.join(args)} timed out after {timeout}s") from e

    def exec(self, service: str, command: list[str], timeout: float = 60) -> subprocess.CompletedProcess:
        """Run a command inside a running container (like `docker compose exec`)."""
        return self.compose("exec", "-T", service, *command, timeout=timeout)

    def sh(self, service: str, script: str, timeout: float = 60) -> subprocess.CompletedProcess:
        """Run a shell snippet inside a container."""
        return self.exec(service, ["sh", "-c", script], timeout=timeout)

    def parallel(self, jobs: dict[str, callable], all_at_once: bool = False) -> dict:
        """Run several functions at once; returns {name: result}. At most
        MAX_PARALLEL run together, unless all_at_once (a mesh, whose peers
        must all be running at the same time)."""
        workers = len(jobs) if all_at_once else min(len(jobs), MAX_PARALLEL)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {name: pool.submit(fn) for name, fn in jobs.items()}
            return {name: f.result() for name, f in futures.items()}

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------
    def up(self, cfg) -> None:
        """(Re)create the configuration's containers (its peers, with CGNs
        for those behind one) and wait until all of them report healthy.
        A running stack of another shape is stopped first.

        cfg.env sets every testbed variable explicitly, so leftovers in your
        shell or in a .env file can never change a configuration by accident.
        """
        self.use_stack(len(cfg.peers), cfg.cgn_peers)
        result = self.compose("up", "-d", "--wait", "--force-recreate", "--remove-orphans",
                              env=cfg.env, timeout=120 + 5 * len(cfg.peers))
        if result.returncode != 0:
            # Half-started containers would break the retry the same way.
            self.compose("down", "-t", "1", "--remove-orphans", timeout=300)
            raise TestbedError("testbed failed to start:\n" + result.stderr[-2000:])

    def use_stack(self, n: int, cgn: set[str]) -> None:
        """Make the stack of peers 1..n the one compose uses. Docker cannot
        always move a running container onto a network new to it (a CGN's
        carrier network), so a running stack of another shape stops first."""
        check_arp_limit(topology.wan_neighbours(n, len(cgn)))
        text = compose_file.render(n, cgn)
        if self.compose_path.read_text() != text:
            self.compose("down", "-t", "1", "--remove-orphans", timeout=300)
        path = self.root / GENERATED
        path.parent.mkdir(exist_ok=True)
        path.write_text(text)

    def down(self) -> None:
        """Stop everything and go back to the committed two-peer stack."""
        self.compose("down", "-t", "1", "--remove-orphans", timeout=300)
        (self.root / GENERATED).unlink(missing_ok=True)

    def services(self) -> list[str]:
        return self.compose("ps", "--status", "running", "--services").stdout.split()

    def running_peers(self) -> list[str]:
        """Names of the running peers, in peer order (a, b, ..., z, aa, ...)."""
        names = [s.removeprefix("peer-") for s in self.services() if s.startswith("peer-")]
        return sorted(names, key=topology.peer_number)

    def reset_state(self) -> None:
        """Forget everything the NATs learned in the previous trial.

        Without this, a mapping opened in trial N could still be open in
        trial N+1 and make it succeed (or fail) for the wrong reason.
          - conntrack -F : drop all NAT mappings / connection state
          - ipset flush  : empty the cone-NAT tables (full/restricted cone)
        """
        script = "conntrack -F >/dev/null 2>&1; ipset flush >/dev/null 2>&1; true"
        routers = [s for s in self.services() if s.startswith(("nat-", "cgn-"))]
        self.parallel({r: (lambda r=r: self.sh(r, script, timeout=30)) for r in routers})

    # ------------------------------------------------------------------
    # Information for the results' metadata
    # ------------------------------------------------------------------
    def versions(self) -> dict:
        def out(cmd: list[str]) -> str:
            try:
                return subprocess.run(cmd, cwd=self.root, capture_output=True,
                                      text=True, timeout=30).stdout.strip()
            except (OSError, subprocess.TimeoutExpired):
                return ""
        return {
            "docker": out(["docker", "version", "--format", "{{.Server.Version}}"]),
            "compose": out(["docker", "compose", "version", "--short"]),
            "kernel": out(["uname", "-r"]),
            "git_commit": out(["git", "rev-parse", "HEAD"]),
            # Non-empty means there were uncommitted changes when the run started.
            "git_dirty": bool(out(["git", "status", "--porcelain"])),
        }


def check_arp_limit(needed: int, limit_file: Path = ARP_LIMIT) -> None:
    """Over the kernel's ARP table limit, routers silently lose track of
    each other and punches fail as if a NAT blocked them."""
    try:
        limit = int(limit_file.read_text())
    except (OSError, ValueError):
        return  # not a Linux host we can see (e.g. Docker Desktop's VM)
    if limit < needed:
        raise TestbedError(f"this testbed needs up to {needed} ARP entries but the kernel allows {limit};"
                           " run testbed/prepare-host.sh")
