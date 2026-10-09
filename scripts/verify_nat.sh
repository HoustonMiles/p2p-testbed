#!/usr/bin/env bash
# =============================================================================
# verify_nat.sh - confirm each NAT behaves like the type it is configured as.
#
# Runs the RFC 5780 NAT behaviour discovery tests (coturn's
# turnutils_natdiscovery) from each peer against the STUN server, and
# compares the measured behaviour with what the configuration should give.
#
# Usage (from anywhere):
#   ./scripts/verify_nat.sh         check the testbed as it is running now
#   ./scripts/verify_nat.sh --all   cycle through every NAT type, both as a
#                                   home router (side A) and as a CGN (side B),
#                                   then restore the default configuration
#
# Behaviour names follow RFC 4787:
#   mapping   - does the NAT reuse the same public port for every destination?
#   filtering - who may send packets in through an open mapping?
#   EI  = endpoint-independent         (anyone / same port for everyone)
#   AD  = address-dependent            (only IPs the peer has contacted)
#   APD = address-and-port-dependent   (only exact IP:port the peer contacted)
# =============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."

STUN=203.0.113.100
TYPES=(full_cone restricted port_restricted symmetric)
FAILS=0

dc() { docker compose "$@"; }

# What each configured NAT type should measure as: "<mapping> <filtering>".
expected_for_type() {
  case "$1" in
    full_cone)       echo "EI EI" ;;
    restricted)      echo "EI AD" ;;
    port_restricted) echo "EI APD" ;;
    symmetric)       echo "APD APD" ;;
    *)               echo "? ?" ;;
  esac
}

# Strictness order, used to combine two NATs in a row (home router + CGN):
# the pair behaves like the stricter of the two, for mapping and filtering
# separately.
rank() { case "$1" in EI) echo 0 ;; AD) echo 1 ;; APD) echo 2 ;; *) echo 9 ;; esac; }
stricter() { if (( $(rank "$1") >= $(rank "$2") )); then echo "$1"; else echo "$2"; fi; }

# Turn the tool's wording into the short codes above.
to_code() {
  case "$1" in
    "Endpoint Independent")       echo EI ;;
    "Address Dependent")          echo AD ;;
    "Address and Port Dependent") echo APD ;;
    "")                           echo "none" ;;   # tool printed no verdict
    *)                            echo "?($1)" ;;
  esac
}

# Work out the expected behaviour of one side from its running containers.
expected_for_side() {
  local side="$1" home_type upstream cgn_type m f cm cf
  read -r home_type upstream < <(dc exec -T "nat-$side" sh -c 'echo $NAT_TYPE $UPSTREAM')
  read -r m f <<< "$(expected_for_type "$home_type")"
  local desc="$home_type"
  if [[ "$upstream" == "cgnat" ]]; then
    cgn_type=$(dc exec -T "cgn-$side" sh -c 'echo $NAT_TYPE')
    read -r cm cf <<< "$(expected_for_type "$cgn_type")"
    m=$(stricter "$m" "$cm"); f=$(stricter "$f" "$cf")
    desc="$home_type behind CGN $cgn_type"
  fi
  echo "$m $f $desc"
}

# Run the RFC 5780 tests from one peer and print "<mapping> <filtering>".
measure_side() {
  local side="$1" out m f
  # The tool waits for replies a filtering NAT may (correctly) never let in,
  # so cap its run time.
  out=$(dc exec -T "peer-$side" timeout 60 turnutils_natdiscovery -m -f "$STUN" 2>&1)
  m=$(sed -n 's/^NAT with \(.*\) Mapping!.*/\1/p'   <<< "$out" | head -1)
  f=$(sed -n 's/^NAT with \(.*\) Filtering!.*/\1/p' <<< "$out" | head -1)
  echo "$(to_code "$m") $(to_code "$f")"
}

# Check both sides of the running testbed (measured in parallel, ~10 s).
check_running() {
  local side tmp exp_m exp_f desc got_m got_f
  tmp=$(mktemp -d)
  for side in a b; do measure_side "$side" > "$tmp/$side" & done
  wait
  for side in a b; do
    read -r exp_m exp_f desc <<< "$(expected_for_side "$side")"
    read -r got_m got_f < "$tmp/$side"
    if [[ "$got_m" == "$exp_m" && "$got_f" == "$exp_f" ]]; then
      printf '  PASS  side %s  %-38s mapping=%-4s filtering=%s\n' "${side^^}" "$desc" "$got_m" "$got_f"
    else
      printf '  FAIL  side %s  %-38s mapping=%s (expected %s) filtering=%s (expected %s)\n' \
        "${side^^}" "$desc" "$got_m" "$exp_m" "$got_f" "$exp_f"
      FAILS=$((FAILS + 1))
    fi
  done
  rm -rf "$tmp"
}

# --- Preflight: Docker and the testbed must be running ---
if ! docker info >/dev/null 2>&1; then
  echo "Cannot reach the Docker daemon. Is the docker service running?"; exit 2
fi

if [[ "${1:-}" == "--all" ]]; then
  # Each run tests one NAT type twice: as side A's home router, and as side
  # B's CGN (with a full cone home router in front, so the CGN decides).
  for t in "${TYPES[@]}"; do
    echo "Configuration: $t"
    if ! env NAT_A_TYPE="$t" NAT_A_UPSTREAM=wan \
             NAT_B_TYPE=full_cone NAT_B_UPSTREAM=cgnat CGN_B_TYPE="$t" \
             docker compose up -d --wait --force-recreate >/dev/null 2>&1; then
      echo "  FAIL  testbed did not start (see: docker compose logs)"; FAILS=$((FAILS + 1)); continue
    fi
    check_running
  done
  echo "Restoring the default configuration..."
  docker compose up -d --wait --force-recreate >/dev/null 2>&1
else
  for svc in nat-a nat-b peer-a peer-b stun; do
    if [[ -z "$(dc ps -q --status running "$svc" 2>/dev/null)" ]]; then
      echo "Service '$svc' is not running. Start the testbed first:"
      echo "  docker compose up -d --build --wait"; exit 2
    fi
  done
  check_running
fi

echo
if (( FAILS == 0 )); then echo "All NAT behaviour checks passed."; else echo "$FAILS check(s) failed."; exit 1; fi
