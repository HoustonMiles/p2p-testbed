#!/usr/bin/env bash
# =============================================================================
# host_setup.sh - run once per boot on your Linux machine (needs sudo).
#
# Containers share the host's kernel but are not allowed to load kernel
# modules themselves, so we load the ones the routers need here.
# =============================================================================
set -euo pipefail

modules=(
  sch_netem            # tc netem: latency, jitter, loss and rate limiting
  ifb                  # IFB devices: lets us rate-limit INCOMING traffic (downlink cap)
  ip_set               # ipset: in-kernel tables of addresses/ports...
  ip_set_bitmap_port   # ...per-port table (full cone NAT type)
  ip_set_hash_ipport   # ...per (IP, port) table (restricted cone NAT type)
  xt_set               # lets iptables read/write those tables
  nf_conntrack         # connection tracking: the heart of Linux NAT
  nf_nat               # NAT itself
)

for m in "${modules[@]}"; do
  if sudo modprobe "$m" 2>/dev/null; then
    echo "loaded   $m"
  elif [[ -d "/sys/module/$m" ]]; then
    echo "built-in $m"
  else
    echo "MISSING  $m  (your kernel may not include it)"
  fi
done
