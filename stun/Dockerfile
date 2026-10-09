# =============================================================================
# STUN server image (coturn in STUN-only mode, with RFC 5780 support).
# Lives on the WAN at 203.0.113.100, with 203.0.113.103 as its alternate IP.
# =============================================================================
FROM alpine:3.20

# coturn   : the STUN server (turnserver) plus turnutils_* test clients
# iproute2 : to add the second IP address
# iputils  : ping, used by the smoke test's NAT-bypass check
RUN apk add --no-cache bash coturn iproute2 iputils

COPY turnserver.conf /etc/coturn/turnserver.conf
COPY start-stun.sh /usr/local/bin/start-stun.sh
RUN chmod +x /usr/local/bin/start-stun.sh

CMD ["start-stun.sh"]
