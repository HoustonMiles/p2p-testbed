# =============================================================================
# coturn image, run twice: the STUN server (STUN only, with RFC 5780 support;
# 198.18.0.100, alternate IP 198.18.0.103) and the TURN relay (198.18.0.102).
# Each container picks its role with its command: start-stun.sh (the
# default) or start-relay.sh.
# =============================================================================
FROM alpine:3.20

# coturn      : the server (turnserver) plus turnutils_* test clients
# iproute2    : to add the STUN server's second IP address
# iproute2-tc : tc netem, for the relay's optional extra delay
# iputils     : ping, used by the smoke test's NAT-bypass check
RUN apk add --no-cache bash coturn iproute2 iproute2-tc iputils

COPY turnserver.conf relay.conf /etc/coturn/
COPY start-stun.sh start-relay.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/start-stun.sh /usr/local/bin/start-relay.sh

CMD ["start-stun.sh"]
