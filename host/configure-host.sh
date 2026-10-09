#!/usr/bin/env bash
# =============================================================================
# configure-host.sh
#
# Network setup for end hosts: the two peers, and WAN services like the probe.
#
#   GATEWAY set   -> the host sits on a private LAN; all traffic goes through
#                    that router (its home NAT).
#   GATEWAY empty -> the host is on the public WAN and needs no default route.
#
# After configuring, it runs whatever command the container was given, so the
# same image can later run the Go client, a STUN server, an attacker, etc.
# =============================================================================
set -euo pipefail

# Remove Docker's default route, which points at the Docker host and would let
# traffic skip our simulated routers entirely.
while ip route del default 2>/dev/null; do :; done

if [[ -n "${GATEWAY:-}" ]]; then
  ip route add default via "$GATEWAY"
  echo "[host] default route via $GATEWAY"
else
  echo "[host] on the public WAN, no default route"
fi

touch /tmp/ready
exec "$@"
