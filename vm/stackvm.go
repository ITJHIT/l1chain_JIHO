// Package vm — StackVM is the M3 execution engine: a deterministic 256-bit word
// stack machine with fixed per-opcode gas metering, contract deployment, calls,
// and journaled (revertible) storage.
//
// Determinism: all arithmetic is performed modulo 2^256 (overflow wraps); there
// is no wall-clock, randomness, or nondeterministic map iteration that affects
// results. State writes are buffered in an in-memory journal (scope) and only
// flushed to the backing StateDB when the call frame succeeds, so an out-of-gas
// or reverting execution leaves storage untouched. The same message applied to
// the same state therefore yields the same storage, gas, and StateRoot on every
// node — which is what block validation (StateRoot re-derivation) relies on.
//
// Gas / OOG semantics: each opcode has a fixed gas cost. Gas is a single budget
// shared across nested calls (a simplification of EVM's per-call gas). When the
// budget is exhausted, Execute returns Receipt{Success:false, Err:ErrOutOfGas,
// GasUsed: GasLimit} and NO state changes from the message are committed.
//
// Contract creation (msg.To == nil): the contract address is the last 20 bytes
// of SumHash(From || Nonce). msg.Data is stored verbatim as the contract's
// runtime code (no init-code execution in M3). Receipt.ContractAddress is set.
//
// Contract call (msg.To != nil): the account's stored code runs with msg.Data
// as calldata. Calling an account with no code is a plain value transfer.
package vm

import (
	"encoding/binary"
	"errors"
	"math/big"

	"l1chain/core"
	"l1chain/state"
)

// Execution errors. Any error reverts the frame's state writes.
var (
	ErrOutOfGas            = errors.New("vm: out of gas")
	ErrStackUnderflow     = errors.New("vm: stack underflow")
	ErrStackOverflow      = errors.New("vm: stack overflow")
	ErrInvalidJump        = errors.New("vm: invalid jump destination")
	ErrInvalidOpcode      = errors.New("vm: invalid opcode")
	ErrCallDepth          = errors.New("vm: call depth exceeded")
	ErrMemoryLimit        = errors.New("vm: memory limit exceeded")
	ErrInsufficientFunds  = errors.New("vm: insufficient balance for value transfer")
)

// Machine limits.
const (
	MaxStack     = 1024
	MaxCallDepth = 1024
	maxMemBytes  = 1 << 16 // 64 KiB cap to bound allocation deterministically
)

// Fixed gas costs.
const (
	GasCreate = 32000
	gasCall   = 700
)

// Opcodes (Ethereum-compatible numbering for the subset implemented).
const (
	opSTOP byte = 0x00
	opADD  byte = 0x01
	opMUL  byte = 0x02
	opSUB  byte = 0x03
	opDIV  byte = 0x04

	opLT     byte = 0x10
	opGT     byte = 0x11
	opEQ     byte = 0x14
	opISZERO byte = 0x15

	opCALLVALUE    byte = 0x34
	opCALLDATALOAD byte = 0x35
	opCALLDATASIZE byte = 0x36

	opPOP      byte = 0x50
	opMLOAD    byte = 0x51
	opMSTORE   byte = 0x52
	opSLOAD    byte = 0x54
	opSSTORE   byte = 0x55
	opJUMP     byte = 0x56
	opJUMPI    byte = 0x57
	opJUMPDEST byte = 0x5b

	opPUSH1  byte = 0x60
	opPUSH32 byte = 0x7f

	opDUP1  byte = 0x80
	opSWAP1 byte = 0x90

	opCALL   byte = 0xf1
	opRETURN byte = 0xf3
)

// StackVM implements VM with the M3 stack machine.
type StackVM struct{}

// NewStackVM returns a StackVM.
func NewStackVM() StackVM { return StackVM{} }

// tt256 = 2^256; mod258 is used to wrap every word into [0, 2^256).
var tt256 = new(big.Int).Lsh(big.NewInt(1), 256)

func wrap(x *big.Int) *big.Int { return x.Mod(x, tt256) }

// ---------------------------------------------------------------------------
// Journaled state scope
// ---------------------------------------------------------------------------

// scope buffers account/code/storage writes. A root scope reads through to the
// backing StateDB; a child scope reads through to its parent. commit() flushes
// buffered writes one level down (to the parent, or to the base for the root).
// Discarding a scope (never calling commit) reverts every write it holds.
type scope struct {
	base    state.StateDB
	parent  *scope
	acct    map[core.Address]state.Account
	code    map[core.Address][]byte
	storage map[core.Address]map[core.Hash]core.Hash
}

