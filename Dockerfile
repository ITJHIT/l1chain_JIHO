# Build the l1 node binary and run it as a mining node with an RPC endpoint.
#
#   docker build -t l1chain .
#   docker run -p 8545:8545 -p 4001:4001 l1chain \
#       --genesis-timestamp 1750000000 --difficulty 10 --mine-interval 5s
#
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/l1 ./cmd/l1

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/l1 /usr/local/bin/l1
EXPOSE 8545 4001
# RPC on 0.0.0.0 so it is reachable from outside the container; libp2p on 4001.
ENTRYPOINT ["l1", "node", "--rpc-addr", "0.0.0.0:8545", "--listen", "4001"]
CMD ["--difficulty", "10", "--mine-interval", "5s"]
