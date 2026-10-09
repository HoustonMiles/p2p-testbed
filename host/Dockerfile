# =============================================================================
# End-host image: the peers (peer-a, peer-b) and simple WAN services (probe).
# Later steps will add the Go client to this image or build on top of it.
# =============================================================================
FROM alpine:3.20

# iproute2 : routes        socat   : quick UDP tests (and the probe's echo server)
# iputils  : ping          tcpdump : packet capture when debugging
RUN apk add --no-cache bash iproute2 iputils socat tcpdump

COPY configure-host.sh /usr/local/bin/configure-host.sh
RUN chmod +x /usr/local/bin/configure-host.sh

# Set up routing first, then run the container's command.
ENTRYPOINT ["configure-host.sh"]
CMD ["sleep", "infinity"]
