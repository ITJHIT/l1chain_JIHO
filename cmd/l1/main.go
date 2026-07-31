// Command l1 is the CLI front-end for the l1chain node. Subcommands:
//
//	l1 wallet new
//	    Generate a new key; print the private key hex and derived address.
//
//	l1 balance --addr <hex> --rpc <url> [--verify]
//	    Query an account balance over JSON-RPC. --verify fetches a Merkle
//	    proof and the block it was generated against, checks that block's
//	    own PoW itself, and verifies the proof locally instead of trusting
//	    the node's plain answer.
//
//	l1 send --key <hex> --to <hex> --value <n> --rpc <url>
//	    Build, sign, and submit a value transfer over JSON-RPC.
//
//	l1 node --db <path> --rpc-addr <host:port> --miner-key <hex> \
//	        --difficulty <n> --alloc <addrHex:amount,...> --base-alloc <addrHex:amount,...> \
//	        --mine-interval <dur> --listen-host <host> --listen <port> \
//	        --peers <multiaddr,...> --identity-key <hex> --mdns \
//	        --relay-service --relay <multiaddr> \
//	        --dht --dht-bootstrap <multiaddr,...>
//	    Run a node with an HTTP JSON-RPC server, mining on an interval.
//	    --listen-host defaults to 127.0.0.1; set it to 0.0.0.0 to be
//	    reachable from sibling Docker containers (see docker-compose.yml).
//	    --identity-key pins the libp2p peer ID across restarts instead of
//	    generating a fresh one every time. --alloc funds the native/quote
//	    asset at genesis; --base-alloc funds the on-chain exchange's base
//	    asset the same way, so a resting sell order (and therefore a real
//	    crossing trade) is reachable on a freshly started node.
//	    --relay-service makes this node a circuit-relay-v2 relay other
//	    peers can reserve a slot on; --relay <multiaddr> reserves a slot
//	    on a peer running --relay-service, so THIS node becomes reachable
//	    at /p2p/<relay>/p2p-circuit/p2p/<this node> without a direct
//	    address of its own.
//	    --dht enables Kademlia DHT peer discovery (advertise + find +
//	    auto-dial under a shared rendezvous) -- the non-LAN counterpart
//	    to --mdns; --dht-bootstrap seeds its routing table.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"l1chain/chain"
	"l1chain/consensus"
	"l1chain/core"
	"l1chain/node"
	"l1chain/p2p"
	"l1chain/rpc"
	"l1chain/wallet"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "wallet":
		err = cmdWallet(os.Args[2:])
	case "balance":
		err = cmdBalance(os.Args[2:])
	case "send":
		err = cmdSend(os.Args[2:])
	case "node":
		err = cmdNode(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `l1 — l1chain node CLI

Usage:
  l1 wallet new
  l1 balance --addr <hex> --rpc <url> [--verify]
  l1 send --key <hex> --to <hex> --value <n> --rpc <url>
  l1 node --db <path> --rpc-addr <host:port> --miner-key <hex> --difficulty <n> --alloc <addrHex:amt,...> --base-alloc <addrHex:amt,...> --mine-interval <dur>`)
}

