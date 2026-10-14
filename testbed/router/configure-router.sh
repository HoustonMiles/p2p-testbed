#!/usr/bin/env bash
# =============================================================================
# configure-router.sh
#
# Turns this container into a NAT router using the Linux kernel's own NAT
# (netfilter + conntrack), the same code that runs on most real home routers.
#
# The same script is used for two roles:
#   ROLE=home  - the subscriber's home router (nat-a / nat-b). Sits between a
#                peer's LAN and either the public WAN or a carrier network.
#                This is also where the "access link" (latency, jitter, loss,
#                bandwidth cap) is simulated.
#   ROLE=cgn   - an ISP's carrier-grade NAT (cgn-a / cgn-b). Sits between the
#                carrier network (100.64.0.0/10) and the public WAN.
#
# Everything is configured through environment variables set in
# docker-compose.yml (see the README for the full list).
# =============================================================================
set -euo pipefail

# ----------------------------------------------------------------------------
# Small helpers
# ----------------------------------------------------------------------------
log() { echo "[router:${ROLE:-?}] $*"; }
die() { echo "[router:${ROLE:-?}] ERROR: $*" >&2; exit 1; }

# Docker does not guarantee which network becomes eth0, eth1, ... so we find
# each interface by the static IP address we assigned to it in compose.
iface_for_ip() {
  ip -o -4 addr show | awk -v want="$1" '{
    split($4, a, "/")                 # "192.168.10.2/24" -> "192.168.10.2"
    if (a[1] == want) { sub(/@.*/, "", $2); print $2 }
  }'
}

# Create (or empty, if it already exists) one of our own iptables chains and
# hook it into a built-in chain exactly once. Using our own chains means we
# never flush Docker's rules (e.g. its embedded DNS rules in the nat table),
# and re-running this script is safe.
ensure_chain() {
  local table="$1" chain="$2" hook="$3"
  iptables -t "$table" -N "$chain" 2>/dev/null || iptables -t "$table" -F "$chain"
  iptables -t "$table" -C "$hook" -j "$chain" 2>/dev/null \
    || iptables -t "$table" -A "$hook" -j "$chain"
}

# ----------------------------------------------------------------------------
# 1. Work out which side is "inside" and which is "outside" for this role
# ----------------------------------------------------------------------------
ROLE="${ROLE:?ROLE must be 'home' or 'cgn'}"
NAT_TYPE="${NAT_TYPE:-port_restricted}"
INSIDE_HOST="${INSIDE_HOST:?INSIDE_HOST (the single host behind this NAT) is required}"

case "$ROLE" in
  home)
    IN_IP="${LAN_IP:?}"; IN_SUBNET="${LAN_SUBNET:?}"
    case "${UPSTREAM:-wan}" in
      # Directly on the public internet: our WAN IP is the public address.
      wan)   OUT_IP="${WAN_IP:?}";     GATEWAY="";             UNUSED_IP="${CARRIER_IP:-}" ;;
      # Behind CGNAT: we only have a private carrier address; the CGN is our
      # next hop to the internet, and our own WAN interface is switched off.
      cgnat) OUT_IP="${CARRIER_IP:?}"; GATEWAY="${CGN_IP:?}";  UNUSED_IP="${WAN_IP:-}" ;;
      *) die "UPSTREAM must be 'wan' or 'cgnat' (got '${UPSTREAM}')" ;;
    esac
    ;;
  cgn)
    IN_IP="${INSIDE_IP:?}"; IN_SUBNET="${INSIDE_SUBNET:?}"
    OUT_IP="${OUTSIDE_IP:?}"; GATEWAY=""; UNUSED_IP=""
    ;;
  *) die "ROLE must be 'home' or 'cgn' (got '$ROLE')" ;;
esac

IN_IF="$(iface_for_ip "$IN_IP")"
OUT_IF="$(iface_for_ip "$OUT_IP")"
[[ -n "$IN_IF"  ]] || die "no interface has inside IP $IN_IP"
[[ -n "$OUT_IF" ]] || die "no interface has outside IP $OUT_IP"
log "inside=$IN_IF ($IN_IP, $IN_SUBNET)  outside=$OUT_IF ($OUT_IP)  nat_type=$NAT_TYPE"

# ----------------------------------------------------------------------------
# 2. Routing
# ----------------------------------------------------------------------------
# Forwarding and rp_filter are set via `sysctls:` in compose (/proc/sys is
# read-only inside containers). Fail loudly if forwarding is somehow off.
[[ "$(cat /proc/sys/net/ipv4/ip_forward)" == "1" ]] \
  || die "net.ipv4.ip_forward is not 1; check the sysctls section in compose"

# Switch off the interface we are not using in this topology, so nothing can
# bypass the intended path (e.g. home router's WAN port while behind CGNAT).
if [[ -n "$UNUSED_IP" ]]; then
  UNUSED_IF="$(iface_for_ip "$UNUSED_IP")"
  if [[ -n "$UNUSED_IF" ]]; then
    ip link set dev "$UNUSED_IF" down
    log "disabled unused interface $UNUSED_IF ($UNUSED_IP)"
  fi
