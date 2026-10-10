"""
p2prunner - runs experiments on the P2P testbed and records the results.

Modules:
  experiment  load an experiment file and expand its matrix into configurations
  testbed     bring the Docker testbed up/down and reset state between trials
  trials      the measurements a trial can run (nat_verify, ping, command)
  store       results folder: trials.jsonl, configs.jsonl, meta.json, raw logs
  cli         command line: `python -m p2prunner run|plan ...`
"""