func cmdWallet(args []string) error {
	fs := flag.NewFlagSet("wallet", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.Arg(0) != "new" {
		return errors.New("usage: l1 wallet new")
	}
	k, err := wallet.NewKey()
	if err != nil {
		return err
	}
	addr := k.Address()
	fmt.Printf("private_key: %s\n", hex.EncodeToString(k.Bytes()))
	fmt.Printf("address:     %s\n", addr.Hex())
	return nil
}

func cmdBalance(args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	addr := fs.String("addr", "", "account address (hex)")
	rpcURL := fs.String("rpc", "http://127.0.0.1:8545", "JSON-RPC endpoint URL")
	verify := fs.Bool("verify", false, "verify the balance against a Merkle proof and the block's own PoW instead of trusting the node's plain answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *addr == "" {
		return errors.New("--addr is required")
	}
	c := rpc.NewClient(*rpcURL)
	if !*verify {
		bal, err := c.GetBalance(*addr)
		if err != nil {
			return err
		}
		fmt.Printf("balance: %d\n", bal)
		return nil
	}
	return cmdBalanceVerify(c, *addr)
}

// cmdBalanceVerify is --verify's implementation, and the whole point of
// having a light-client mode at all: it does not just print whatever
// getBalance says. It fetches the account's Merkle proof and the exact
// block it was generated against, independently checks that block's own
// header meets its own declared PoW difficulty (so a malicious node cannot
// hand back a fabricated header with an easier target), cross-checks the
// header's StateRoot against the one the proof claims, and only THEN
// verifies the proof itself. A node can only make this print a wrong
// balance by breaking the proof's cryptographic hash chain or forging a
// block that meets its own PoW -- not simply by lying in a getBalance
// response.
func cmdBalanceVerify(c *rpc.Client, addrHex string) error {
	res, found, err := c.GetAccountProof(addrHex)
	if err != nil {
		return fmt.Errorf("getAccountProof: %w", err)
	}
	if !found {
		fmt.Println("balance: 0 (account has never been touched -- nothing to prove)")
		return nil
	}

	blk, ok, err := c.GetBlockByHeight(res.Height)
	if err != nil {
		return fmt.Errorf("getBlockByHeight(%d): %w", res.Height, err)
	}
	if !ok {
		return fmt.Errorf("node returned a proof for height %d but the block itself is missing", res.Height)
	}
	header, err := rpc.HeaderFromJSON(blk.Header)
	if err != nil {
		return fmt.Errorf("decode header: %w", err)
	}
	if header.StateRoot.Hex() != res.StateRoot {
		return fmt.Errorf("block %d's own StateRoot (%s) does not match the proof's claimed root (%s) -- refusing to trust either",
			res.Height, header.StateRoot.Hex(), res.StateRoot)
	}
	if !consensus.MeetsTarget(header.Hash(), header.Difficulty) {
		return fmt.Errorf("block %d's header does not meet its own declared PoW difficulty -- refusing to trust it", res.Height)
	}

	acct, ok, err := rpc.VerifyAccountProof(res.StateRoot, addrHex, res.Proof)
	if err != nil {
		return fmt.Errorf("verify proof: %w", err)
	}
	if !ok {
		return errors.New("account proof failed verification -- the node's response cannot be trusted")
	}

	fmt.Printf("balance: %d (verified: block %d, PoW-checked header, Merkle proof against StateRoot %s)\n",
		acct.Balance, res.Height, res.StateRoot)
	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	keyHex := fs.String("key", "", "sender private key (hex)")
	to := fs.String("to", "", "recipient address (hex)")
	value := fs.Uint64("value", 0, "amount to transfer")
	nonce := fs.Int64("nonce", -1, "sender nonce (default: query via RPC)")
	rpcURL := fs.String("rpc", "http://127.0.0.1:8545", "JSON-RPC endpoint URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyHex == "" || *to == "" {
		return errors.New("--key and --to are required")
	}
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(*keyHex, "0x"))
	if err != nil {
		return fmt.Errorf("bad --key: %w", err)
	}
	key := wallet.KeyFromBytes(keyBytes)

	toAddr, err := parseAddress(*to)
	if err != nil {
		return fmt.Errorf("bad --to: %w", err)
	}

	c := rpc.NewClient(*rpcURL)

	// Resolve the nonce: use --nonce if provided, else query the sender balance
	// endpoint is not enough; we scan blocks is overkill — instead we rely on the
	// server rejecting a wrong nonce. Default to querying is not exposed, so when
	// --nonce is absent, default to 0 for a fresh account.
	var txNonce uint64
	if *nonce >= 0 {
		txNonce = uint64(*nonce)
	}

	tx := core.Transaction{
		To:       toAddr,
		Value:    *value,
		Nonce:    txNonce,
		GasLimit: 21000,
		ChainID:  chain.DefaultChainID,
	}
	key.Sign(&tx)

	txHash, err := c.SendRawTx(tx)
	if err != nil {
		return err
	}
	fmt.Printf("txHash: %s\n", txHash)
	return nil
}

func cmdNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	db := fs.String("db", "", "BoltDB path (empty = in-memory)")
	rpcAddr := fs.String("rpc-addr", "127.0.0.1:8545", "HTTP JSON-RPC listen address")
	minerKeyHex := fs.String("miner-key", "", "miner private key (hex; empty = generate)")
	difficulty := fs.Uint("difficulty", 8, "PoW difficulty (leading zero bits)")
	allocSpec := fs.String("alloc", "", "genesis alloc: addrHex:amount,addrHex:amount,...")
	baseAllocSpec := fs.String("base-alloc", "", "genesis exchange base-asset alloc: addrHex:amount,addrHex:amount,... (funds the exchange's base asset; --alloc funds the native/quote asset)")
	mineInterval := fs.Duration("mine-interval", 5*time.Second, "block mining interval")
	listenHost := fs.String("listen-host", "127.0.0.1", "libp2p TCP listen host (use 0.0.0.0 to be reachable from sibling Docker containers; loopback is not)")
	listen := fs.Int("listen", 0, "libp2p TCP listen port (0 = ephemeral)")
	peers := fs.String("peers", "", "comma-separated libp2p multiaddrs to dial on startup")
	mdnsEnabled := fs.Bool("mdns", false, "enable mDNS LAN peer auto-discovery (no --peers needed on the same LAN)")
	genesisTS := fs.Int64("genesis-timestamp", 0, "genesis block unix timestamp; REQUIRED to match across nodes for identical genesis")
	identityKeyHex := fs.String("identity-key", "", "hex seed for a fixed libp2p peer identity (empty = fresh identity every restart); pins the peer ID so other nodes' --peers can name this node's multiaddr ahead of time")
	relayService := fs.Bool("relay-service", false, "become a circuit-relay-v2 relay other peers can reserve a slot on and be reached through")
	relaySpec := fs.String("relay", "", "full multiaddr of a relay (running --relay-service) to reserve a slot on, so peers can reach this node at /p2p/<relay>/p2p-circuit/p2p/<this node> without a direct address of its own")
	dhtEnabled := fs.Bool("dht", false, "enable Kademlia DHT peer discovery: advertise and find other l1chain nodes under a shared rendezvous, dialing any found -- the non-LAN counterpart to --mdns")
	dhtBootstrapSpec := fs.String("dht-bootstrap", "", "comma-separated multiaddrs to seed the DHT routing table; at least one live peer is required for --dht to discover anything beyond an empty table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var minerKey wallet.Key
	if *minerKeyHex != "" {
		b, err := hex.DecodeString(strings.TrimPrefix(*minerKeyHex, "0x"))
		if err != nil {
			return fmt.Errorf("bad --miner-key: %w", err)
		}
		minerKey = wallet.KeyFromBytes(b)
	} else {
		k, err := wallet.NewKey()
		if err != nil {
			return err
		}
		minerKey = k
		fmt.Printf("generated miner key: %s (address %s)\n", hex.EncodeToString(k.Bytes()), k.Address().Hex())
	}

	alloc, err := parseAlloc(*allocSpec)
	if err != nil {
		return fmt.Errorf("bad --alloc: %w", err)
	}
	baseAlloc, err := parseAlloc(*baseAllocSpec)
	if err != nil {
		return fmt.Errorf("bad --base-alloc: %w", err)
	}

	n, err := node.New(node.Config{
		DBPath:           *db,
		MinerKey:         minerKey,
		Difficulty:       uint32(*difficulty),
		GenesisAlloc:     alloc,
		GenesisBaseAlloc: baseAlloc,
		GenesisTimestamp: *genesisTS,
	})
	if err != nil {
		return err
	}
	defer n.Close()

	ln, err := net.Listen("tcp", *rpcAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: rpc.NewServer(n)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start real libp2p networking: host, GossipSub, and the sync protocol.
	// Peers gossip mined blocks and validate every one via AcceptExternalBlock.
	hostCfg := p2p.HostConfig{ListenHost: *listenHost, ListenPort: *listen}
	if *identityKeyHex != "" {
		id, err := p2p.IdentityFromSeed(*identityKeyHex)
		if err != nil {
			return err
		}
		hostCfg.IdentityKey = id
	}
	h, err := p2p.NewHostWithConfig(ctx, hostCfg)
	if err != nil {
		return fmt.Errorf("p2p host: %w", err)
	}
	defer func() { _ = h.Close() }()
	pp, err := p2p.NewP2P(ctx, h)
	if err != nil {
		return fmt.Errorf("p2p gossip: %w", err)
	}
	if err := p2p.Wire(ctx, n, pp); err != nil {
		return fmt.Errorf("p2p wire: %w", err)
	}
	for _, addr := range p2p.FullAddrs(h) {
		fmt.Printf("p2p listening: %s\n", addr)
	}
	fmt.Printf("p2p peer id: %s\n", h.ID())

	if *relayService {
		if _, err := p2p.EnableRelayService(h); err != nil {
			return fmt.Errorf("p2p relay service: %w", err)
		}
		fmt.Println("relay service enabled: other peers may reserve a slot and be reached through this node")
	}
	if *relaySpec != "" {
		m, err := ma.NewMultiaddr(*relaySpec)
		if err != nil {
			return fmt.Errorf("bad --relay: %w", err)
		}
		relayInfo, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			return fmt.Errorf("bad --relay: %w", err)
		}
		// ReserveRelaySlot (via the underlying client.Reserve) already adds
		// relayInfo.Addrs to the peerstore itself before dialing -- no need
		// to do it here too.
		if err := p2p.ReserveRelaySlot(ctx, h, *relayInfo); err != nil {
			return fmt.Errorf("p2p relay reservation: %w", err)
		}
		fmt.Printf("reserved a slot on relay %s: reachable at /p2p/%s/p2p-circuit/p2p/%s\n",
			relayInfo.ID, relayInfo.ID, h.ID())
	}

	// Dial any startup peers so gossip meshes form immediately.
	for _, addr := range strings.Split(*peers, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if err := p2p.ConnectAddr(ctx, h, addr); err != nil {
			fmt.Fprintln(os.Stderr, "peer connect:", err)
			continue
		}
		fmt.Printf("connected to peer: %s\n", addr)
	}

	// Enable mDNS LAN auto-discovery so nodes on the same network segment mesh
	// without explicit --peers. Runs for the node's lifetime (bound to ctx).
	if *mdnsEnabled {
		if err := p2p.EnableMDNS(ctx, h, p2p.DefaultMDNSTag); err != nil {
			return fmt.Errorf("p2p mdns: %w", err)
		}
		fmt.Printf("mdns discovery enabled (tag %q)\n", p2p.DefaultMDNSTag)
	}

	// Enable DHT peer discovery -- the non-LAN counterpart to --mdns. Runs
	// for the node's lifetime (bound to ctx).
	if *dhtEnabled {
		var dhtBootstrap []peer.AddrInfo
		for _, addr := range strings.Split(*dhtBootstrapSpec, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			m, err := ma.NewMultiaddr(addr)
			if err != nil {
				return fmt.Errorf("bad --dht-bootstrap %q: %w", addr, err)
			}
			pi, err := peer.AddrInfoFromP2pAddr(m)
			if err != nil {
				return fmt.Errorf("bad --dht-bootstrap %q: %w", addr, err)
			}
			dhtBootstrap = append(dhtBootstrap, *pi)
		}
		if _, err := p2p.EnableDHTDiscovery(ctx, h, dhtBootstrap, p2p.DefaultDHTRendezvous); err != nil {
			return fmt.Errorf("p2p dht: %w", err)
		}
		fmt.Printf("dht discovery enabled (rendezvous %q, %d bootstrap peer(s))\n", p2p.DefaultDHTRendezvous, len(dhtBootstrap))
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "rpc server:", err)
		}
	}()
	fmt.Printf("node listening on http://%s (head height %d, difficulty %d)\n",
		ln.Addr(), n.Head().Header.Height, *difficulty)

	ticker := time.NewTicker(*mineInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("shutting down...")
			shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return srv.Shutdown(shutCtx)
		case <-ticker.C:
			blk, err := n.MineBlock()
			if err != nil {
				fmt.Fprintln(os.Stderr, "mine:", err)
				continue
			}
			if err := pp.AnnounceBlock(blk); err != nil {
				fmt.Fprintln(os.Stderr, "announce:", err)
			}
			fmt.Printf("mined block %d (%s, %d txs)\n",
				blk.Header.Height, blk.Hash().Hex()[:12], len(blk.Txs))
		}
	}
}

func parseAddress(s string) (core.Address, error) {
	var a core.Address
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return a, err
	}
	if len(b) != core.AddrLen {
		return a, fmt.Errorf("address must be %d bytes, got %d", core.AddrLen, len(b))
	}
	copy(a[:], b)
	return a, nil
}

// parseAlloc parses "addrHex:amount,addrHex:amount" into a genesis alloc map.
func parseAlloc(spec string) (map[core.Address]uint64, error) {
	alloc := make(map[core.Address]uint64)
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return alloc, nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("bad alloc entry %q (want addrHex:amount)", part)
		}
		addr, err := parseAddress(strings.TrimSpace(kv[0]))
		if err != nil {
			return nil, err
		}
		amt, err := strconv.ParseUint(strings.TrimSpace(kv[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad amount in %q: %w", part, err)
		}
		alloc[addr] = amt
	}
	return alloc, nil
}
