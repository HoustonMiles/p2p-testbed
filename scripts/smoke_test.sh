#!/usr/bin/env bash
# =============================================================================
# smoke_test.sh - quick sanity check that the testbed is wired correctly.
#
# Run from anywhere after the testbed is up:
#   docker compose up -d --build --wait
#   ./scripts/smoke_test.sh
#
# Checks, for each side:
#   1. The peer can reach the internet (the probe on the WAN).
#   2. The probe sees the expected PUBLIC address, i.e. NAT actually happened
#      (the home router's WAN IP, or the CGN's IP when behind CGNAT).
#   3. A WAN host cannot bypass the NAT by routing straight to the private LAN.
#
# Full NAT-type verification (mapping/filtering behaviour) comes in step 3.
# =============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."

PROBE=203.0.113.100
SRC_PORT=40000
FAILS=0

dc() { docker compose "$@"; }
pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; FAILS=$((FAILS + 1)); }

check_side() {
  local side="$1" peer_ip="$2" wan_ip="$3" cgn_wan_ip="$4"
  local upstream nat_type expected observed obs_ip obs_port first_hop
  upstream=$(dc exec -T "nat-$side" sh -c 'echo $UPSTREAM')
  nat_type=$(dc exec -T "nat-$side" sh -c 'echo $NAT_TYPE')
  echo "Side ${side^^}: nat_type=$nat_type upstream=$upstream"

  # Expected public IP: the home router's own WAN IP, or the CGN's when behind CGNAT.
  if [[ "$upstream" == "cgnat" ]]; then expected="$cgn_wan_ip"; else expected="$wan_ip"; fi

  # 1 + 2: ask the probe what address it sees us coming from.
  # The probe replies "IP PORT".
  observed=$(dc exec -T "peer-$side" sh -c \
    "echo hello | socat -T 2 - UDP:$PROBE:9000,sourceport=$SRC_PORT" 2>/dev/null | tr -d '\r\n')
  if [[ -z "$observed" ]]; then
    fail "peer-$side got no reply from the probe"
  else
    read -r obs_ip obs_port <<< "$observed"
    observed="$obs_ip:$obs_port"
    if [[ "$obs_ip" == "$expected" ]]; then
      pass "probe sees peer-$side as $observed (public IP $expected)"
    else
      fail "probe sees peer-$side as $observed, expected IP $expected"
    fi
    # Informational: cone NATs should keep the port, symmetric NATs should not.
    if [[ "$obs_port" == "$SRC_PORT" ]]; then echo "        source port preserved ($SRC_PORT)"
    else echo "        source port changed $SRC_PORT -> $obs_port"; fi
  fi

  # 3: try to reach the private peer directly from the WAN. The first hop is
  # whatever router holds this side's public IP.
  first_hop="$expected"
  if ! dc exec -T probe ip route replace "${peer_ip%.*}.0/24" via "$first_hop" >/dev/null 2>&1; then
    fail "could not add the test route on the probe (check skipped)"
  elif dc exec -T probe ping -c 1 -W 1 "$peer_ip" >/dev/null 2>&1; then
    fail "WAN host reached private $peer_ip directly (NAT bypass!)"
  else
    pass "WAN host cannot reach private $peer_ip directly"
  fi
  dc exec -T probe ip route del "${peer_ip%.*}.0/24" >/dev/null 2>&1
}

# --- Preflight: make sure Docker and the testbed are actually running ---
# Without this, a missing daemon makes the bypass check "pass" for the wrong
# reason (the ping fails because nothing exists, not because the NAT blocked it).
if ! docker info >/dev/null 2>&1; then
  echo "Cannot reach the Docker daemon. Is the docker service running?"; exit 2
fi
for svc in nat-a nat-b peer-a peer-b probe; do
  if [[ -z "$(dc ps -q --status running "$svc" 2>/dev/null)" ]]; then
    echo "Service '$svc' is not running. Start the testbed first:"
    echo "  docker compose up -d --build --wait"; exit 2
  fi
done

check_side a 192.168.10.10 203.0.113.10 203.0.113.11
check_side b 192.168.20.10 203.0.113.20 203.0.113.21

echo
if (( FAILS == 0 )); then echo "All checks passed."; else echo "$FAILS check(s) failed."; exit 1; fi
