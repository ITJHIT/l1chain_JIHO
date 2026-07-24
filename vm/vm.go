// Package vm defines the single execution entrypoint used by block processing.
//
// Isolating execution behind VM.Execute lets M1/M2 run with a no-op VM (plain
// value transfers only), M3 plug in a custom stack VM with gas metering, and M4
// plug in an EVM-compatible implementation — all without changing block
// validation or state plumbing.
package vm

import (
	"l1chain/core"
	"l1chain/state"
)

// Message is a contract-execution request derived from a transaction.
type Message struct {
	From     core.Address
	To       *core.Address // nil for contract creation
	Value    uint64
	GasLimit uint64
	Nonce    uint64 // sender nonce, used to derive the created contract address
	Data     []byte
}

// Receipt is the result of executing a Message against state.
type Receipt struct {
	GasUsed         uint64
	Success         bool
	ReturnData      []byte
	ContractAddress *core.Address // set on creation
	Err             error
}

// VM executes a message against a StateDB and returns a receipt.
type VM interface {
	Execute(st state.StateDB, msg Message) Receipt
}

// NoopVM is the M1/M2 execution engine: it performs no contract logic and
// reports zero gas used. Plain value transfers are handled by the state
// transition function, not the VM, so a Message with empty Data is a no-op.
type NoopVM struct{}

// Execute implements VM. It succeeds with zero gas for empty-data messages and
// fails for any message that carries contract data (no VM wired yet).
func (NoopVM) Execute(_ state.StateDB, msg Message) Receipt {
	if len(msg.Data) == 0 {
		return Receipt{Success: true}
	}
	return Receipt{Success: false, Err: ErrNoVM}
}

// ErrNoVM is returned when contract execution is requested before a real VM
// (M3/M4) is installed.
var ErrNoVM = vmError("contract execution not supported until M3 (custom VM)")

type vmError string

func (e vmError) Error() string { return string(e) }
