package holders

import (
	"context"
	"fmt"
	"math/big"

	"golang.org/x/sync/errgroup"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// AccountsFetcher fetches full accounts by address.
type AccountsFetcher interface {
	GetMultipleAccounts(ctx context.Context, addresses []string) ([]*rpc.Account, error)
}

// DLMMStrategy values Meteora DLMM positions exactly from bin arrays:
//
//	amountX = Σ share_i * bin_i.amount_x / bin_i.liquidity_supply
//	amountY = Σ share_i * bin_i.amount_y / bin_i.liquidity_supply
//
// RPC cost: 3 getProgramAccounts in parallel (owner+shares slice, bin-range
// slice, the pair's bin arrays) plus getMultipleAccounts for positions wider
// than 70 bins, whose extra shares live in an account extension. No owner
// resolution is needed: DLMM positions store the owner directly.
type DLMMStrategy struct {
	Scanner      Scanner
	Accounts     AccountsFetcher
	MaxPositions int
}

type dlmmPosition struct {
	address      string
	owner        string
	lower, upper int32
	shares       []*big.Int // one per bin from lower to upper
}

// Holders implements Strategy.
func (s DLMMStrategy) Holders(ctx context.Context, pool *domain.Pool) (*Result, error) {
	positions, bins, skipped, err := s.scan(ctx, pool.Address)
	if err != nil {
		return nil, err
	}
	positions, vanished, err := s.loadExtensions(ctx, positions)
	if err != nil {
		return nil, err
	}
	skipped += vanished

	holders := make([]domain.Holder, 0, len(positions))
	missingBins := 0
	for _, p := range positions {
		v := valueDLMMPosition(p.shares, p.lower, bins)
		if v.liquidity.Sign() == 0 {
			continue // empty position kept open
		}
		missingBins += v.missingBins
		lower, upper := p.lower, p.upper
		holders = append(holders, domain.Holder{
			Wallet:          p.owner,
			PositionAddress: p.address,
			Liquidity:       v.liquidity,
			AmountARaw:      v.x,
			AmountBRaw:      v.y,
			BinIDLower:      &lower,
			BinIDUpper:      &upper,
		})
	}
	return &Result{
		Holders:    holders,
		Warnings:   dlmmWarnings(skipped, missingBins),
		Incomplete: skipped > 0 || missingBins > 0,
	}, nil
}

func (s DLMMStrategy) scan(ctx context.Context, pair string) ([]*dlmmPosition, map[int32]protocol.DLMMBin, int, error) {
	adapter := protocol.MeteoraDLMM{}
	var sharesAccts, rangeAccts, binAccts []rpc.Account
	g, gctx := errgroup.WithContext(ctx)
	run := func(dst *[]rpc.Account, q rpc.ProgramAccountsQuery) func() error {
		return func() error {
			out, err := s.Scanner.Scan(gctx, q)
			*dst = out
			return err
		}
	}
	g.Go(run(&sharesAccts, adapter.PositionSharesQuery(pair)))
	g.Go(run(&rangeAccts, adapter.PositionRangeQuery(pair)))
	g.Go(run(&binAccts, adapter.BinArrayQuery(pair)))
	if err := g.Wait(); err != nil {
		return nil, nil, 0, fmt.Errorf("scan dlmm accounts: %w", err)
	}
	if err := checkSize(len(sharesAccts), s.MaxPositions); err != nil {
		return nil, nil, 0, err
	}

	bins := make(map[int32]protocol.DLMMBin, len(binAccts)*protocol.DLMMBinsPerArray)
	for _, a := range binAccts {
		if err := protocol.DecodeDLMMBinArray(a.Data, bins); err != nil {
			return nil, nil, 0, fmt.Errorf("decode bin array %s: %w", a.Pubkey, err)
		}
	}

	type binRange struct{ lower, upper int32 }
	ranges := make(map[string]binRange, len(rangeAccts))
	for _, a := range rangeAccts {
		lo, hi, err := protocol.DecodeDLMMRangeSlice(a.Data)
		// Widths come from chain data: compute in int64 so a corrupt range cannot
		// overflow, and reject anything the program itself would not allow.
		if width := int64(hi) - int64(lo) + 1; err == nil && width >= 1 && width <= protocol.DLMMMaxPositionWidth {
			ranges[a.Pubkey] = binRange{lo, hi}
		}
	}

	positions := make([]*dlmmPosition, 0, len(sharesAccts))
	skipped := 0
	for _, a := range sharesAccts {
		shares, err := protocol.DecodeDLMMSharesSlice(a.Data)
		r, ok := ranges[a.Pubkey]
		if err != nil || !ok {
			skipped++ // created or closed between the scans
			continue
		}
		p := &dlmmPosition{address: a.Pubkey, owner: shares.Owner, lower: r.lower, upper: r.upper}
		base := min(p.width(), protocol.DLMMBinsPerPosition)
		p.shares = append(make([]*big.Int, 0, p.width()), shares.Shares[:base]...)
		positions = append(positions, p)
	}
	return positions, bins, skipped, nil
}

// loadExtensions fetches full accounts of positions wider than 70 bins and
// appends their extension shares. Positions that were closed or resized
// since the scan are dropped and counted.
func (s DLMMStrategy) loadExtensions(ctx context.Context, positions []*dlmmPosition) ([]*dlmmPosition, int, error) {
	var wide []*dlmmPosition
	for _, p := range positions {
		if p.width() > protocol.DLMMBinsPerPosition {
			wide = append(wide, p)
		}
	}
	if len(wide) == 0 {
		return positions, 0, nil
	}
	keys := make([]string, len(wide))
	for i, p := range wide {
		keys[i] = p.address
	}
	accts, err := s.Accounts.GetMultipleAccounts(ctx, keys)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch extended dlmm positions: %w", err)
	}
	drop := make(map[*dlmmPosition]bool)
	for i, p := range wide {
		var ext []*big.Int
		if accts[i] != nil {
			ext, err = protocol.DecodeDLMMExtendedShares(accts[i].Data, p.width())
		}
		if accts[i] == nil || err != nil {
			drop[p] = true
			continue
		}
		p.shares = append(p.shares, ext...)
	}
	if len(drop) == 0 {
		return positions, 0, nil
	}
	kept := make([]*dlmmPosition, 0, len(positions)-len(drop))
	for _, p := range positions {
		if !drop[p] {
			kept = append(kept, p)
		}
	}
	return kept, len(drop), nil
}

