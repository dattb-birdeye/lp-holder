package protocol

import (
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// MeteoraDLMMProgramID is the Meteora DLMM (Liquidity Book) program.
const MeteoraDLMMProgramID = "LBUZKhRxPF3XUpBCjp4YzTKgLccjZhTSDM9YuVaPwxo"

// DLMM layouts (match dlmm-sdk/idls/dlmm.json and mainnet).
const (
	// LbPair: StaticParameters(32) + VariableParameters(32) + 4 seed bytes, then:
	dlmmActiveID = 76
	dlmmBinStep  = 80
	dlmmMintX    = 88
	dlmmMintY    = 120
	dlmmReserveX = 152
	dlmmReserveY = 184
	dlmmPairMin  = 216

	// PositionV2: lb_pair@8, owner@40, liquidity_shares [u128;70]@72,
	// reward_infos [48B;70]@1192, fee_infos [48B;70]@4552,
	// lower_bin_id@7912, upper_bin_id@7916. Base size 8120.
	// Positions wider than 70 bins append one PositionBinData per extra bin
	// after the base struct: liquidity_share u128 + reward(48) + fee(48) = 112 bytes.
	DLMMBinsPerPosition   = 70
	DLMMMaxPositionWidth  = 1400 // IDL constant POSITION_MAX_LENGTH
	dlmmPosPairOffset     = 8
	dlmmPosOwnerOffset    = 40
	dlmmPosLowerOffset    = 7912
	dlmmPosBaseSize       = 8120
	dlmmPosExtBinSize     = 112
	dlmmPosSharesSliceLen = solana.PubkeySize + DLMMBinsPerPosition*16 // owner + shares
	dlmmPosRangeSliceLen  = 8

	// BinArray: index i64@8, version@16, lb_pair@24, bins [Bin;70]@56.
	// Bin: amount_x u64@0, amount_y u64@8, price u128@16, liquidity_supply u128@32. 144 bytes.
	DLMMBinsPerArray     = 70
	dlmmBinArrayIndex    = 8
	dlmmBinArrayPair     = 24
	dlmmBinArrayBins     = 56
	dlmmBinSize          = 144
	dlmmBinArraySize     = dlmmBinArrayBins + DLMMBinsPerArray*dlmmBinSize // 10136
	dlmmBinAmountYOffset = 8
	dlmmBinPriceOffset   = 16
	dlmmBinSupplyOffset  = 32
)

var (
	dlmmPairDiscriminator     = solana.Discriminator("LbPair")
	dlmmPositionDiscriminator = solana.Discriminator("PositionV2")
	dlmmBinArrayDiscriminator = solana.Discriminator("BinArray")
)

// MeteoraDLMM decodes Meteora DLMM pairs, positions and bin arrays.
type MeteoraDLMM struct{}

func (MeteoraDLMM) Protocol() domain.Protocol { return domain.ProtocolMeteoraDLMM }
func (MeteoraDLMM) ProgramID() string         { return MeteoraDLMMProgramID }
func (MeteoraDLMM) Match(data []byte) bool {
	return solana.HasDiscriminator(data, dlmmPairDiscriminator)
}

func (MeteoraDLMM) DecodePool(address string, data []byte) (*domain.Pool, error) {
	l, err := newLayout(data, dlmmPairMin, "meteora dlmm pair")
	if err != nil {
		return nil, err
	}
	return &domain.Pool{
		Address:     address,
		Protocol:    domain.ProtocolMeteoraDLMM,
		Kind:        domain.KindPosition,
		MintA:       l.Pubkey(dlmmMintX),
		MintB:       l.Pubkey(dlmmMintY),
		VaultA:      l.Pubkey(dlmmReserveX),
		VaultB:      l.Pubkey(dlmmReserveY),
		ActiveBinID: ptr(l.I32(dlmmActiveID)),
		BinStep:     ptr(l.U16(dlmmBinStep)),
	}, nil
}

func dlmmPositionFilters(pair string) []rpc.Filter {
	return []rpc.Filter{
		rpc.Memcmp(0, dlmmPositionDiscriminator[:]),
		rpc.MemcmpPubkey(dlmmPosPairOffset, pair),
	}
}

// PositionSharesQuery scans owner + liquidity shares of every position (1152 of 8120 bytes).
func (MeteoraDLMM) PositionSharesQuery(pair string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program:   MeteoraDLMMProgramID,
		Filters:   dlmmPositionFilters(pair),
		DataSlice: sliceQuery(dlmmPosOwnerOffset, dlmmPosSharesSliceLen),
	}
}

