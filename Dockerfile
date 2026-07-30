# Build the l1 node binary and run it as a mining node with an RPC endpoint.
#
#   docker build -t l1chain .
#   docker run -p 8545:8545 -p 4001:4001 l1chain \
#       --genesis-timestamp 1750000000 --difficulty 10 --mine-interval 5s
#
# For a multi-node testnet, see docker-compose.yml -- it overrides
# ENTRYPOINT to docker/entrypoint.sh for peer-discovery orchestration a
# single-node run does not need; this file's own ENTRYPOINT/CMD are
# untouched by that and still work exactly as before for `docker run`.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/l1 ./cmd/l1

# debian:12-slim, not distroless: docker-compose.yml's multi-node demo needs
# a shell (docker/entrypoint.sh) to discover a sibling's p2p peer ID via a
# shared volume -- a libp2p peer ID is not knowable to another service's
# static config until the process has already started and printed it.
FROM debian:12-slim
COPY --from=build /out/l1 /usr/local/bin/l1
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
EXPOSE 8545 4001
# RPC on 0.0.0.0 so it is reachable from outside the container; libp2p on 4001.
ENTRYPOINT ["l1", "node", "--rpc-addr", "0.0.0.0:8545", "--listen", "4001"]
CMD ["--difficulty", "10", "--mine-interval", "5s"]