fi

# Remove Docker's default route (it points at the Docker host, which would
# leak traffic to your real network) and add ours if we have an upstream hop.
while ip route del default 2>/dev/null; do :; done
if [[ -n "$GATEWAY" ]]; then
  ip route add default via "$GATEWAY" dev "$OUT_IF"
  log "default route via $GATEWAY"
fi

# ----------------------------------------------------------------------------
# 3. NAT behaviour (mapping + filtering)
#
#   Type             Mapping (outbound)            Filtering (inbound)
#   ---------------  ----------------------------  ---------------------------
#   full_cone        endpoint-independent          anyone, on ports in use
#   restricted       endpoint-independent          IPs contacted from that port
#   port_restricted  endpoint-independent          only exact IP:port contacted
#   symmetric        new random port per dest.     only exact IP:port contacted
#
# Linux's plain MASQUERADE keeps the internal source port when it is free,
# which gives endpoint-independent mapping with port preservation. With a
# single host behind each NAT the port is always free, so the external port
# equals the internal port. That is what lets the cone types DNAT inbound
# traffic straight back to INSIDE_HOST on the same port.
# ----------------------------------------------------------------------------
ensure_chain nat    TB_PRE   PREROUTING
ensure_chain nat    TB_POST  POSTROUTING
ensure_chain mangle TB_MPOST POSTROUTING
ensure_chain filter TB_FWD   FORWARD
ensure_chain filter TB_IN    INPUT

# --- Outbound mapping ---
if [[ "$NAT_TYPE" == "symmetric" ]]; then
  # --random-fully picks a fresh random external port for every new flow,
  # so each destination sees a different port (endpoint-dependent mapping).
  iptables -t nat -A TB_POST -o "$OUT_IF" -s "$IN_SUBNET" -j MASQUERADE --random-fully
else
  iptables -t nat -A TB_POST -o "$OUT_IF" -s "$IN_SUBNET" -j MASQUERADE
fi

# --- Inbound filtering (beyond what conntrack already allows) ---
#
# The two cone types are emulated with ipset tables that remember outbound
# activity PER MAPPING, i.e. per internal UDP port, like a real NAT does:
#   full_cone : which internal ports currently have a mapping
#   restricted: which (remote IP, internal port) pairs have been contacted
# Entries expire after the conntrack UDP "stream" timeout, so a cone pinhole
# closes at the same time as the underlying mapping would. Every outbound
# packet refreshes the entry (--exist).
SET_TIMEOUT="$(cat /proc/sys/net/netfilter/nf_conntrack_udp_timeout_stream 2>/dev/null || echo 120)"

case "$NAT_TYPE" in
  full_cone)
    # Endpoint-independent filtering: once the inside host has sent from
    # port P, ANYONE may send to external port P. Ports with no mapping stay
    # closed, so a scanning attacker only finds ports actually in use.
    ipset create tb_mapped bitmap:port range 0-65535 timeout "$SET_TIMEOUT" -exist \
      || die "ipset unavailable; run scripts/host_setup.sh on the host"
    ipset flush tb_mapped
    # Outbound: remember our internal source port.
    iptables -t mangle -A TB_MPOST -o "$OUT_IF" -s "$IN_SUBNET" -p udp \
      -j SET --add-set tb_mapped src --exist --timeout "$SET_TIMEOUT"
    # Inbound: forward to the inside host if that port has a mapping.
    # (Ports are preserved, so external port == internal port.)
    iptables -t nat -A TB_PRE -i "$OUT_IF" -p udp \
      -m set --match-set tb_mapped dst \
      -j DNAT --to-destination "$INSIDE_HOST"
    ;;
  restricted)
    # Address-dependent filtering: inbound to port P is allowed from any port
    # of an IP that the inside host has contacted FROM PORT P.
    ipset create tb_contacted hash:ip,port timeout "$SET_TIMEOUT" -exist \
      || die "ipset unavailable; run scripts/host_setup.sh on the host"
    ipset flush tb_contacted
    # Outbound: remember (destination IP, our internal source port).
    iptables -t mangle -A TB_MPOST -o "$OUT_IF" -s "$IN_SUBNET" -p udp \
      -j SET --add-set tb_contacted dst,src --exist --timeout "$SET_TIMEOUT"
    # Inbound: forward if (sender IP, destination port) was remembered.
    iptables -t nat -A TB_PRE -i "$OUT_IF" -p udp \
      -m set --match-set tb_contacted src,dst \
      -j DNAT --to-destination "$INSIDE_HOST"
    ;;
  port_restricted|symmetric)
    # Nothing extra: conntrack only lets replies from the exact IP:port the
    # inside host contacted back in.
    ;;
  *) die "unknown NAT_TYPE '$NAT_TYPE'" ;;
esac

