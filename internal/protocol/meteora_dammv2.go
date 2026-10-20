package protocol

import (
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// MeteoraDAMMV2ProgramID is the Meteora DAMM v2 (cp-amm) program.
const MeteoraDAMMV2ProgramID = "cpamdpZCGKUy5JxQXB4dcpGPiikHawvSWAd6mEn1sGG"

// DAMM v2 layouts.
const (
	// Pool: PoolFeesStruct(160) then mints/vaults, liquidity, fees, sqrt prices.
	dammV2MintA        = 168
	dammV2MintB        = 200
	dammV2VaultA       = 232
	dammV2VaultB       = 264
	dammV2ProtocolFeeA = 392
	dammV2ProtocolFeeB = 400
	dammV2PartnerFeeA  = 408
	dammV2PartnerFeeB  = 416
	dammV2SqrtMin      = 424
	dammV2SqrtMax      = 440
	dammV2SqrtPrice    = 456
	dammV2PoolMin      = 472

	// Position: pool@8, nft_mint@40, unlocked_liquidity@152, vested@168,
	// permanent_locked@184. The scan slices bytes [40, 200).
	dammV2PosPoolOffset = 8
	dammV2PosSliceStart = 40
	dammV2PosSliceLen   = 160
	dammV2PosUnlocked   = 152 - dammV2PosSliceStart
	dammV2PosVested     = 168 - dammV2PosSliceStart
	dammV2PosPermanent  = 184 - dammV2PosSliceStart
)

var dammV2PositionDiscriminator = solana.Discriminator("Position")

// NFTPosition is a position owned by whoever holds NFTMint.
type NFTPosition struct {
	NFTMint string
	// Liquidity is unlocked + vested + permanently locked liquidity (Q64 scaled).
	Liquidity *big.Int
	// Locked is the vested + permanently locked part.
	Locked *big.Int
}

// MeteoraDAMMV2 decodes Meteora DAMM v2 pools and positions.
type MeteoraDAMMV2 struct{}

func (MeteoraDAMMV2) Protocol() domain.Protocol { return domain.ProtocolMeteoraDAMMV2 }
func (MeteoraDAMMV2) ProgramID() string         { return MeteoraDAMMV2ProgramID }
func (MeteoraDAMMV2) Match(data []byte) bool {
	return solana.HasDiscriminator(data, dammPoolDiscriminator)
}

func (MeteoraDAMMV2) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, dammV2PoolMin, "meteora damm v2 pool")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:  address,
		Protocol: domain.ProtocolMeteoraDAMMV2,
		Kind:     domain.KindPosition,
		MintA:    l.Pubkey(dammV2MintA),
		MintB:    l.Pubkey(dammV2MintB),
		VaultA:   l.Pubkey(dammV2VaultA),
		VaultB:   l.Pubkey(dammV2VaultB),
		// A pool is concentrated between sqrt_min_price and sqrt_max_price;
		// it is only "full range" when those are the protocol extremes.
		SqrtMinPriceX64: l.U128(dammV2SqrtMin),
		SqrtMaxPriceX64: l.U128(dammV2SqrtMax),
		SqrtPriceX64:    l.U128(dammV2SqrtPrice),
		Reserve: domain.ReserveSpec{
			ExcludeA: l.U64(dammV2ProtocolFeeA) + l.U64(dammV2PartnerFeeA),
			ExcludeB: l.U64(dammV2ProtocolFeeB) + l.U64(dammV2PartnerFeeB),
		},
	}, nil
}

// PositionQuery scans all positions of a pool (160 of 408 bytes each).
func (MeteoraDAMMV2) PositionQuery(pool string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program: MeteoraDAMMV2ProgramID,
		Filters: []rpc.Filter{
			rpc.Memcmp(0, dammV2PositionDiscriminator[:]),
			rpc.MemcmpPubkey(dammV2PosPoolOffset, pool),
		},
		DataSlice: sliceQuery(dammV2PosSliceStart, dammV2PosSliceLen),
	}
}

// DecodePositionSlice decodes a PositionQuery slice.
func (MeteoraDAMMV2) DecodePositionSlice(slice []byte) (NFTPosition, error) {
	l, err := newLayout(slice, dammV2PosSliceLen, "meteora damm v2 position")
	if err != nil {
		return NFTPosition{}, err
	}
	locked := new(big.Int).Add(l.U128(dammV2PosVested), l.U128(dammV2PosPermanent))
	return NFTPosition{
		NFTMint:   l.Pubkey(0),
		Liquidity: new(big.Int).Add(l.U128(dammV2PosUnlocked), locked),
		Locked:    locked,
	}, nil
}
