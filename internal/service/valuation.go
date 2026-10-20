package service

import (
	"math/big"
	"sort"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
)

// value converts raw amounts to UI amounts, computes each holder's share and
// USD value, and sorts by share. Rules:
//
//   - lp_token: share = LP balance / LP supply (exact protocol math; the list
//     can sum below 100% when LP sits in pool-owned or burned accounts).
//   - position: share = holder value / total value, valued in token B at the
//     pool spot price. Summing raw liquidity across different tick or bin
//     ranges is not meaningful, so it is only used when no value is known.
//
// usdPerB is the USD price of one token B (0 when unknown).
func value(pool *domain.Pool, holders []domain.Holder, usdPerB float64) float64 {
	totalInB := 0.0
	for i := range holders {
		h := &holders[i]
		h.AmountA = poolmath.ToUI(h.AmountARaw, pool.DecimalsA)
		h.AmountB = poolmath.ToUI(h.AmountBRaw, pool.DecimalsB)
		h.ValueInB = h.AmountA*pool.PriceAInB + h.AmountB
		totalInB += h.ValueInB
	}

	switch {
	case pool.Kind == domain.KindLPToken:
		for i := range holders {
			holders[i].Percentage = ratioPercent(holders[i].Liquidity, pool.LPSupply)
		}
	case totalInB > 0:
		for i := range holders {
			holders[i].Percentage = holders[i].ValueInB / totalInB * 100
		}
	default:
		total := new(big.Int)
		for i := range holders {
			total.Add(total, holders[i].Liquidity)
		}
		for i := range holders {
			holders[i].Percentage = ratioPercent(holders[i].Liquidity, total)
		}
	}

	totalUSD := 0.0
	if usdPerB > 0 {
		for i := range holders {
			holders[i].ValueUSD = holders[i].ValueInB * usdPerB
			totalUSD += holders[i].ValueUSD
		}
	}

	sort.SliceStable(holders, func(i, j int) bool {
		if holders[i].Percentage != holders[j].Percentage {
			return holders[i].Percentage > holders[j].Percentage
		}
		if holders[i].PositionAddress != holders[j].PositionAddress {
			return holders[i].PositionAddress < holders[j].PositionAddress
		}
		return holders[i].Wallet < holders[j].Wallet
	})
	return totalUSD
}

// usdPerTokenB derives the USD price of token B from whichever price is known.
func usdPerTokenB(pool *domain.Pool, prices map[string]float64) float64 {
	if p := prices[pool.MintB]; p > 0 {
		return p
	}
	if p := prices[pool.MintA]; p > 0 && pool.PriceAInB > 0 {
		return p / pool.PriceAInB
	}
	return 0
}

func ratioPercent(part, total *big.Int) float64 {
	if part == nil || total == nil || total.Sign() == 0 {
		return 0
	}
	r := new(big.Rat).SetFrac(part, total)
	f, _ := r.Float64()
	return f * 100
}

// groupByWallet merges positions of the same wallet. Input must be valued;
// output is re-sorted by share.
func groupByWallet(holders []domain.Holder) []domain.Holder {
	idx := make(map[string]int, len(holders))
	out := make([]domain.Holder, 0, len(holders))
	for _, h := range holders {
		i, ok := idx[h.Wallet]
		if !ok || h.Wallet == "" {
			idx[h.Wallet] = len(out)
			out = append(out, domain.Holder{
				Wallet:        h.Wallet,
				Liquidity:     new(big.Int),
				AmountARaw:    new(big.Int),
				AmountBRaw:    new(big.Int),
				PositionCount: 0,
			})
			i = len(out) - 1
		}
		g := &out[i]
		g.Liquidity.Add(g.Liquidity, nonNil(h.Liquidity))
		g.AmountARaw.Add(g.AmountARaw, nonNil(h.AmountARaw))
		g.AmountBRaw.Add(g.AmountBRaw, nonNil(h.AmountBRaw))
		g.AmountA += h.AmountA
		g.AmountB += h.AmountB
		g.ValueInB += h.ValueInB
		g.ValueUSD += h.ValueUSD
		g.Percentage += h.Percentage
		g.PositionCount++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Percentage != out[j].Percentage {
			return out[i].Percentage > out[j].Percentage
		}
		return out[i].Wallet < out[j].Wallet
	})
	return out
}

func nonNil(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}