# --- Forwarding firewall ---
# Inside -> outside: always allowed.
iptables -A TB_FWD -i "$IN_IF" -o "$OUT_IF" -j ACCEPT
# Outside -> inside: only replies to existing flows, or flows the cone rules
# above deliberately forwarded (conntrack state "DNAT").
iptables -A TB_FWD -i "$OUT_IF" -o "$IN_IF" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A TB_FWD -i "$OUT_IF" -o "$IN_IF" -m conntrack --ctstate DNAT -j ACCEPT
# Everything else is dropped. This also stops a WAN host from adding a route
# to our private LAN and reaching the peer directly, which the real internet
# would never allow.
iptables -A TB_FWD -j DROP

# --- Traffic addressed to the router itself ---
# Real routers silently drop unsolicited WAN traffic instead of answering with
# "port unreachable" (which would leak information to scanners/attackers).
iptables -A TB_IN -i "$OUT_IF" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A TB_IN -i "$OUT_IF" -p icmp --icmp-type echo-request -j ACCEPT   # keep ping for debugging
iptables -A TB_IN -i "$OUT_IF" -j DROP

# ----------------------------------------------------------------------------
# 4. Access link emulation (home router only)
#
# Distance profiles are given as round-trip values in the paper. Each home
# router delays its own outgoing traffic, so each side applies HALF the RTT:
#   RTT = delay(A->B, applied at nat-a) + delay(B->A, applied at nat-b)
# Jitter is divided by sqrt(2) and loss by 2 per side, so the round trip
# roughly matches the paper's numbers.
#
# Note: netem jitter can reorder packets. That is realistic-ish for bad paths,
# but keep it in mind when testing the replay window.
# ----------------------------------------------------------------------------
if [[ "$ROLE" == "home" ]]; then
  NET_PROFILE="${NET_PROFILE:-local}"
  case "$NET_PROFILE" in
    none)          DELAY="";     JITTER="";     LOSS="" ;;
    local)         DELAY="5ms";  JITTER="";     LOSS="" ;;       # 10 ms RTT, 0% loss
    coast)         DELAY="35ms"; JITTER="7ms";  LOSS="0.25%" ;;  # 70 ms RTT, ~10 ms jitter, ~0.5% loss
    transatlantic) DELAY="75ms"; JITTER="11ms"; LOSS="0.5%" ;;   # 150 ms RTT, ~15 ms jitter, ~1% loss
    custom)        DELAY="${ONE_WAY_DELAY:-}"; JITTER="${ONE_WAY_JITTER:-}"; LOSS="${ONE_WAY_LOSS:-}" ;;
    *) die "unknown NET_PROFILE '$NET_PROFILE'" ;;
  esac
  QUEUE_PKTS="${QUEUE_PKTS:-1000}"   # router buffer size, in packets

  # --- Uplink: delay/jitter/loss + upload cap on outgoing packets ---
  netem_args=()
  [[ -n "$DELAY"  ]] && netem_args+=(delay "$DELAY")
  [[ -n "$DELAY" && -n "$JITTER" ]] && netem_args+=("$JITTER")
  [[ -n "$LOSS"   ]] && netem_args+=(loss "$LOSS")
  [[ -n "${UPLINK_MBIT:-}" ]] && netem_args+=(rate "${UPLINK_MBIT}mbit")
  if (( ${#netem_args[@]} > 0 )); then
    tc qdisc replace dev "$OUT_IF" root netem "${netem_args[@]}" limit "$QUEUE_PKTS" \
      || die "tc netem failed; run scripts/host_setup.sh on the host (needs sch_netem)"
    log "uplink on $OUT_IF: netem ${netem_args[*]}"
  fi

  # --- Downlink: download cap on incoming packets ---
  # This MUST happen before the NAT drops anything. In the real world a flood
  # fills your internet line before your router gets a chance to discard it,
  # which is exactly why port-hopping cannot stop volumetric floods. The tc
  # "ingress" hook runs before netfilter, so we redirect all incoming packets
  # through an IFB device and rate-limit them there.
  if [[ -n "${DOWNLINK_MBIT:-}" ]]; then
    ip link add tb_ifb type ifb 2>/dev/null || ip link show tb_ifb >/dev/null 2>&1 \
      || die "cannot create IFB device; run scripts/host_setup.sh on the host (needs ifb)"
    ip link set dev tb_ifb up
    tc qdisc del dev "$OUT_IF" ingress 2>/dev/null || true
    tc qdisc add dev "$OUT_IF" handle ffff: ingress
    tc filter add dev "$OUT_IF" parent ffff: protocol all prio 1 u32 match u32 0 0 \
      action mirred egress redirect dev tb_ifb
    tc qdisc replace dev tb_ifb root netem rate "${DOWNLINK_MBIT}mbit" limit "$QUEUE_PKTS" \
      || die "tc netem failed; run scripts/host_setup.sh on the host (needs sch_netem)"
    log "downlink on $OUT_IF: capped at ${DOWNLINK_MBIT} Mbit/s (via tb_ifb)"
  fi
fi

# ----------------------------------------------------------------------------
# 5. Done. The healthcheck in compose waits for this file, so
#    `docker compose up --wait` returns only once every router is configured.
# ----------------------------------------------------------------------------
[[ "${DEBUG:-0}" == "1" ]] && iptables-save
touch /tmp/ready
log "ready"
