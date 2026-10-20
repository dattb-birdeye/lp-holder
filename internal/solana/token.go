package solana

// SPL token layouts. Token-2022 shares the same base layout and appends
// extensions after it, so these parsers work for both programs.
const (
	// TokenAccountSize is the size of a plain SPL Token account (no extensions).
	TokenAccountSize = 165
	// MintSize is the size of a plain SPL Mint (no extensions).
	MintSize = 82

	tokenAccountMintOffset   = 0
	tokenAccountOwnerOffset  = 32
	tokenAccountAmountOffset = 64
	mintSupplyOffset         = 36
	mintDecimalsOffset       = 44

	// TokenOwnerAmountSlice selects owner(32) + amount(8) from a token account.
	// Used as getProgramAccounts dataSlice to cut payload from 165 to 40 bytes.
	TokenOwnerAmountSliceOffset = tokenAccountOwnerOffset
	TokenOwnerAmountSliceLength = PubkeySize + 8
)

// TokenAccount is the base part of an SPL token account.
type TokenAccount struct {
	Mint   string
	Owner  string
	Amount uint64
}

// ParseTokenAccount decodes an SPL Token or Token-2022 account.
func ParseTokenAccount(data []byte) (TokenAccount, error) {
	l := NewLayout(data)
	if err := l.Require(tokenAccountAmountOffset + 8); err != nil {
		return TokenAccount{}, err
	}
	return TokenAccount{
		Mint:   l.Pubkey(tokenAccountMintOffset),
		Owner:  l.Pubkey(tokenAccountOwnerOffset),
		Amount: l.U64(tokenAccountAmountOffset),
	}, nil
}

// ParseOwnerAmountSlice decodes the owner+amount dataSlice of a token account.
func ParseOwnerAmountSlice(slice []byte) (owner string, amount uint64, err error) {
	l := NewLayout(slice)
	if err := l.Require(TokenOwnerAmountSliceLength); err != nil {
		return "", 0, err
	}
	return l.Pubkey(0), l.U64(PubkeySize), nil
}

// Mint is the base part of an SPL mint.
type Mint struct {
	Supply   uint64
	Decimals uint8
}

// ParseMint decodes an SPL Token or Token-2022 mint.
func ParseMint(data []byte) (Mint, error) {
	l := NewLayout(data)
	if err := l.Require(mintDecimalsOffset + 1); err != nil {
		return Mint{}, err
	}
	return Mint{
		Supply:   l.U64(mintSupplyOffset),
		Decimals: l.U8(mintDecimalsOffset),
	}, nil
}

// IsTokenProgram reports whether owner is SPL Token or Token-2022.
func IsTokenProgram(owner string) bool {
	return owner == TokenProgramID || owner == Token2022ProgramID
}
