package pos

import (
	"encoding/binary"

	"l1chain/core"
)

// AttestAddress is the reserved account a PoS-mode checkpoint attestation is
// sent to -- the same "routes past the ordinary transfer/contract dispatch"
// shape exchange.Address and evm.DeployAddress already use. 0x50 0x4F 0x53
// spells "POS" in ASCII, mirroring evm.DeployAddress's own "0x45 0x56 0x4D
// spells EVM" convention.
var AttestAddress = core.Address{
	0x50, 0x4F, 0x53, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

// CheckpointInterval is the number of blocks between checkpoint heights a
// validator attests to -- mirrors consensus.RetargetInterval's own "every N
// blocks" convention. Height % CheckpointInterval == 0 marks a checkpoint.
const CheckpointInterval = 32

// OpAttest is attestation calldata's only opcode today (mirrors exchange's
// OpPlace/OpCancel byte-tag convention, in AttestAddress's own, independent
// op-byte namespace).
const OpAttest = 0xA1

// attestPayloadLen is the fixed encoded length: op(1) || targetHeight(8) ||
// targetHash(32) || blsSig(96).
const attestPayloadLen = 1 + 8 + core.HashLen + sigSize

// EncodeAttest builds the calldata for a checkpoint attestation: a validator
// votes that the block at targetHeight has hash targetHash, signing that
// claim with a BLS signature (see bls.go's Key.Sign, under DST(chainID)).
//
//	op(1) || targetHeight(8, big-endian) || targetHash(32) || blsSig(96)
func EncodeAttest(targetHeight uint64, targetHash core.Hash, blsSig []byte) []byte {
	out := make([]byte, attestPayloadLen)
	out[0] = OpAttest
	binary.BigEndian.PutUint64(out[1:9], targetHeight)
	copy(out[9:9+core.HashLen], targetHash[:])
	copy(out[9+core.HashLen:], blsSig)
	return out
}

// DecodeAttest is EncodeAttest's inverse.
func DecodeAttest(data []byte) (targetHeight uint64, targetHash core.Hash, blsSig []byte, err error) {
	if len(data) != attestPayloadLen || data[0] != OpAttest {
		return 0, core.Hash{}, nil, ErrMalformedAttestation
	}
	targetHeight = binary.BigEndian.Uint64(data[1:9])
	copy(targetHash[:], data[9:9+core.HashLen])
	blsSig = append([]byte(nil), data[9+core.HashLen:]...)
	return targetHeight, targetHash, blsSig, nil
}

// IsAttestationTx reports whether tx is a checkpoint attestation, mirroring
// exchange.IsExchangeTx's exact one-line shape.
func IsAttestationTx(tx core.Transaction) bool { return tx.To == AttestAddress }

// CheckpointRound is the attestation round a given target height belongs to
// -- the unit equivocation is checked against (see the M8 plan: a validator
// may cast at most one vote per round; a second, conflicting vote for the
// SAME round is equivocation).
func CheckpointRound(targetHeight uint64) uint64 { return targetHeight / CheckpointInterval }
