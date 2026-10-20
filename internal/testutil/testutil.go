// Package testutil builds fake on-chain accounts and loads protocol fixtures.
// It is imported by tests only.
package testutil

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"birdeye.so/lp-holder/internal/solana"
)

// BigOne returns a new big.Int equal to 1.
func BigOne() *big.Int { return big.NewInt(1) }

// Pubkey returns a deterministic valid public key for seed.
func Pubkey(seed byte) string {
	b := make([]byte, solana.PubkeySize)
	for i := range b {
		b[i] = seed
	}
	return solana.EncodeBase58(b)
}

// TokenAccount builds SPL token account data.
func TokenAccount(t testing.TB, mint, owner string, amount uint64) []byte {
	t.Helper()
	data := make([]byte, solana.TokenAccountSize)
	copy(data[0:], mustPubkey(t, mint))
	copy(data[32:], mustPubkey(t, owner))
	binary.LittleEndian.PutUint64(data[64:], amount)
	return data
}

// OwnerAmountSlice builds the owner+amount dataSlice of a token account.
func OwnerAmountSlice(t testing.TB, owner string, amount uint64) []byte {
	t.Helper()
	data := make([]byte, solana.TokenOwnerAmountSliceLength)
	copy(data, mustPubkey(t, owner))
	binary.LittleEndian.PutUint64(data[32:], amount)
	return data
}

// Mint builds SPL mint data.
func Mint(supply uint64, decimals uint8) []byte {
	data := make([]byte, solana.MintSize)
	binary.LittleEndian.PutUint64(data[36:], supply)
	data[44] = decimals
	return data
}

// Fixture is a mainnet account stored under internal/protocol/testdata.
type Fixture struct {
	Address string `json:"address"`
	Owner   string `json:"owner"`
	Data    []byte `json:"-"`
	Raw     string `json:"data"`
}

// LoadFixture loads internal/protocol/testdata/<name>.json.
func LoadFixture(t testing.TB, name string) Fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdataDir(), name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	if f.Data, err = base64.StdEncoding.DecodeString(f.Raw); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return f
}

// Expected loads internal/protocol/testdata/expected.json.
func Expected(t testing.TB) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdataDir(), "expected.json"))
	if err != nil {
		t.Fatalf("read expected.json: %v", err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse expected.json: %v", err)
	}
	return out
}

func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "protocol", "testdata")
}

func mustPubkey(t testing.TB, s string) []byte {
	t.Helper()
	pk, err := solana.DecodePubkey(s)
	if err != nil {
		t.Fatalf("bad pubkey %q: %v", s, err)
	}
	return pk[:]
}
