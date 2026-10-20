package protocol

import (
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana"
)

// Program IDs of LP-token protocols.
const (
	RaydiumAMMV4ProgramID  = "675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8"
	RaydiumCPMMProgramID   = "CPMMoo8L3F4NbTegBCKVNunggL7H1ZpdTHKxQB5qKP1C"
	LifinityV2ProgramID    = "2wT8Yq49kHgDzXuPxZSaeLaH1qbmGXtEyPy64bL7aD3c"
	MeteoraDAMMV1ProgramID = "Eo7WjKq67rjJQSZxS6z3YkapzY3eMj6Xy8X5EQVn5UaB"
)

// ---------------------------------------------------------------------------
// Raydium AMM v4 — AmmInfo, no discriminator, fixed size 752.
// Offsets match Raydium SDK LIQUIDITY_STATE_LAYOUT_V4 (base mint @400, quote @432).
// ---------------------------------------------------------------------------

const (
	ammV4Size            = 752
	ammV4NeedTakePnlCoin = 192 // StateData starts right after 16 u64 + Fees(8 u64)
	ammV4NeedTakePnlPC   = 200
	ammV4CoinVault       = 336
	ammV4PCVault         = 368
	ammV4CoinMint        = 400
	ammV4PCMint          = 432
	ammV4LPMint          = 464
	ammV4LPAmount        = 720
)

// RaydiumAMMV4 decodes Raydium AMM v4 pools.
type RaydiumAMMV4 struct{}

func (RaydiumAMMV4) Protocol() domain.Protocol { return domain.ProtocolRaydiumAMMV4 }
func (RaydiumAMMV4) ProgramID() string         { return RaydiumAMMV4ProgramID }
func (RaydiumAMMV4) Match(data []byte) bool    { return len(data) == ammV4Size }

func (RaydiumAMMV4) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, ammV4Size, "raydium amm v4 pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:  address,
		Protocol: domain.ProtocolRaydiumAMMV4,
		Kind:     domain.KindLPToken,
		MintA:    l.Pubkey(ammV4CoinMint),
		MintB:    l.Pubkey(ammV4PCMint),
		VaultA:   l.Pubkey(ammV4CoinVault),
		VaultB:   l.Pubkey(ammV4PCVault),
		LPMint:   l.Pubkey(ammV4LPMint),
		// Withdraw math divides by AmmInfo.lp_amount, not the mint supply.
		LPSupply: new(big.Int).SetUint64(l.U64(ammV4LPAmount)),
		Reserve: domain.ReserveSpec{
			ExcludeA: l.U64(ammV4NeedTakePnlCoin),
			ExcludeB: l.U64(ammV4NeedTakePnlPC),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Raydium CPMM — PoolState (Anchor), size 637.
// ---------------------------------------------------------------------------

const (
	cpmmVault0        = 72
	cpmmVault1        = 104
	cpmmLPMint        = 136
	cpmmMint0         = 168
	cpmmMint1         = 200
	cpmmLPSupply      = 333
	cpmmProtocolFees0 = 341
	cpmmProtocolFees1 = 349
	cpmmFundFees0     = 357
	cpmmFundFees1     = 365
	cpmmCreatorFees0  = 397
	cpmmCreatorFees1  = 405
	cpmmMinSize       = 413
)

// Same Anchor name ("PoolState") as Raydium CLMM; programs differ.
var cpmmDiscriminator = solana.Discriminator("PoolState")

// RaydiumCPMM decodes Raydium CPMM pools.
type RaydiumCPMM struct{}

func (RaydiumCPMM) Protocol() domain.Protocol { return domain.ProtocolRaydiumCPMM }
func (RaydiumCPMM) ProgramID() string         { return RaydiumCPMMProgramID }
func (RaydiumCPMM) Match(data []byte) bool    { return solana.HasDiscriminator(data, cpmmDiscriminator) }

func (RaydiumCPMM) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, cpmmMinSize, "raydium cpmm pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:  address,
		Protocol: domain.ProtocolRaydiumCPMM,
		Kind:     domain.KindLPToken,
		MintA:    l.Pubkey(cpmmMint0),
		MintB:    l.Pubkey(cpmmMint1),
		VaultA:   l.Pubkey(cpmmVault0),
		VaultB:   l.Pubkey(cpmmVault1),
		LPMint:   l.Pubkey(cpmmLPMint),
		LPSupply: new(big.Int).SetUint64(l.U64(cpmmLPSupply)),
		// Vaults also hold protocol, fund and creator fees that LPs cannot withdraw.
		Reserve: domain.ReserveSpec{
			ExcludeA: l.U64(cpmmProtocolFees0) + l.U64(cpmmFundFees0) + l.U64(cpmmCreatorFees0),
			ExcludeB: l.U64(cpmmProtocolFees1) + l.U64(cpmmFundFees1) + l.U64(cpmmCreatorFees1),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Lifinity AMM v2 — Amm (Anchor), size 911.
// ---------------------------------------------------------------------------

const (
	lifinityTokenAAccount = 158
	lifinityTokenBAccount = 190
	lifinityPoolMint      = 222
	lifinityTokenAMint    = 254
	lifinityTokenBMint    = 286
	lifinityMinSize       = 318
)

var lifinityDiscriminator = solana.Discriminator("Amm")

// LifinityV2 decodes Lifinity v2 pools.
type LifinityV2 struct{}

func (LifinityV2) Protocol() domain.Protocol { return domain.ProtocolLifinityV2 }
func (LifinityV2) ProgramID() string         { return LifinityV2ProgramID }
func (LifinityV2) Match(data []byte) bool {
	return solana.HasDiscriminator(data, lifinityDiscriminator)
}

func (LifinityV2) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, lifinityMinSize, "lifinity v2 pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:  address,
		Protocol: domain.ProtocolLifinityV2,
		Kind:     domain.KindLPToken,
		MintA:    l.Pubkey(lifinityTokenAMint),
		MintB:    l.Pubkey(lifinityTokenBMint),
		VaultA:   l.Pubkey(lifinityTokenAAccount),
		VaultB:   l.Pubkey(lifinityTokenBAccount),
		LPMint:   l.Pubkey(lifinityPoolMint),
	}, nil
}

