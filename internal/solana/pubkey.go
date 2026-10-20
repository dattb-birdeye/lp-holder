package solana

import (
	"crypto/sha256"
	"fmt"

	"github.com/mr-tron/base58"
)

// PubkeySize is the byte length of a Solana public key.
const PubkeySize = 32

// Well-known program IDs.
const (
	TokenProgramID     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	Token2022ProgramID = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

// EncodeBase58 encodes raw bytes as base58.
func EncodeBase58(b []byte) string {
	return base58.Encode(b)
}

// DecodePubkey parses a base58 public key.
func DecodePubkey(s string) ([PubkeySize]byte, error) {
	var pk [PubkeySize]byte
	b, err := base58.Decode(s)
	if err != nil {
		return pk, fmt.Errorf("invalid base58: %w", err)
	}
	if len(b) != PubkeySize {
		return pk, fmt.Errorf("pubkey must be %d bytes, got %d", PubkeySize, len(b))
	}
	copy(pk[:], b)
	return pk, nil
}

// IsValidPubkey reports whether s is a well-formed base58 public key.
func IsValidPubkey(s string) bool {
	_, err := DecodePubkey(s)
	return err == nil
}

// Discriminator returns the 8-byte Anchor account discriminator,
// sha256("account:<Name>")[:8]. Computing it avoids hand-copied magic bytes.
func Discriminator(accountName string) [8]byte {
	sum := sha256.Sum256([]byte("account:" + accountName))
	var d [8]byte
	copy(d[:], sum[:8])
	return d
}

// HasDiscriminator reports whether data starts with disc.
func HasDiscriminator(data []byte, disc [8]byte) bool {
	if len(data) < len(disc) {
		return false
	}
	return [8]byte(data[:8]) == disc
}
