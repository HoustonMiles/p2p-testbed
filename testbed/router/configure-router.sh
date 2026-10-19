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

main() {
  ROLE="${ROLE:?ROLE must be 'home' or 'cgn'}"
  NAT_TYPE="${NAT_TYPE:-port_restricted}"
  INSIDE_HOST="${INSIDE_HOST:?INSIDE_HOST (the single host behind this NAT) is required}"

  resolve_role
  find_interfaces
  setup_routing
  setup_nat
  setup_firewall
  [[ "$ROLE" == "home" ]] && setup_link

  # The healthcheck in compose waits for this file, so `docker compose up
  # --wait` returns only once every router is configured.
  [[ "${DEBUG:-0}" == "1" ]] && iptables-save
  touch /tmp/ready
  log "ready"
}

log() { echo "[router:${ROLE:-?}] $*"; }
die() { echo "[router:${ROLE:-?}] ERROR: $*" >&2; exit 1; }

# Docker does not guarantee which network becomes eth0, eth1, ... so each
# interface is found by the static IP address compose assigned to it.
iface_for_ip() {
  ip -o -4 addr show | awk -v want="$1" '{
    split($4, a, "/")                 # "192.168.10.2/24" -> "192.168.10.2"
    if (a[1] == want) { sub(/@.*/, "", $2); print $2 }
  }'
}

# Creates (or empties) one of our own iptables chains and hooks it into a
# built-in chain exactly once. Own chains mean Docker's rules (e.g. its
# embedded DNS in the nat table) are never flushed, and re-running is safe.
ensure_chain() {
  local table="$1" chain="$2" hook="$3"
  iptables -t "$table" -N "$chain" 2>/dev/null || iptables -t "$table" -F "$chain"
  iptables -t "$table" -C "$hook" -j "$chain" 2>/dev/null \
    || iptables -t "$table" -A "$hook" -j "$chain"
}

# ----------------------------------------------------------------------------
# Inside and outside for this role
# ----------------------------------------------------------------------------
# Sets IN_IP, IN_SUBNET, OUT_IP, GATEWAY (next hop, or empty) and UNUSED_IP
# (an interface to switch off, or empty).
resolve_role() {
  case "$ROLE" in
    home) resolve_home ;;
    cgn)
      IN_IP="${INSIDE_IP:?}"; IN_SUBNET="${INSIDE_SUBNET:?}"
      OUT_IP="${OUTSIDE_IP:?}"; GATEWAY=""; UNUSED_IP=""
      ;;
    *) die "ROLE must be 'home' or 'cgn' (got '$ROLE')" ;;
  esac
}

resolve_home() {
  IN_IP="${LAN_IP:?}"; IN_SUBNET="${LAN_SUBNET:?}"
  case "${UPSTREAM:-wan}" in
    # Directly on the public internet: our WAN IP is the public address.
    wan)   OUT_IP="${WAN_IP:?}";     GATEWAY="";            UNUSED_IP="${CARRIER_IP:-}" ;;
    # Behind CGNAT: only a private carrier address; the CGN is the next hop
    # to the internet, and our own WAN interface is switched off.
    cgnat) OUT_IP="${CARRIER_IP:?}"; GATEWAY="${CGN_IP:?}"; UNUSED_IP="${WAN_IP:-}" ;;
    *) die "UPSTREAM must be 'wan' or 'cgnat' (got '${UPSTREAM}')" ;;
  esac
}

find_interfaces() {
  IN_IF="$(iface_for_ip "$IN_IP")"
  OUT_IF="$(iface_for_ip "$OUT_IP")"
  [[ -n "$IN_IF"  ]] || die "no interface has inside IP $IN_IP"
  [[ -n "$OUT_IF" ]] || die "no interface has outside IP $OUT_IP"
  log "inside=$IN_IF ($IN_IP, $IN_SUBNET)  outside=$OUT_IF ($OUT_IP)  nat_type=$NAT_TYPE"
}

# ----------------------------------------------------------------------------
# Routing
# ----------------------------------------------------------------------------
setup_routing() {
  # Forwarding and rp_filter are set via `sysctls:` in compose (/proc/sys is
  # read-only inside containers); fail loudly if forwarding is off.
  [[ "$(cat /proc/sys/net/ipv4/ip_forward)" == "1" ]] \
    || die "net.ipv4.ip_forward is not 1; check the sysctls section in compose"
  disable_unused_interface

  # Docker's default route points at the Docker host and would leak traffic
  # to the real network; ours goes via the upstream hop, if there is one.
  while ip route del default 2>/dev/null; do :; done
  if [[ -n "$GATEWAY" ]]; then
    ip route add default via "$GATEWAY" dev "$OUT_IF"
    log "default route via $GATEWAY"
  fi
}

