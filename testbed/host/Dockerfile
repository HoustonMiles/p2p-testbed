# =============================================================================
# End-host image: the peers (peer-a, peer-b) and simple WAN services
# (the rendezvous server).
#
# Built from the REPOSITORY ROOT (see docker-compose.yml), so the Go code in
# p2p/ can be compiled in the first stage. .dockerignore keeps results/,
# .venv/ and other large folders out of the build.
# =============================================================================

# --- Stage 1: compile the Go tools -------------------------------------------
FROM golang:1.24-alpine AS gobuild
WORKDIR /src
COPY p2p/ ./
# CGO_ENABLED=0 gives static binaries that run on plain Alpine.
# go vet + go test run here, so an image never ships code that fails its tests.
RUN CGO_ENABLED=0 go vet ./... \
 && CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -o /out/punch ./cmd/punch \
 && CGO_ENABLED=0 go build -trimpath -o /out/rendezvous ./cmd/rendezvous

# --- Stage 2: the runtime image ----------------------------------------------
FROM alpine:3.20

# iproute2 : routes        socat   : quick UDP tests
# iputils  : ping          tcpdump : packet capture when debugging
# coturn   : only for its STUN test clients, turnutils_stunclient and
#            turnutils_natdiscovery (RFC 5780 NAT behaviour discovery)
RUN apk add --no-cache bash coturn iproute2 iputils socat tcpdump

# The raw UDP hole-punching baseline (peers) and its rendezvous server.
COPY --from=gobuild /out/punch /out/rendezvous /usr/local/bin/

COPY testbed/host/configure-host.sh /usr/local/bin/configure-host.sh
RUN chmod +x /usr/local/bin/configure-host.sh

# Set up routing first, then run the container's command.
ENTRYPOINT ["configure-host.sh"]
CMD ["sleep", "infinity"]
