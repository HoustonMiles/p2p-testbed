#!/usr/bin/env bash
# =============================================================================
# start-stun.sh
#
# Gives the STUN container its second IP address, then starts coturn on both.
# Docker Compose can only assign one IPv4 address per network, so the
# alternate address (ALT_IP) is added here by hand.
# =============================================================================
set -euo pipefail

PRIMARY_IP="${PRIMARY_IP:?PRIMARY_IP is required}"
ALT_IP="${ALT_IP:?ALT_IP is required}"

# Find the interface that holds PRIMARY_IP and its prefix length (e.g. 24).
read -r IFACE PREFIX < <(ip -o -4 addr show | awk -v want="$PRIMARY_IP" '{
  split($4, a, "/")
  if (a[1] == want) { sub(/@.*/, "", $2); print $2, a[2] }
}')
[[ -n "${IFACE:-}" ]] || { echo "[stun] no interface has $PRIMARY_IP" >&2; exit 1; }

# Add the alternate address (ignore the error if it already exists).
ip addr add "$ALT_IP/$PREFIX" dev "$IFACE" 2>/dev/null || true
echo "[stun] listening on $PRIMARY_IP and $ALT_IP ($IFACE)"

# Like every WAN host, the STUN server needs no default route; removing
# Docker's keeps traffic from leaking to the host's real network.
while ip route del default 2>/dev/null; do :; done

# exec: coturn replaces this script, so it receives stop signals directly.
exec turnserver -c /etc/coturn/turnserver.conf \
  --listening-ip "$PRIMARY_IP" --listening-ip "$ALT_IP"