// ---------------------------------------------------------------------------
// Meteora DAMM v1 (Dynamic AMM / "Meteora Pools") — Pool (Anchor).
// The pool owns LP shares of *shared* Meteora vaults (one vault per mint for
// all pools), so vault token balances must never be used as pool reserves.
// ---------------------------------------------------------------------------

const (
	dammV1LPMint    = 8
	dammV1MintA     = 40
	dammV1MintB     = 72
	dammV1AVault    = 104
	dammV1BVault    = 136
	dammV1AVaultLP  = 168
	dammV1BVaultLP  = 200
	dammV1MinSize   = 232
	meteoraVaultMin = 147
	// Meteora Vault layout.
	meteoraVaultTotalAmount = 11
	meteoraVaultTokenVault  = 19
	meteoraVaultLPMint      = 115
)

var (
	dammPoolDiscriminator     = solana.Discriminator("Pool") // same name in DAMM v1 and v2
	meteoraVaultDiscriminator = solana.Discriminator("Vault")
)

// MeteoraDAMMV1 decodes Meteora DAMM v1 pools.
type MeteoraDAMMV1 struct{}

func (MeteoraDAMMV1) Protocol() domain.Protocol { return domain.ProtocolMeteoraDAMMV1 }
func (MeteoraDAMMV1) ProgramID() string         { return MeteoraDAMMV1ProgramID }
func (MeteoraDAMMV1) Match(data []byte) bool {
	return solana.HasDiscriminator(data, dammPoolDiscriminator)
}

func (MeteoraDAMMV1) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, dammV1MinSize, "meteora damm v1 pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:  address,
		Protocol: domain.ProtocolMeteoraDAMMV1,
		Kind:     domain.KindLPToken,
		MintA:    l.Pubkey(dammV1MintA),
		MintB:    l.Pubkey(dammV1MintB),
		VaultA:   l.Pubkey(dammV1AVault),
		VaultB:   l.Pubkey(dammV1BVault),
		LPMint:   l.Pubkey(dammV1LPMint),
		Reserve: domain.ReserveSpec{
			MeteoraVaultA:   l.Pubkey(dammV1AVault),
			MeteoraVaultB:   l.Pubkey(dammV1BVault),
			VaultLPAccountA: l.Pubkey(dammV1AVaultLP),
			VaultLPAccountB: l.Pubkey(dammV1BVaultLP),
		},
	}, nil
}

// MeteoraVault is the part of a Meteora vault account needed for reserves.
type MeteoraVault struct {
	TotalAmount uint64
	TokenVault  string
	LPMint      string
}

// DecodeMeteoraVault decodes a Meteora vault account.
func DecodeMeteoraVault(data []byte) (MeteoraVault, error) {
	if !solana.HasDiscriminator(data, meteoraVaultDiscriminator) {
		return MeteoraVault{}, errNotAccount("meteora vault")
	}
	l, err := newLayout(data, meteoraVaultMin, "meteora vault")
	if err != nil {
		return MeteoraVault{}, err
	}
	return MeteoraVault{
		TotalAmount: l.U64(meteoraVaultTotalAmount),
		TokenVault:  l.Pubkey(meteoraVaultTokenVault),
		LPMint:      l.Pubkey(meteoraVaultLPMint),
	}, nil
}