func (p *dlmmPosition) width() int {
	return int(int64(p.upper)-int64(p.lower)) + 1
}

type dlmmValue struct {
	x, y, liquidity *big.Int
	missingBins     int
}

func valueDLMMPosition(shares []*big.Int, lower int32, bins map[int32]protocol.DLMMBin) dlmmValue {
	v := dlmmValue{x: new(big.Int), y: new(big.Int), liquidity: new(big.Int)}
	for i, share := range shares {
		if share == nil || share.Sign() == 0 {
			continue
		}
		v.liquidity.Add(v.liquidity, share)
		bin, ok := bins[lower+int32(i)]
		if !ok {
			v.missingBins++
			continue
		}
		v.x.Add(v.x, poolmath.MulDiv(share, new(big.Int).SetUint64(bin.AmountX), bin.LiquiditySupply))
		v.y.Add(v.y, poolmath.MulDiv(share, new(big.Int).SetUint64(bin.AmountY), bin.LiquiditySupply))
	}
	return v
}

func dlmmWarnings(skipped, missingBins int) []string {
	var w []string
	if skipped > 0 {
		w = append(w, fmt.Sprintf("%d positions changed between scans or had an invalid range and were skipped", skipped))
	}
	if missingBins > 0 {
		w = append(w, fmt.Sprintf("%d position bins had no bin array data", missingBins))
	}
	return w
}