# Switches off the interface this topology does not use, so nothing can
# bypass the intended path (e.g. the home router's WAN port behind CGNAT).
disable_unused_interface() {
  [[ -n "$UNUSED_IP" ]] || return 0
  local unused_if
  unused_if="$(iface_for_ip "$UNUSED_IP")"
  [[ -n "$unused_if" ]] || return 0
  ip link set dev "$unused_if" down
  log "disabled unused interface $unused_if ($UNUSED_IP)"
}

# ----------------------------------------------------------------------------
# NAT behaviour (mapping + filtering)
#
#   Type             Mapping (outbound)            Filtering (inbound)
#   ---------------  ----------------------------  ---------------------------
#   full_cone        endpoint-independent          anyone, on ports in use
#   restricted       endpoint-independent          IPs contacted from that port
#   port_restricted  endpoint-independent          only exact IP:port contacted
#   symmetric        new random port per dest.     only exact IP:port contacted
#
# Plain MASQUERADE keeps the internal source port when it is free, which
# gives endpoint-independent mapping with port preservation. With a single
# host behind each NAT the port is always free, so the external port equals
# the internal one; that lets the cone types DNAT inbound traffic straight
# back to INSIDE_HOST on the same port.
# ----------------------------------------------------------------------------
setup_nat() {
  ensure_chain nat    TB_PRE   PREROUTING
  ensure_chain nat    TB_POST  POSTROUTING
  ensure_chain mangle TB_MPOST POSTROUTING
  ensure_chain filter TB_FWD   FORWARD
  ensure_chain filter TB_IN    INPUT
  setup_mapping
  setup_cone_filtering
}

setup_mapping() {
  if [[ "$NAT_TYPE" == "symmetric" ]]; then
    # A fresh random external port for every new flow, so each destination
    # sees a different port (endpoint-dependent mapping).
    iptables -t nat -A TB_POST -o "$OUT_IF" -s "$IN_SUBNET" -j MASQUERADE --random-fully
  else
    iptables -t nat -A TB_POST -o "$OUT_IF" -s "$IN_SUBNET" -j MASQUERADE
  fi
}

# The cone types remember outbound activity PER MAPPING (per internal UDP
# port), like a real NAT, in ipset tables that expire with the conntrack
# UDP "stream" timeout, so a pinhole closes when the mapping would. Every
# outbound packet refreshes its entry (--exist).
setup_cone_filtering() {
  SET_TIMEOUT="$(cat /proc/sys/net/netfilter/nf_conntrack_udp_timeout_stream 2>/dev/null || echo 120)"
  case "$NAT_TYPE" in
    full_cone)  full_cone_filter ;;
    restricted) restricted_filter ;;
    port_restricted|symmetric) ;;  # conntrack alone admits only the exact IP:port contacted
    *) die "unknown NAT_TYPE '$NAT_TYPE'" ;;
  esac
}

# Endpoint-independent filtering: once the inside host has sent from port
# P, ANYONE may send to P. Ports with no mapping stay closed, so a scanning
# attacker only finds ports in use.
full_cone_filter() {
  ipset create tb_mapped bitmap:port range 0-65535 timeout "$SET_TIMEOUT" -exist \
    || die "ipset unavailable; run testbed/load-kernel-modules.sh on the host"
  ipset flush tb_mapped
  iptables -t mangle -A TB_MPOST -o "$OUT_IF" -s "$IN_SUBNET" -p udp \
    -j SET --add-set tb_mapped src --exist --timeout "$SET_TIMEOUT"
  # Ports are preserved, so external port == internal port.
  iptables -t nat -A TB_PRE -i "$OUT_IF" -p udp \
    -m set --match-set tb_mapped dst \
    -j DNAT --to-destination "$INSIDE_HOST"
}

# Address-dependent filtering: inbound to port P is allowed from any port
# of an IP the inside host has contacted FROM PORT P.
restricted_filter() {
  ipset create tb_contacted hash:ip,port timeout "$SET_TIMEOUT" -exist \
    || die "ipset unavailable; run testbed/load-kernel-modules.sh on the host"
  ipset flush tb_contacted
  iptables -t mangle -A TB_MPOST -o "$OUT_IF" -s "$IN_SUBNET" -p udp \
    -j SET --add-set tb_contacted dst,src --exist --timeout "$SET_TIMEOUT"
  iptables -t nat -A TB_PRE -i "$OUT_IF" -p udp \
    -m set --match-set tb_contacted src,dst \
    -j DNAT --to-destination "$INSIDE_HOST"
}

