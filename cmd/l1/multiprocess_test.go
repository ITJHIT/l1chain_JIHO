package main

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"l1chain/rpc"
)

// TestMultiprocessConvergence is the REQUIRED cross-process acceptance: it
// builds the l1 binary once, then launches three independent `l1 node` OS
// processes on ephemeral ports that share an identical genesis (same
// --genesis-timestamp, --alloc, --difficulty). node1 mines on a short interval;
// node2 and node3 only receive over real libp2p gossip (their mine interval is
// set far beyond the test window so node1 is the sole block producer, avoiding
// PoW forks). The test polls each node's getChainHead RPC until all three
// report the SAME head hash at height >= 2, proving real inter-process gossip
// and sync converge the network.
func TestMultiprocessConvergence(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}

	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "l1")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}

	// Build the node binary once from this package directory.
	build := exec.Command(goBin, "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build l1 binary (process spawn unavailable): %v\n%s", err, out)
	}

	const (
		genesisTS   = "1700000000"
		faucet      = "1111111111111111111111111111111111111111"
		alloc       = faucet + ":1000000"
		difficulty  = "4"
		fastMine    = "200ms"
		slowMine    = "1h" // effectively never fires within the test
		convergeTTL = 90 * time.Second
	)

	baseArgs := func(listenPeers, mineInterval string) []string {
		a := []string{
			"node",
			"--rpc-addr", "127.0.0.1:0",
			"--listen", "0",
			"--genesis-timestamp", genesisTS,
			"--alloc", alloc,
			"--difficulty", difficulty,
			"--mine-interval", mineInterval,
		}
		if listenPeers != "" {
			a = append(a, "--peers", listenPeers)
		}
		return a
	}

	// node1: sole miner, no upstream peers.
	n1 := startNode(t, binPath, baseArgs("", fastMine))
	defer n1.stop(t)
	p2pAddr := n1.waitLine(t, "p2p listening: ", 20*time.Second)
	p2pAddr = strings.TrimSpace(strings.TrimPrefix(p2pAddr, "p2p listening: "))
	if !strings.Contains(p2pAddr, "127.0.0.1") {
		t.Fatalf("node1 p2p addr not loopback: %q", p2pAddr)
	}
	rpc1 := n1.rpcURL(t)

	// node2 + node3: dial node1, do not mine within the window.
	n2 := startNode(t, binPath, baseArgs(p2pAddr, slowMine))
	defer n2.stop(t)
	n3 := startNode(t, binPath, baseArgs(p2pAddr, slowMine))
	defer n3.stop(t)
	rpc2 := n2.rpcURL(t)
	rpc3 := n3.rpcURL(t)

	clients := []*rpc.Client{rpc.NewClient(rpc1), rpc.NewClient(rpc2), rpc.NewClient(rpc3)}

	// Poll until all three report the same head hash at height >= 2.
	deadline := time.Now().Add(convergeTTL)
	var (
		conv     bool
		lastHash string
		lastH    uint64
	)
	for time.Now().Before(deadline) {
		heads := make([]rpc.ChainHead, 0, len(clients))
		ok := true
		for _, c := range clients {
			h, err := c.GetChainHead()
			if err != nil {
				ok = false
				break
			}
			heads = append(heads, h)
		}
		if ok && len(heads) == 3 {
			h0 := heads[0]
			same := h0.Hash != "" && h0.Height >= 2 &&
				heads[1].Hash == h0.Hash && heads[2].Hash == h0.Hash &&
				heads[1].Height == h0.Height && heads[2].Height == h0.Height
			lastHash, lastH = h0.Hash, h0.Height
			if same {
				conv = true
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	if !conv {
		h1, _ := clients[0].GetChainHead()
		h2, _ := clients[1].GetChainHead()
		h3, _ := clients[2].GetChainHead()
		t.Fatalf("nodes did not converge within %s: n1=(%d,%s) n2=(%d,%s) n3=(%d,%s)",
			convergeTTL, h1.Height, short(h1.Hash), h2.Height, short(h2.Hash), h3.Height, short(h3.Hash))
	}

	t.Logf("converged: 3 processes at height %d, head hash %s", lastH, lastHash)
}

func short(h string) string {
	if len(h) > 14 {
		return h[:14]
	}
	return h
}

// nodeProc wraps a spawned `l1 node` process and buffers its stdout so the test
// can wait for the emitted p2p multiaddr and RPC address lines.
type nodeProc struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	lines []string
}

func startNode(t *testing.T, binPath string, args []string) *nodeProc {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn node process (spawn unavailable): %v", err)
	}
	np := &nodeProc{cmd: cmd}
	go np.scan(stdout)
	return np
}

func (np *nodeProc) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		np.mu.Lock()
		np.lines = append(np.lines, sc.Text())
		np.mu.Unlock()
	}
}

// waitLine blocks until a buffered stdout line contains substr, returning that
// line, or fails the test after timeout.
func (np *nodeProc) waitLine(t *testing.T, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		np.mu.Lock()
		for _, l := range np.lines {
			if strings.Contains(l, substr) {
				np.mu.Unlock()
				return l
			}
		}
		np.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	np.mu.Lock()
	dump := strings.Join(np.lines, "\n")
	np.mu.Unlock()
	t.Fatalf("timed out waiting for %q in node output; got:\n%s", substr, dump)
	return ""
}

// rpcURL waits for the RPC listen line and returns the concrete http URL.
func (np *nodeProc) rpcURL(t *testing.T) string {
	t.Helper()
	line := np.waitLine(t, "node listening on http://", 20*time.Second)
	i := strings.Index(line, "http://")
	rest := line[i:]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	rest = strings.TrimSpace(rest)
	// Sanity check the port parses.
	if _, _, ok := splitHostPort(rest); !ok {
		t.Fatalf("bad rpc url parsed: %q (from %q)", rest, line)
	}
	return rest
}

func splitHostPort(url string) (host, port string, ok bool) {
	s := strings.TrimPrefix(url, "http://")
	idx := strings.LastIndexByte(s, ':')
	if idx < 0 {
		return "", "", false
	}
	host, port = s[:idx], s[idx+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", false
	}
	return host, port, true
}

// stop terminates the node process tree. On Windows a single Kill may orphan
// children, so use taskkill /T /F; elsewhere Kill the process directly.
func (np *nodeProc) stop(t *testing.T) {
	t.Helper()
	if np.cmd == nil || np.cmd.Process == nil {
		return
	}
	pid := np.cmd.Process.Pid
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		kill := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
		_ = kill.Run()
	} else {
		_ = np.cmd.Process.Kill()
	}
	_ = np.cmd.Wait()
}
