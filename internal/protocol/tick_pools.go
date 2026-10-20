package protocol

import (
	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// Program IDs of tick-based (Uniswap v3 style) protocols.
const (
	RaydiumCLMMProgramID   = "CAMMCzo5YL8w4VFF8KVHrK22GGUsp5VTaW7grrKgrWqK"
	OrcaWhirlpoolProgramID = "whirLbMiicVdio4qvUfM5KAg6Ct8VwpYzGff3uctyCc"
)

// ---------------------------------------------------------------------------
// Raydium CLMM — PoolState / PersonalPositionState (Anchor).
// ---------------------------------------------------------------------------

const (
	clmmMint0       = 73
	clmmMint1       = 105
	clmmVault0      = 137
	clmmVault1      = 169
	clmmDecimals0   = 233
	clmmDecimals1   = 234
	clmmSqrtPrice   = 253
	clmmTickCurrent = 269
	clmmMinSize     = 273

	// PersonalPositionState: bump@8, nft_mint@9, pool_id@41, tick_lower@73,
	// tick_upper@77, liquidity@81. The scan slices bytes [9, 97).
	clmmPosPoolOffset = 41
	clmmPosSliceStart = 9
	clmmPosSliceLen   = 88
)

var (
	clmmPoolDiscriminator     = solana.Discriminator("PoolState")
	clmmPositionDiscriminator = solana.Discriminator("PersonalPositionState")
)

// RaydiumCLMM decodes Raydium CLMM pools and positions.
type RaydiumCLMM struct{}

func (RaydiumCLMM) Protocol() domain.Protocol { return domain.ProtocolRaydiumCLMM }
func (RaydiumCLMM) ProgramID() string         { return RaydiumCLMMProgramID }
func (RaydiumCLMM) Match(data []byte) bool {
	return solana.HasDiscriminator(data, clmmPoolDiscriminator)
}

func (RaydiumCLMM) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, clmmMinSize, "raydium clmm pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:      address,
		Protocol:     domain.ProtocolRaydiumCLMM,
		Kind:         domain.KindPosition,
		MintA:        l.Pubkey(clmmMint0),
		MintB:        l.Pubkey(clmmMint1),
		VaultA:       l.Pubkey(clmmVault0),
		VaultB:       l.Pubkey(clmmVault1),
		DecimalsA:    l.U8(clmmDecimals0),
		DecimalsB:    l.U8(clmmDecimals1),
		SqrtPriceX64: l.U128(clmmSqrtPrice),
		TickCurrent:  ptr(l.I32(clmmTickCurrent)),
	}, nil
}

func (RaydiumCLMM) PositionQuery(pool string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program: RaydiumCLMMProgramID,
		Filters: []rpc.Filter{
			rpc.Memcmp(0, clmmPositionDiscriminator[:]),
			rpc.MemcmpPubkey(clmmPosPoolOffset, pool),
		},
		DataSlice: sliceQuery(clmmPosSliceStart, clmmPosSliceLen),
	}
}

func (RaydiumCLMM) DecodePositionSlice(slice []byte) (TickPosition, error) {
	l, err := newLayout(slice, clmmPosSliceLen, "raydium clmm position")
	if err != nil {
		return TickPosition{}, err
	}
	return TickPosition{
		NFTMint:   l.Pubkey(0),
		TickLower: l.I32(64),
		TickUpper: l.I32(68),
		Liquidity: l.U128(72),
	}, nil
}

// ---------------------------------------------------------------------------
// Orca Whirlpool — Whirlpool / Position (Anchor). Position size is 216.
// Bundled positions store the bundle mint in position_mint, so resolving the
// NFT owner works for them too.
// ---------------------------------------------------------------------------

const (
	whirlpoolSqrtPrice   = 65
	whirlpoolTickCurrent = 81
	whirlpoolMintA       = 101
	whirlpoolVaultA      = 133
	whirlpoolMintB       = 181
	whirlpoolVaultB      = 213
	whirlpoolMinSize     = 245

	// Position: whirlpool@8, position_mint@40, liquidity@72, tick_lower@88,
	// tick_upper@92. The scan slices bytes [40, 96).
	orcaPositionSize   = 216
	orcaPosPoolOffset  = 8
	orcaPosSliceStart  = 40
	orcaPosSliceLength = 56
)

var (
	whirlpoolDiscriminator    = solana.Discriminator("Whirlpool")
	orcaPositionDiscriminator = solana.Discriminator("Position")
)

// OrcaWhirlpool decodes Orca Whirlpool pools and positions.
type OrcaWhirlpool struct{}

func (OrcaWhirlpool) Protocol() domain.Protocol { return domain.ProtocolOrcaWhirlpool }
func (OrcaWhirlpool) ProgramID() string         { return OrcaWhirlpoolProgramID }
func (OrcaWhirlpool) Match(data []byte) bool {
	return solana.HasDiscriminator(data, whirlpoolDiscriminator)
}

func (OrcaWhirlpool) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, whirlpoolMinSize, "orca whirlpool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:      address,
		Protocol:     domain.ProtocolOrcaWhirlpool,
		Kind:         domain.KindPosition,
		MintA:        l.Pubkey(whirlpoolMintA),
		MintB:        l.Pubkey(whirlpoolMintB),
		VaultA:       l.Pubkey(whirlpoolVaultA),
		VaultB:       l.Pubkey(whirlpoolVaultB),
		SqrtPriceX64: l.U128(whirlpoolSqrtPrice),
		TickCurrent:  ptr(l.I32(whirlpoolTickCurrent)),
	}, nil
}

func (OrcaWhirlpool) PositionQuery(pool string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program: OrcaWhirlpoolProgramID,
		Filters: []rpc.Filter{
			rpc.DataSize(orcaPositionSize),
			rpc.Memcmp(0, orcaPositionDiscriminator[:]),
			rpc.MemcmpPubkey(orcaPosPoolOffset, pool),
		},
		DataSlice: sliceQuery(orcaPosSliceStart, orcaPosSliceLength),
	}
}

func (OrcaWhirlpool) DecodePositionSlice(slice []byte) (TickPosition, error) {
	l, err := newLayout(slice, orcaPosSliceLength, "orca position")
	if err != nil {
		return TickPosition{}, err
	}
	return TickPosition{
		NFTMint:   l.Pubkey(0),
		Liquidity: l.U128(32),
		TickLower: l.I32(48),
		TickUpper: l.I32(52),
	}, nil
}
