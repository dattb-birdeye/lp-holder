package holders

import (
	"context"
	"fmt"
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/protocol"
)

// TickStrategy lists Raydium CLMM / Orca Whirlpool positions.
//
// RPC cost: 1 getProgramAccounts (sliced) + owner resolution
// (1 DAS call per 1000 positions, or the token-account fallback).
type TickStrategy struct {
	Adapter      protocol.TickAdapter
	Scanner      Scanner
	Owners       OwnerResolver
	MaxPositions int
}

// Holders implements Strategy.
func (s TickStrategy) Holders(ctx context.Context, pool *domain.Pool) (*Result, error) {
	accounts, err := s.Scanner.Scan(ctx, s.Adapter.PositionQuery(pool.Address))
	if err != nil {
		return nil, fmt.Errorf("scan %s positions: %w", pool.Protocol, err)
	}
	if err := checkSize(len(accounts), s.MaxPositions); err != nil {
		return nil, err
	}

	type entry struct {
		address string
		pos     protocol.TickPosition
	}
	entries := make([]entry, 0, len(accounts))
	mints := make([]string, 0, len(accounts))
	skipped := 0
	for _, a := range accounts {
		pos, err := s.Adapter.DecodePositionSlice(a.Data)
		if err != nil {
			skipped++
			continue
		}
		if pos.Liquidity.Sign() == 0 {
			continue // emptied position that was not closed
		}
		entries = append(entries, entry{address: a.Pubkey, pos: pos})
		mints = append(mints, pos.NFTMint)
	}

	owners, err := s.Owners.Resolve(ctx, mints)
	if err != nil {
		return nil, fmt.Errorf("resolve position owners: %w", err)
	}

	sqrtAt := tickSqrtCache{}
	holders := make([]domain.Holder, 0, len(entries))
	for _, e := range entries {
		a, b := poolmath.ConcentratedAmounts(e.pos.Liquidity, pool.SqrtPriceX64,
			sqrtAt.get(e.pos.TickLower), sqrtAt.get(e.pos.TickUpper))
		lower, upper := e.pos.TickLower, e.pos.TickUpper
		holders = append(holders, domain.Holder{
			Wallet:          owners[e.pos.NFTMint],
			PositionAddress: e.address,
			Liquidity:       e.pos.Liquidity,
			AmountARaw:      a,
			AmountBRaw:      b,
			TickLower:       &lower,
			TickUpper:       &upper,
		})
	}
	return newPositionResult(holders, skipped, len(entries)-len(owners)), nil
}

// tickSqrtCache memoises tick -> sqrt price; many positions share ticks.
type tickSqrtCache map[int32]*big.Int

func (c tickSqrtCache) get(tick int32) *big.Int {
	if v, ok := c[tick]; ok {
		return v
	}
	v := poolmath.TickToSqrtPriceX64(tick)
	c[tick] = v
	return v
}

// DAMMV2Strategy lists Meteora DAMM v2 positions. All positions share the
// pool's [sqrt_min, sqrt_max] range, so liquidity share equals value share.
type DAMMV2Strategy struct {
	Scanner      Scanner
	Owners       OwnerResolver
	MaxPositions int
}

// Holders implements Strategy.
func (s DAMMV2Strategy) Holders(ctx context.Context, pool *domain.Pool) (*Result, error) {
	adapter := protocol.MeteoraDAMMV2{}
	accounts, err := s.Scanner.Scan(ctx, adapter.PositionQuery(pool.Address))
	if err != nil {
		return nil, fmt.Errorf("scan damm v2 positions: %w", err)
	}
	if err := checkSize(len(accounts), s.MaxPositions); err != nil {
		return nil, err
	}

	type entry struct {
		address string
		pos     protocol.NFTPosition
	}
	entries := make([]entry, 0, len(accounts))
	mints := make([]string, 0, len(accounts))
	skipped := 0
	for _, a := range accounts {
		pos, err := adapter.DecodePositionSlice(a.Data)
		if err != nil {
			skipped++
			continue
		}
		if pos.Liquidity.Sign() == 0 {
			continue
		}
		entries = append(entries, entry{address: a.Pubkey, pos: pos})
		mints = append(mints, pos.NFTMint)
	}

	owners, err := s.Owners.Resolve(ctx, mints)
	if err != nil {
		return nil, fmt.Errorf("resolve position owners: %w", err)
	}

	holders := make([]domain.Holder, 0, len(entries))
	for _, e := range entries {
		a, b := poolmath.DAMMV2Amounts(e.pos.Liquidity, pool.SqrtPriceX64, pool.SqrtMinPriceX64, pool.SqrtMaxPriceX64)
		holders = append(holders, domain.Holder{
			Wallet:          owners[e.pos.NFTMint],
			PositionAddress: e.address,
			Liquidity:       e.pos.Liquidity,
			AmountARaw:      a,
			AmountBRaw:      b,
		})
	}
	return newPositionResult(holders, skipped, len(entries)-len(owners)), nil
}

func newPositionResult(holders []domain.Holder, skipped, unresolved int) *Result {
	res := &Result{Holders: holders, Incomplete: skipped > 0 || unresolved > 0}
	if skipped > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d position accounts could not be decoded", skipped))
	}
	if unresolved > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d position owners could not be resolved (wallet is empty)", unresolved))
	}
	return res
}
