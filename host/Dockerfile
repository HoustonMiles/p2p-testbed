# =============================================================================
# End-host image: the peers (peer-a, peer-b), and any simple WAN hosts.
# Later steps will add the Go client to this image or build on top of it.
# =============================================================================
FROM alpine:3.20

# iproute2 : routes        socat   : quick UDP tests
# iputils  : ping          tcpdump : packet capture when debugging
# coturn   : only for its STUN test clients, turnutils_stunclient and
#            turnutils_natdiscovery (RFC 5780 NAT behaviour discovery)
RUN apk add --no-cache bash coturn iproute2 iputils socat tcpdump

COPY configure-host.sh /usr/local/bin/configure-host.sh
RUN chmod +x /usr/local/bin/configure-host.sh

# Set up routing first, then run the container's command.
ENTRYPOINT ["configure-host.sh"]
CMD ["sleep", "infinity"]
