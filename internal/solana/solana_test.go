package solana

import (
	"encoding/binary"
	"math/big"
	"testing"
)

func TestDiscriminatorMatchesAnchor(t *testing.T) {
	// Known on-chain values (Orca Whirlpool, Meteora LbPair).
	cases := map[string][8]byte{
		"Whirlpool": {0x3f, 0x95, 0xd1, 0x0c, 0xe1, 0x80, 0x63, 0x09},
		"LbPair":    {0x21, 0x0b, 0x31, 0x62, 0xb5, 0x65, 0xb1, 0x0d},
	}
	for name, want := range cases {
		if got := Discriminator(name); got != want {
			t.Errorf("Discriminator(%q) = %x, want %x", name, got, want)
		}
	}
	d := Discriminator("Whirlpool")
	if !HasDiscriminator(append(d[:], 1, 2, 3), d) {
		t.Error("HasDiscriminator should match prefix")
	}
	if HasDiscriminator([]byte{1, 2}, d) {
		t.Error("HasDiscriminator must reject short data")
	}
}

func TestPubkeyValidation(t *testing.T) {
	if !IsValidPubkey(TokenProgramID) {
		t.Error("token program id must be valid")
	}
	for _, bad := range []string{"", "0OIl", "abc", TokenProgramID + "1"} {
		if IsValidPubkey(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestLayoutReaders(t *testing.T) {
	data := make([]byte, 64)
	binary.LittleEndian.PutUint64(data[0:], 7)
	binary.LittleEndian.PutUint64(data[8:], 1) // high word of u128 at 0
	binary.LittleEndian.PutUint32(data[16:], uint32(0xFFFFFFFE))
	binary.LittleEndian.PutUint16(data[20:], 513)
	l := NewLayout(data)
	want := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(7))
	if got := l.U128(0); got.Cmp(want) != 0 {
		t.Errorf("U128 = %s, want %s", got, want)
	}
	if got := l.I32(16); got != -2 {
		t.Errorf("I32 = %d, want -2", got)
	}
	if got := l.U16(20); got != 513 {
		t.Errorf("U16 = %d, want 513", got)
	}
	if got := l.I64(0); got != 7 {
		t.Errorf("I64 = %d, want 7", got)
	}
	if l.Require(65) == nil || l.Require(64) != nil {
		t.Error("Require bounds are wrong")
	}
	if got := l.Bytes(0, 2); got[0] != 7 || len(got) != 2 {
		t.Errorf("Bytes = %v", got)
	}
}

func TestParseTokenAccountAndMint(t *testing.T) {
	owner, _ := DecodePubkey(TokenProgramID)
	acct := make([]byte, TokenAccountSize)
	copy(acct[tokenAccountOwnerOffset:], owner[:])
	binary.LittleEndian.PutUint64(acct[tokenAccountAmountOffset:], 42)
	ta, err := ParseTokenAccount(acct)
	if err != nil || ta.Owner != TokenProgramID || ta.Amount != 42 {
		t.Fatalf("ParseTokenAccount = %+v, %v", ta, err)
	}
	o, amt, err := ParseOwnerAmountSlice(acct[TokenOwnerAmountSliceOffset : TokenOwnerAmountSliceOffset+TokenOwnerAmountSliceLength])
	if err != nil || o != TokenProgramID || amt != 42 {
		t.Fatalf("ParseOwnerAmountSlice = %s %d %v", o, amt, err)
	}
	if _, err := ParseTokenAccount(acct[:10]); err == nil {
		t.Error("short token account must fail")
	}

	mint := make([]byte, MintSize)
	binary.LittleEndian.PutUint64(mint[mintSupplyOffset:], 1000)
	mint[mintDecimalsOffset] = 6
	m, err := ParseMint(mint)
	if err != nil || m.Supply != 1000 || m.Decimals != 6 {
		t.Fatalf("ParseMint = %+v, %v", m, err)
	}
	if !IsTokenProgram(Token2022ProgramID) || IsTokenProgram("x") {
		t.Error("IsTokenProgram is wrong")
	}
}
