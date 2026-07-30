#!/bin/sh
# entrypoint.sh -- multi-node testnet demo orchestration (docker-compose.yml
# only; a plain `docker run` uses the Dockerfile's own ENTRYPOINT unchanged).
#
# l1 node's --peers flag needs a concrete, dialable multiaddr, and that is
# unknowable ahead of time: with --identity-key the peer ID is fixed, but
# the multiaddr node1 itself prints ("p2p listening: /ip4/0.0.0.0/tcp/4001/
# p2p/<id>") is useless as a DESTINATION for a sibling to dial -- 0.0.0.0
# means "every interface" for BINDING, it is not a routable address. A
# sibling has to dial node1 by its Docker Compose service name instead
# (resolved via Docker's internal DNS), so this script rebuilds the
# multiaddr as /dns4/<SERVICE_NAME>/tcp/4001/p2p/<id> rather than trusting
# the listen address verbatim.
#
# Coordination happens over a shared volume (no other synchronization
# primitive is available between separate containers here):
#   PEER_WAIT_FILE  -- if set, block until this file is non-empty, then pass
#                      its contents to l1 node as --peers.
#   ADVERTISE_FILE  -- if set (node1 only, the seed), run l1 node in the
#                      background, watch its own stdout for the peer ID it
#                      already prints, and write the DNS-based multiaddr
#                      here once known, so siblings' PEER_WAIT_FILE loops
#                      can proceed. SERVICE_NAME must also be set.
set -eu

PEER_ARGS=""
if [ -n "${PEER_WAIT_FILE:-}" ]; then
    echo "entrypoint: waiting for $PEER_WAIT_FILE ..." >&2
    until [ -s "$PEER_WAIT_FILE" ]; do
        sleep 0.5
    done
    ADDR=$(cat "$PEER_WAIT_FILE")
    echo "entrypoint: dialing $ADDR" >&2
    # Deliberately unquoted: this is meant to word-split into two argv
    # entries ("--peers" "<addr>") when non-empty, and vanish entirely
    # (zero args) when PEER_ARGS was never set above.
    PEER_ARGS="--peers $ADDR"
fi

if [ -z "${ADVERTISE_FILE:-}" ]; then
    # shellcheck disable=SC2086
    exec l1 node --rpc-addr 0.0.0.0:8545 --listen 4001 --listen-host 0.0.0.0 $PEER_ARGS "$@"
fi

if [ -z "${SERVICE_NAME:-}" ]; then
    echo "entrypoint: ADVERTISE_FILE is set but SERVICE_NAME is not -- cannot build a dialable multiaddr" >&2
    exit 1
fi

# Cannot exec() here: we still need to watch this process's own stdout below
# before the container's job is done, so it runs in the background instead,
# with a trap to forward shutdown signals to it (a plain background job
# would otherwise not receive them).
# shellcheck disable=SC2086
l1 node --rpc-addr 0.0.0.0:8545 --listen 4001 --listen-host 0.0.0.0 $PEER_ARGS "$@" 2>&1 | tee /tmp/node.log &
NODE_PID=$!
trap 'kill "$NODE_PID" 2>/dev/null; exit 0' TERM INT

echo "entrypoint: waiting to learn our own p2p peer id..." >&2
while [ ! -s "$ADVERTISE_FILE" ]; do
    LISTEN_ADDR=$(grep -m1 '^p2p listening: ' /tmp/node.log 2>/dev/null | sed 's/^p2p listening: //') || true
    if [ -n "$LISTEN_ADDR" ]; then
        PEER_ID=$(echo "$LISTEN_ADDR" | sed -n 's#.*/p2p/##p')
        if [ -n "$PEER_ID" ]; then
            echo "/dns4/${SERVICE_NAME}/tcp/4001/p2p/${PEER_ID}" > "$ADVERTISE_FILE"
            echo "entrypoint: advertised /dns4/${SERVICE_NAME}/tcp/4001/p2p/${PEER_ID}" >&2
        fi
    fi
    sleep 0.5
done
wait "$NODE_PID"
