#!/usr/bin/env bash
# =============================================================================
# prepare-host.sh - run once per boot on your Linux machine (needs sudo).
#
# Containers share the host's kernel but are not allowed to load kernel
# modules or change its global limits themselves, so we do that here.
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

# The kernel's ARP table is shared by all containers. On the WAN every
# router may talk to every other, so a testbed of N peers needs about
# (N + CGNs)^2 entries; the default limit of 1024 runs out above ~30 peers,
# and then punches fail silently. These cover 255 peers, each with a CGN.
sudo sysctl -q -w net.ipv4.neigh.default.gc_thresh1=65536 \
                  net.ipv4.neigh.default.gc_thresh2=262144 \
                  net.ipv4.neigh.default.gc_thresh3=524288
echo "set      ARP table limit $(sysctl -n net.ipv4.neigh.default.gc_thresh3)"
