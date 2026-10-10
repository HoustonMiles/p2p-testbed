"""
Control of the Docker testbed through `docker compose`.

Everything here is the same command you would type by hand, run as a
subprocess from the repository root.
"""

from __future__ import annotations

import os
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from p2prunner.experiment import FACTORS

ROUTERS = ("nat-a", "nat-b", "cgn-a", "cgn-b")
PEERS = ("peer-a", "peer-b")


class TestbedError(RuntimeError):
    """The testbed could not be brought up or a command failed to run."""


class Testbed:
    def __init__(self, root: Path):
        self.root = root

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
            return subprocess.run(["docker", "compose", *args], cwd=self.root, env=full_env,
                                  capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired as e:
            raise TestbedError(f"docker compose {' '.join(args)} timed out after {timeout}s") from e

    def exec(self, service: str, command: list[str], timeout: float = 60) -> subprocess.CompletedProcess:
        """Run a command inside a running container (like `docker compose exec`)."""
        return self.compose("exec", "-T", service, *command, timeout=timeout)

    def sh(self, service: str, script: str, timeout: float = 60) -> subprocess.CompletedProcess:
        """Run a shell snippet inside a container."""
        return self.exec(service, ["sh", "-c", script], timeout=timeout)

    def parallel(self, jobs: dict[str, callable]) -> dict:
        """Run several functions at once; returns {name: result}."""
        with ThreadPoolExecutor(max_workers=len(jobs)) as pool:
            futures = {name: pool.submit(fn) for name, fn in jobs.items()}
            return {name: f.result() for name, f in futures.items()}

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------
    def up(self, env: dict[str, str]) -> None:
        """(Re)create every container with the given settings and wait until
        all of them report healthy.

        Every testbed variable is set explicitly, so leftovers in your shell
        or in a .env file can never change a configuration by accident.
        """
        full = {var: default for var, default in FACTORS.values()}
        full.update(env)
        result = self.compose("up", "-d", "--wait", "--force-recreate", "--remove-orphans",
                              env=full, timeout=300)
        if result.returncode != 0:
            raise TestbedError("testbed failed to start:\n" + result.stderr[-2000:])

    def down(self) -> None:
        self.compose("down", "-t", "1", timeout=120)

    def reset_state(self) -> None:
        """Forget everything the NATs learned in the previous trial.

        Without this, a mapping opened in trial N could still be open in
        trial N+1 and make it succeed (or fail) for the wrong reason.
          - conntrack -F : drop all NAT mappings / connection state
          - ipset flush  : empty the cone-NAT tables (full/restricted cone)
        """
        script = "conntrack -F >/dev/null 2>&1; ipset flush >/dev/null 2>&1; true"
        self.parallel({r: (lambda r=r: self.sh(r, script, timeout=30)) for r in ROUTERS})

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