func newScope(base state.StateDB) *scope {
	return &scope{
		base:    base,
		acct:    make(map[core.Address]state.Account),
		code:    make(map[core.Address][]byte),
		storage: make(map[core.Address]map[core.Hash]core.Hash),
	}
}

func (s *scope) child() *scope {
	return &scope{
		parent:  s,
		acct:    make(map[core.Address]state.Account),
		code:    make(map[core.Address][]byte),
		storage: make(map[core.Address]map[core.Hash]core.Hash),
	}
}

func (s *scope) getAccount(a core.Address) state.Account {
	if acct, ok := s.acct[a]; ok {
		return acct
	}
	if s.parent != nil {
		return s.parent.getAccount(a)
	}
	return s.base.GetAccount(a)
}

func (s *scope) setAccount(a core.Address, acct state.Account) { s.acct[a] = acct }

func (s *scope) getCode(a core.Address) []byte {
	if c, ok := s.code[a]; ok {
		return c
	}
	if s.parent != nil {
		return s.parent.getCode(a)
	}
	return s.base.GetCode(a)
}

func (s *scope) setCode(a core.Address, c []byte) { s.code[a] = c }

func (s *scope) getStorage(a core.Address, k core.Hash) core.Hash {
	if m, ok := s.storage[a]; ok {
		if v, ok2 := m[k]; ok2 {
			return v
		}
	}
	if s.parent != nil {
		return s.parent.getStorage(a, k)
	}
	return s.base.GetStorage(a, k)
}

func (s *scope) setStorage(a core.Address, k, v core.Hash) {
	m := s.storage[a]
	if m == nil {
		m = make(map[core.Hash]core.Hash)
		s.storage[a] = m
	}
	m[k] = v
}

// commit flushes this scope's writes down one level. Map iteration order is
// irrelevant: the destination (parent map or StateDB whose StateRoot sorts
// deterministically) does not depend on write order.
func (s *scope) commit() {
	if s.parent != nil {
		for a, acct := range s.acct {
			s.parent.acct[a] = acct
		}
		for a, c := range s.code {
			s.parent.code[a] = c
		}
		for a, m := range s.storage {
			pm := s.parent.storage[a]
			if pm == nil {
				pm = make(map[core.Hash]core.Hash)
				s.parent.storage[a] = pm
			}
			for k, v := range m {
				pm[k] = v
			}
		}
		return
	}
	for a, acct := range s.acct {
		s.base.SetAccount(a, acct)
	}
	for a, c := range s.code {
		s.base.SetCode(a, c)
	}
	for a, m := range s.storage {
		for k, v := range m {
			s.base.SetStorage(a, k, v)
		}
	}
}

func (s *scope) transfer(from, to core.Address, value uint64) error {
	if value == 0 || from == to {
		return nil
	}
	f := s.getAccount(from)
	if f.Balance < value {
		return ErrInsufficientFunds
	}
	f.Balance -= value
	s.setAccount(from, f)
	t := s.getAccount(to)
	t.Balance += value
	s.setAccount(to, t)
	return nil
}

// ---------------------------------------------------------------------------
// Gas budget shared across nested frames
// ---------------------------------------------------------------------------

type gasMeter struct {
	limit uint64
	used  uint64
}

func (g *gasMeter) charge(cost uint64) error {
	if g.used+cost > g.limit || g.used+cost < g.used { // overflow-safe
		g.used = g.limit
		return ErrOutOfGas
	}
	g.used += cost
	return nil
}