// PositionRangeQuery scans lower/upper bin ids of every position (8 bytes each).
func (MeteoraDLMM) PositionRangeQuery(pair string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program:   MeteoraDLMMProgramID,
		Filters:   dlmmPositionFilters(pair),
		DataSlice: sliceQuery(dlmmPosLowerOffset, dlmmPosRangeSliceLen),
	}
}

// BinArrayQuery scans every bin array of a pair.
func (MeteoraDLMM) BinArrayQuery(pair string) rpc.ProgramAccountsQuery {
	return rpc.ProgramAccountsQuery{
		Program: MeteoraDLMMProgramID,
		Filters: []rpc.Filter{
			rpc.Memcmp(0, dlmmBinArrayDiscriminator[:]),
			rpc.MemcmpPubkey(dlmmBinArrayPair, pair),
		},
	}
}

// DLMMShares is the owner and per-bin liquidity shares of a position.
type DLMMShares struct {
	Owner  string
	Shares [DLMMBinsPerPosition]*big.Int
}

// DecodeDLMMSharesSlice decodes a PositionSharesQuery slice.
func DecodeDLMMSharesSlice(slice []byte) (DLMMShares, error) {
	l, err := newLayout(slice, dlmmPosSharesSliceLen, "dlmm position shares")
	if err != nil {
		return DLMMShares{}, err
	}
	out := DLMMShares{Owner: l.Pubkey(0)}
	for i := range DLMMBinsPerPosition {
		out.Shares[i] = l.U128(solana.PubkeySize + i*16)
	}
	return out, nil
}

// DecodeDLMMRangeSlice decodes a PositionRangeQuery slice.
func DecodeDLMMRangeSlice(slice []byte) (lower, upper int32, err error) {
	l, err := newLayout(slice, dlmmPosRangeSliceLen, "dlmm position range")
	if err != nil {
		return 0, 0, err
	}
	return l.I32(0), l.I32(4), nil
}

// DecodeDLMMExtendedShares returns the shares of bins 70..width-1 from a full
// PositionV2 account (the extension after the 8120-byte base struct).
func DecodeDLMMExtendedShares(data []byte, width int) ([]*big.Int, error) {
	extra := width - DLMMBinsPerPosition
	if extra <= 0 {
		return nil, nil
	}
	l, err := newLayout(data, dlmmPosBaseSize+extra*dlmmPosExtBinSize, "dlmm extended position")
	if err != nil {
		return nil, err
	}
	out := make([]*big.Int, extra)
	for i := range extra {
		out[i] = l.U128(dlmmPosBaseSize + i*dlmmPosExtBinSize)
	}
	return out, nil
}

// DLMMBin is the part of a bin needed to value shares.
type DLMMBin struct {
	AmountX         uint64
	AmountY         uint64
	PriceX64        *big.Int // Q64.64, Y per X in raw units
	LiquiditySupply *big.Int
}

// DecodeDLMMBinArray returns the bins of a bin array keyed by bin id.
func DecodeDLMMBinArray(data []byte, dst map[int32]DLMMBin) error {
	if !solana.HasDiscriminator(data, dlmmBinArrayDiscriminator) {
		return errNotAccount("dlmm bin array")
	}
	l, err := newLayout(data, dlmmBinArraySize, "dlmm bin array")
	if err != nil {
		return err
	}
	first := int32(l.I64(dlmmBinArrayIndex)) * DLMMBinsPerArray
	for i := range DLMMBinsPerArray {
		off := dlmmBinArrayBins + i*dlmmBinSize
		supply := l.U128(off + dlmmBinSupplyOffset)
		if supply.Sign() == 0 {
			continue
		}
		dst[first+int32(i)] = DLMMBin{
			AmountX:         l.U64(off),
			AmountY:         l.U64(off + dlmmBinAmountYOffset),
			PriceX64:        l.U128(off + dlmmBinPriceOffset),
			LiquiditySupply: supply,
		}
	}
	return nil
}
