#!/usr/bin/env bash
# =============================================================================
# start-relay.sh
#
# Starts coturn as the TURN relay. With RELAY_DELAY_MS above 0, everything
# the relay sends is first delayed by that much (netem), so the relayed path
# can model a relay that is out of the way: a relayed packet passes the
# relay once, so each direction gets the delay once and a relayed round trip
# 2 x RELAY_DELAY_MS more than the direct one.
# =============================================================================
set -euo pipefail

RELAY_IP="${RELAY_IP:?RELAY_IP is required}"
RELAY_DELAY_MS="${RELAY_DELAY_MS:-0}"

IFACE=$(ip -o -4 addr show | awk -v want="$RELAY_IP" '{
  split($4, a, "/")
  if (a[1] == want) { sub(/@.*/, "", $2); print $2 }
}')
[[ -n "$IFACE" ]] || { echo "[relay] no interface has $RELAY_IP" >&2; exit 1; }

# Like every WAN host, no default route: nothing leaks to the real network.
while ip route del default 2>/dev/null; do :; done

if [[ "$RELAY_DELAY_MS" != 0 ]]; then
  tc qdisc replace dev "$IFACE" root netem delay "${RELAY_DELAY_MS}ms" limit 10000 \
    || { echo "[relay] tc netem failed; run testbed/prepare-host.sh on the host (needs sch_netem)" >&2; exit 1; }
fi
echo "[relay] TURN on $RELAY_IP:3478 ($IFACE), extra one-way delay ${RELAY_DELAY_MS} ms"

# exec: coturn replaces this script, so it receives stop signals directly.
exec turnserver -c /etc/coturn/relay.conf --listening-ip "$RELAY_IP" --relay-ip "$RELAY_IP"