# ----------------------------------------------------------------------------
# Firewall
# ----------------------------------------------------------------------------
setup_firewall() {
  # Inside -> outside is always allowed. Outside -> inside only for replies
  # to existing flows, or flows the cone rules forwarded (state DNAT).
  # Everything else is dropped, which also stops a WAN host from routing to
  # our private LAN directly, as the real internet never could.
  iptables -A TB_FWD -i "$IN_IF" -o "$OUT_IF" -j ACCEPT
  iptables -A TB_FWD -i "$OUT_IF" -o "$IN_IF" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  iptables -A TB_FWD -i "$OUT_IF" -o "$IN_IF" -m conntrack --ctstate DNAT -j ACCEPT
  iptables -A TB_FWD -j DROP

  # Traffic to the router itself: real routers drop unsolicited WAN traffic
  # silently rather than answer "port unreachable", which leaks to scanners.
  iptables -A TB_IN -i "$OUT_IF" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  iptables -A TB_IN -i "$OUT_IF" -p icmp --icmp-type echo-request -j ACCEPT   # ping, for debugging
  iptables -A TB_IN -i "$OUT_IF" -j DROP
}

# ----------------------------------------------------------------------------
# Access link emulation (home router only)
#
# The profiles are round-trip values. Each home router delays only its own
# outgoing traffic, so each side applies HALF the RTT, jitter divided by
# sqrt(2) and loss by 2, so the round trip matches the paper's numbers.
# netem jitter can reorder packets: realistic for bad paths, and worth
# remembering when testing the replay window.
# ----------------------------------------------------------------------------
setup_link() {
  link_profile
  QUEUE_PKTS="${QUEUE_PKTS:-1000}"   # router buffer size, in packets
  setup_uplink
  [[ -n "${DOWNLINK_MBIT:-}" ]] && setup_downlink
  return 0
}

# Sets DELAY, JITTER and LOSS (one way) from NET_PROFILE.
link_profile() {
  NET_PROFILE="${NET_PROFILE:-local}"
  case "$NET_PROFILE" in
    none)          DELAY="";     JITTER="";     LOSS="" ;;
    local)         DELAY="5ms";  JITTER="";     LOSS="" ;;       # 10 ms RTT, 0% loss
    coast)         DELAY="35ms"; JITTER="7ms";  LOSS="0.25%" ;;  # 70 ms RTT, ~10 ms jitter, ~0.5% loss
    transatlantic) DELAY="75ms"; JITTER="11ms"; LOSS="0.5%" ;;   # 150 ms RTT, ~15 ms jitter, ~1% loss
    custom)        DELAY="${ONE_WAY_DELAY:-}"; JITTER="${ONE_WAY_JITTER:-}"; LOSS="${ONE_WAY_LOSS:-}" ;;
    *) die "unknown NET_PROFILE '$NET_PROFILE'" ;;
  esac
}

# Delay, jitter, loss and the upload cap, on outgoing packets.
setup_uplink() {
  local args=()
  [[ -n "$DELAY"  ]] && args+=(delay "$DELAY")
  [[ -n "$DELAY" && -n "$JITTER" ]] && args+=("$JITTER")
  [[ -n "$LOSS"   ]] && args+=(loss "$LOSS")
  [[ -n "${UPLINK_MBIT:-}" ]] && args+=(rate "${UPLINK_MBIT}mbit")
  (( ${#args[@]} > 0 )) || return 0
  tc qdisc replace dev "$OUT_IF" root netem "${args[@]}" limit "$QUEUE_PKTS" \
    || die "tc netem failed; run testbed/load-kernel-modules.sh on the host (needs sch_netem)"
  log "uplink on $OUT_IF: netem ${args[*]}"
}

# The download cap MUST apply before the NAT drops anything: a real flood
# fills the line before the router can discard it, which is why
# port-hopping cannot stop volumetric floods. The tc ingress hook runs
# before netfilter, so incoming packets are redirected through an IFB
# device and rate-limited there.
setup_downlink() {
  ip link add tb_ifb type ifb 2>/dev/null || ip link show tb_ifb >/dev/null 2>&1 \
    || die "cannot create IFB device; run testbed/load-kernel-modules.sh on the host (needs ifb)"
  ip link set dev tb_ifb up
  tc qdisc del dev "$OUT_IF" ingress 2>/dev/null || true
  tc qdisc add dev "$OUT_IF" handle ffff: ingress
  tc filter add dev "$OUT_IF" parent ffff: protocol all prio 1 u32 match u32 0 0 \
    action mirred egress redirect dev tb_ifb
  tc qdisc replace dev tb_ifb root netem rate "${DOWNLINK_MBIT}mbit" limit "$QUEUE_PKTS" \
    || die "tc netem failed; run testbed/load-kernel-modules.sh on the host (needs sch_netem)"
  log "downlink on $OUT_IF: capped at ${DOWNLINK_MBIT} Mbit/s (via tb_ifb)"
}

main "$@"
