#!/usr/bin/env python3
"""
check_files.py - is every testbed file present and the latest version?

Run from anywhere:   python3 scripts/check_files.py

Compares a fingerprint (the start of a SHA-256 hash) of each file with the
version Claude last sent. For the notebook only the cells' code and text are
compared, so running or saving it in Jupyter doesn't count as a change.

  OK          file is present and identical
  DIFFERENT   file exists but isn't the latest version (or you edited it)
  MISSING     file isn't where it should be
"""
import hashlib
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

EXPECTED = {
    ".dockerignore": "d46d9d6e4a8c63e4",
    ".env.example": "5957daf29a3fdad0",
    ".gitignore": "0923e733359d2c8d",
    "README.md": "06eedd2ad875e4c1",
    "analysis/results.ipynb": "da1b1e7138a25b39",
    "docker-compose.yml": "6043f2c3663c6740",
    "experiments/example_command.toml": "ceb303e7cdd5556d",
    "experiments/nat_matrix.toml": "c334b36473c59375",
    "experiments/path_baseline.toml": "646676d0ad4ec18c",
    "experiments/punch_matrix.toml": "9883475faa3658e2",
    "experiments/punch_path.toml": "1cb4bd37d96d9110",
    "host/Dockerfile": "71db163f38921994",
    "host/configure-host.sh": "9436bebfe07ce13a",
    "p2prunner/__init__.py": "73ee8758e1544f53",
    "p2prunner/__main__.py": "5a1c8759a469a35d",
    "p2prunner/cli.py": "30fdbddd6145cf31",
    "p2prunner/experiment.py": "0a34f49d314e3ee0",
    "p2prunner/store.py": "87463f772bfc03db",
    "p2prunner/testbed.py": "7a7292f3062d3eb4",
    "p2prunner/trials.py": "33b5707cfe9c0adc",
    "pyproject.toml": "7081a2c052a6bc0e",
    "router/Dockerfile": "2b33cf230dff0f15",
    "router/configure-router.sh": "7f6552b3714b56f3",
    "scripts/host_setup.sh": "976dc8a7736baf6d",
    "scripts/smoke_test.sh": "87c98d5051b568ef",
    "scripts/verify_nat.sh": "33bb86ce4b4529b1",
    "stun/Dockerfile": "2e0f8a421eef9698",
    "stun/start-stun.sh": "b67b36eb3c580ec2",
    "stun/turnserver.conf": "46ffcecaf0b28717",
}

# Your own Go code: only checked for presence (you may keep changing it).
YOURS = ["baseline/go.mod", "baseline/cmd/punch/main.go", "baseline/cmd/rendezvous/main.go",
         "baseline/internal/peer/peer.go", "baseline/internal/rendezvous/server.go",
         "baseline/internal/stun/stun.go", "baseline/internal/wire/wire.go"]

# Executable bits the scripts need.
EXECUTABLE = ["scripts/host_setup.sh", "scripts/smoke_test.sh", "scripts/verify_nat.sh",
              "router/configure-router.sh", "host/configure-host.sh", "stun/start-stun.sh"]


def fingerprint(path: pathlib.Path) -> str:
    # Trailing blank lines/spaces at the end of a file don't count (editors add them).
    data = path.read_bytes().rstrip() + b"\n"
    if path.suffix == ".ipynb":
        nb = json.loads(data)
        data = json.dumps([("".join(c["source"]) if isinstance(c["source"], list) else c["source"])
                           for c in nb["cells"]]).encode()
    return hashlib.sha256(data).hexdigest()[:16]


problems = 0
print(f"Checking {ROOT}\n")
for rel, want in EXPECTED.items():
    path = ROOT / rel
    if not path.exists():
        status = "MISSING"
    elif fingerprint(path) != want:
        status = "DIFFERENT"
    else:
        status = "OK"
    problems += status != "OK"
    print(f"  {status:<10} {rel}")

print()
for rel in YOURS:
    present = (ROOT / rel).exists()
    problems += not present
    print(f"  {'OK' if present else 'MISSING':<10} {rel}  (your code: presence only)")

print()
for rel in EXECUTABLE:
    path = ROOT / rel
    if path.exists() and not path.stat().st_mode & 0o100:
        problems += 1
        print(f"  NOT EXEC   {rel}  (fix: chmod +x {rel})")

# Common download mistakes: duplicates saved next to the real file.
strays = [p for p in ROOT.rglob("*") if p.is_file() and ("(1)" in p.name or "(2)" in p.name
          or p.name in ("dockerignore", "gitignore"))]
for p in strays:
    print(f"  STRAY      {p.relative_to(ROOT)}  (a downloaded copy that wasn't renamed/moved?)")

print()
if problems == 0 and not strays:
    print("All files are present and up to date.")
else:
    print(f"{problems} problem(s) found. Replace DIFFERENT/MISSING files with the latest versions.")
sys.exit(1 if problems else 0)
