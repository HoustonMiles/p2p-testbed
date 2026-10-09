# =============================================================================
# Router image: used for both home NATs (nat-a, nat-b) and carrier-grade
# NATs (cgn-a, cgn-b). All NAT behaviour comes from the Linux kernel; this
# image only carries the tools to configure it.
# =============================================================================
FROM alpine:3.20

# iproute2 / iproute2-tc : interfaces, routes, and tc/netem link emulation
# iptables               : NAT rules (Alpine uses the nf_tables backend)
# ipset                  : per-mapping tables used by the full/restricted cone types
# conntrack-tools        : `conntrack -L` to inspect NAT mappings when debugging
# tcpdump                : packet capture when debugging
# bash                   : the configuration script uses bash features
RUN apk add --no-cache bash iproute2 iproute2-tc iptables ipset conntrack-tools tcpdump

COPY configure-router.sh /usr/local/bin/configure-router.sh
RUN chmod +x /usr/local/bin/configure-router.sh

# Configure the router, then stay alive. `init: true` in compose runs tini as
# PID 1, so `docker compose down` stops this container immediately.
CMD ["sh", "-c", "configure-router.sh && exec sleep infinity"]
