// Package solana holds chain-level primitives shared by every protocol decoder:
// fixed-offset account readers, Anchor discriminators, SPL token parsing and
// base58 helpers. It has no network dependencies.
package solana

import (
	"encoding/binary"
	"fmt"
	"math/big"
)

// Layout reads little-endian fields at fixed byte offsets.
//
// Decoders call Require(minSize) once, then read fields by offset. Offsets are
// documented next to each decoder and verified against mainnet fixtures, which
// is less error-prone than chaining "skip N bytes" calls.
type Layout struct {
	data []byte
}

// NewLayout wraps raw account data.
func NewLayout(data []byte) Layout {
	return Layout{data: data}
}

// Len returns the account data length.
func (l Layout) Len() int {
	return len(l.data)
}

// Require fails when the account is shorter than n bytes.
func (l Layout) Require(n int) error {
	if len(l.data) < n {
		return fmt.Errorf("account data too short: got %d bytes, need %d", len(l.data), n)
	}
	return nil
}

// Bytes returns a copy of n bytes starting at off.
func (l Layout) Bytes(off, n int) []byte {
	out := make([]byte, n)
	copy(out, l.data[off:off+n])
	return out
}

// Pubkey returns the base58 public key stored at off.
func (l Layout) Pubkey(off int) string {
	return EncodeBase58(l.data[off : off+PubkeySize])
}

// U8 reads a byte.
func (l Layout) U8(off int) uint8 {
	return l.data[off]
}

// U16 reads a little-endian uint16.
func (l Layout) U16(off int) uint16 {
	return binary.LittleEndian.Uint16(l.data[off:])
}

// I32 reads a little-endian int32.
func (l Layout) I32(off int) int32 {
	return int32(binary.LittleEndian.Uint32(l.data[off:]))
}

// U64 reads a little-endian uint64.
func (l Layout) U64(off int) uint64 {
	return binary.LittleEndian.Uint64(l.data[off:])
}

// I64 reads a little-endian int64.
func (l Layout) I64(off int) int64 {
	return int64(binary.LittleEndian.Uint64(l.data[off:]))
}

// U128 reads a little-endian u128 into a new big.Int.
func (l Layout) U128(off int) *big.Int {
	lo := binary.LittleEndian.Uint64(l.data[off:])
	hi := binary.LittleEndian.Uint64(l.data[off+8:])
	v := new(big.Int).SetUint64(hi)
	v.Lsh(v, 64)
	return v.Or(v, new(big.Int).SetUint64(lo))
}