func gasCost(op byte) uint64 {
	switch {
	case op == opSTOP, op == opRETURN:
		return 0
	case op == opADD, op == opSUB:
		return 3
	case op == opMUL, op == opDIV:
		return 5
	case op == opLT, op == opGT, op == opEQ, op == opISZERO:
		return 3
	case op == opCALLVALUE, op == opCALLDATASIZE:
		return 2
	case op == opCALLDATALOAD:
		return 3
	case op == opPOP:
		return 2
	case op == opMLOAD, op == opMSTORE:
		return 3
	case op == opSLOAD:
		return 200
	case op == opSSTORE:
		return 5000
	case op == opJUMP:
		return 8
	case op == opJUMPI:
		return 10
	case op == opJUMPDEST:
		return 1
	case op >= opPUSH1 && op <= opPUSH32:
		return 3
	case op == opDUP1, op == opSWAP1:
		return 3
	case op == opCALL:
		return gasCall
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// Execute implements VM.
func (StackVM) Execute(st state.StateDB, msg Message) Receipt {
	vm := StackVM{}
	g := &gasMeter{limit: msg.GasLimit}
	root := newScope(st)

	if msg.To == nil {
		return vm.create(g, root, msg)
	}

	self := *msg.To
	if err := root.transfer(msg.From, self, msg.Value); err != nil {
		return Receipt{Success: false, Err: err, GasUsed: g.used}
	}
	ret, err := vm.run(g, root, 0, root.getCode(self), self, msg.Value, msg.Data)
	if err != nil {
		return Receipt{Success: false, Err: err, GasUsed: finalGas(g, err)}
	}
	root.commit()
	return Receipt{Success: true, GasUsed: g.used, ReturnData: ret}
}

func (StackVM) create(g *gasMeter, root *scope, msg Message) Receipt {
	if err := g.charge(GasCreate); err != nil {
		return Receipt{Success: false, Err: ErrOutOfGas, GasUsed: g.limit}
	}
	addr := CreateAddress(msg.From, msg.Nonce)
	if err := root.transfer(msg.From, addr, msg.Value); err != nil {
		return Receipt{Success: false, Err: err, GasUsed: g.used}
	}
	code := make([]byte, len(msg.Data))
	copy(code, msg.Data)
	root.setCode(addr, code)
	root.commit()
	out := addr
	return Receipt{Success: true, GasUsed: g.used, ContractAddress: &out}
}

func finalGas(g *gasMeter, err error) uint64 {
	if errors.Is(err, ErrOutOfGas) {
		return g.limit
	}
	return g.used
}

// CreateAddress derives a contract address as the last 20 bytes of
// SumHash(From || Nonce) (big-endian nonce). Deterministic across nodes.
func CreateAddress(from core.Address, nonce uint64) core.Address {
	buf := make([]byte, 0, core.AddrLen+8)
	buf = append(buf, from[:]...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], nonce)
	buf = append(buf, n[:]...)
	h := core.SumHash(buf)
	var a core.Address
	copy(a[:], h[core.HashLen-core.AddrLen:])
	return a
}

// run executes code within scope sc and returns its RETURN data (nil for STOP).
// Any returned error means the frame reverts (caller discards sc).
func (vm StackVM) run(g *gasMeter, sc *scope, depth int, code []byte, self core.Address, callValue uint64, calldata []byte) ([]byte, error) {
	st := &stack{}
	var mem []byte
	dests := validJumpdests(code)
	pc := 0

	for pc < len(code) {
		op := code[pc]
		if err := g.charge(gasCost(op)); err != nil {
			return nil, err
		}

		switch {
		case op == opSTOP:
			return nil, nil

		case op == opADD:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(wrap(a.Add(a, b))); err != nil {
				return nil, err
			}
		case op == opSUB:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(wrap(a.Sub(a, b))); err != nil {
				return nil, err
			}
		case op == opMUL:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(wrap(a.Mul(a, b))); err != nil {
				return nil, err
			}
		case op == opDIV:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if b.Sign() == 0 {
				a.SetUint64(0)
			} else {
				a.Div(a, b)
			}
			if err := st.push(a); err != nil {
				return nil, err
			}

		case op == opLT:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(boolWord(a.Cmp(b) < 0)); err != nil {
				return nil, err
			}
		case op == opGT:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(boolWord(a.Cmp(b) > 0)); err != nil {
				return nil, err
			}
		case op == opEQ:
			a, b, err := st.pop2()
			if err != nil {
				return nil, err
			}
			if err := st.push(boolWord(a.Cmp(b) == 0)); err != nil {
				return nil, err
			}
		case op == opISZERO:
			a, err := st.pop()
			if err != nil {
				return nil, err
			}
			if err := st.push(boolWord(a.Sign() == 0)); err != nil {
				return nil, err
			}

		case op == opCALLVALUE:
			if err := st.push(new(big.Int).SetUint64(callValue)); err != nil {
				return nil, err
			}
		case op == opCALLDATASIZE:
			if err := st.push(new(big.Int).SetUint64(uint64(len(calldata)))); err != nil {
				return nil, err
			}
		case op == opCALLDATALOAD:
			off, err := st.pop()
			if err != nil {
				return nil, err
			}
			if err := st.push(new(big.Int).SetBytes(readData(calldata, off))); err != nil {
				return nil, err
			}

		case op == opPOP:
			if _, err := st.pop(); err != nil {
				return nil, err
			}

		case op == opMLOAD:
			off, err := st.pop()
			if err != nil {
				return nil, err
			}
			idx, ok := toIndex(off)
			if !ok || idx+32 > maxMemBytes {
				return nil, ErrMemoryLimit
			}
			mem = ensureMem(mem, idx+32)
			if err := st.push(new(big.Int).SetBytes(mem[idx : idx+32])); err != nil {
				return nil, err
			}
		case op == opMSTORE:
			off, err := st.pop()
			if err != nil {
				return nil, err
			}
			val, err := st.pop()
			if err != nil {
				return nil, err
			}
			idx, ok := toIndex(off)
			if !ok || idx+32 > maxMemBytes {
				return nil, ErrMemoryLimit
			}
			mem = ensureMem(mem, idx+32)
			w := bigToHash(val)
			copy(mem[idx:idx+32], w[:])

		case op == opSLOAD:
			key, err := st.pop()
			if err != nil {
				return nil, err
			}
			v := sc.getStorage(self, bigToHash(key))
			if err := st.push(new(big.Int).SetBytes(v[:])); err != nil {
				return nil, err
			}
		case op == opSSTORE:
			key, err := st.pop()
			if err != nil {
				return nil, err
			}
			val, err := st.pop()
			if err != nil {
				return nil, err
			}
			sc.setStorage(self, bigToHash(key), bigToHash(val))

		case op == opJUMP:
			dest, err := st.pop()
			if err != nil {
				return nil, err
			}
			d, ok := toIndex(dest)
			if !ok || !dests[d] {
				return nil, ErrInvalidJump
			}
			pc = d
			continue
		case op == opJUMPI:
			dest, err := st.pop()
			if err != nil {
				return nil, err
			}
			cond, err := st.pop()
			if err != nil {
				return nil, err
			}
			if cond.Sign() != 0 {
				d, ok := toIndex(dest)
				if !ok || !dests[d] {
					return nil, ErrInvalidJump
				}
				pc = d
				continue
			}
		case op == opJUMPDEST:
			// no-op marker

		case op >= opPUSH1 && op <= opPUSH32:
			n := int(op-opPUSH1) + 1
			buf := make([]byte, n)
			start := pc + 1
			for i := 0; i < n && start+i < len(code); i++ {
				buf[i] = code[start+i]
			}
			if err := st.push(new(big.Int).SetBytes(buf)); err != nil {
				return nil, err
			}
			pc = start + n
			continue

		case op == opDUP1:
			v, err := st.peek(0)
			if err != nil {
				return nil, err
			}
			if err := st.push(new(big.Int).Set(v)); err != nil {
				return nil, err
			}
		case op == opSWAP1:
			if err := st.swap1(); err != nil {
				return nil, err
			}

		case op == opRETURN:
			off, err := st.pop()
			if err != nil {
				return nil, err
			}
			size, err := st.pop()
			if err != nil {
				return nil, err
			}
			oi, ok := toIndex(off)
			si, ok2 := toIndex(size)
			if !ok || !ok2 || oi+si > maxMemBytes {
				return nil, ErrMemoryLimit
			}
			mem = ensureMem(mem, oi+si)
			out := make([]byte, si)
			copy(out, mem[oi:oi+si])
			return out, nil

		case op == opCALL:
			if err := vm.opCall(g, sc, depth, st, &mem, self); err != nil {
				return nil, err
			}

		default:
			return nil, ErrInvalidOpcode
		}
		pc++
	}
	return nil, nil
}

// opCall implements a simplified CALL: it pops the 7 EVM arguments
// (gas, addr, value, argsOffset, argsSize, retOffset, retSize). The gas
// argument is ignored — nested calls draw from the shared budget. On success
// the callee's return data is copied into memory and 1 is pushed; on a
// (non-OOG) revert 0 is pushed and the callee's writes are discarded. A global
// OOG propagates up and aborts the whole message.
func (vm StackVM) opCall(g *gasMeter, sc *scope, depth int, st *stack, mem *[]byte, self core.Address) error {
	_, err := st.pop() // gas (ignored, shared budget)
	if err != nil {
		return err
	}
	addrW, err := st.pop()
	if err != nil {
		return err
	}
	valueW, err := st.pop()
	if err != nil {
		return err
	}
	argsOff, err := st.pop()
	if err != nil {
		return err
	}
	argsSize, err := st.pop()
	if err != nil {
		return err
	}
	retOff, err := st.pop()
	if err != nil {
		return err
	}
	retSize, err := st.pop()
	if err != nil {
		return err
	}

	to := addrFromWord(addrW)
	value := uint64(0)
	if valueW.IsUint64() {
		value = valueW.Uint64()
	}

	// Depth limit: exceeding it fails the CALL (push 0) without executing.
	if depth+1 > MaxCallDepth {
		return st.push(big.NewInt(0))
	}

	ai, ok := toIndex(argsOff)
	as, ok2 := toIndex(argsSize)
	if !ok || !ok2 || ai+as > maxMemBytes {
		return ErrMemoryLimit
	}
	*mem = ensureMem(*mem, ai+as)
	input := make([]byte, as)
	copy(input, (*mem)[ai:ai+as])

	child := sc.child()
	if err := child.transfer(self, to, value); err != nil {
		// Insufficient balance: CALL fails, no state change.
		return st.push(big.NewInt(0))
	}

	ret, cerr := vm.run(g, child, depth+1, child.getCode(to), to, value, input)
	if errors.Is(cerr, ErrOutOfGas) {
		return cerr // global budget exhausted: abort whole message
	}
	if cerr != nil {
		return st.push(big.NewInt(0)) // revert child, signal failure
	}

	child.commit()
	// Copy up to retSize bytes of ret into memory at retOff.
	oi, ok3 := toIndex(retOff)
	rs, ok4 := toIndex(retSize)
	if !ok3 || !ok4 || oi+rs > maxMemBytes {
		return ErrMemoryLimit
	}
	if rs > 0 {
		*mem = ensureMem(*mem, oi+rs)
		n := rs
		if n > len(ret) {
			n = len(ret)
		}
		copy((*mem)[oi:oi+n], ret[:n])
	}
	return st.push(big.NewInt(1))
}

// ---------------------------------------------------------------------------
// Stack
// ---------------------------------------------------------------------------

type stack struct{ data []*big.Int }

func (s *stack) push(v *big.Int) error {
	if len(s.data) >= MaxStack {
		return ErrStackOverflow
	}
	s.data = append(s.data, v)
	return nil
}

func (s *stack) pop() (*big.Int, error) {
	n := len(s.data)
	if n == 0 {
		return nil, ErrStackUnderflow
	}
	v := s.data[n-1]
	s.data = s.data[:n-1]
	return v, nil
}

func (s *stack) pop2() (a, b *big.Int, err error) {
	if a, err = s.pop(); err != nil {
		return nil, nil, err
	}
	if b, err = s.pop(); err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

func (s *stack) peek(n int) (*big.Int, error) {
	if len(s.data) <= n {
		return nil, ErrStackUnderflow
	}
	return s.data[len(s.data)-1-n], nil
}

func (s *stack) swap1() error {
	n := len(s.data)
	if n < 2 {
		return ErrStackUnderflow
	}
	s.data[n-1], s.data[n-2] = s.data[n-2], s.data[n-1]
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func boolWord(b bool) *big.Int {
	if b {
		return big.NewInt(1)
	}
	return big.NewInt(0)
}

// validJumpdests returns the set of byte offsets that are real JUMPDEST opcodes
// (i.e. not immediates embedded in PUSH data).
func validJumpdests(code []byte) map[int]bool {
	dests := make(map[int]bool)
	for i := 0; i < len(code); {
		op := code[i]
		switch {
		case op == opJUMPDEST:
			dests[i] = true
			i++
		case op >= opPUSH1 && op <= opPUSH32:
			i += int(op-opPUSH1) + 2
		default:
			i++
		}
	}
	return dests
}

func ensureMem(mem []byte, size int) []byte {
	if len(mem) >= size {
		return mem
	}
	grown := make([]byte, size)
	copy(grown, mem)
	return grown
}

// toIndex converts a word to a non-negative int index, reporting ok=false when
// it does not fit (used to bound jumps and memory offsets).
func toIndex(v *big.Int) (int, bool) {
	if !v.IsUint64() {
		return 0, false
	}
	u := v.Uint64()
	if u > uint64(^uint32(0)) { // keep well within int range
		return 0, false
	}
	return int(u), true
}

// readData returns 32 bytes of data starting at offset (zero-padded when the
// window runs past the end), for CALLDATALOAD.
func readData(data []byte, off *big.Int) []byte {
	out := make([]byte, 32)
	idx, ok := toIndex(off)
	if !ok {
		return out
	}
	for i := 0; i < 32 && idx+i < len(data); i++ {
		out[i] = data[idx+i]
	}
	return out
}

func bigToHash(v *big.Int) core.Hash {
	var h core.Hash
	b := wrap(new(big.Int).Set(v)).Bytes()
	if len(b) > core.HashLen {
		b = b[len(b)-core.HashLen:]
	}
	copy(h[core.HashLen-len(b):], b)
	return h
}

func addrFromWord(v *big.Int) core.Address {
	h := bigToHash(v)
	var a core.Address
	copy(a[:], h[core.HashLen-core.AddrLen:])
	return a
}
