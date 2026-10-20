package holders

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

const defaultPrimaryTimeout = 10 * time.Second

// LPBalance is the LP token balance of one owner (all token accounts summed).
type LPBalance struct {
	Owner  string
	Amount uint64
}

// LPHolderSource lists LP token balances of a mint.
type LPHolderSource interface {
	LPBalances(ctx context.Context, lpMint, tokenProgram string) ([]LPBalance, error)
}

// LPTokenStrategy values LP token holders: amount = reserve * balance / lpSupply.
type LPTokenStrategy struct {
	Source     LPHolderSource
	MaxHolders int // 0 = no limit
}

// Holders implements Strategy.
func (s LPTokenStrategy) Holders(ctx context.Context, pool *domain.Pool) (*Result, error) {
	if pool.LPMint == "" {
		return nil, fmt.Errorf("pool %s has no LP mint", pool.Address)
	}
	if pool.LPSupply == nil || pool.LPSupply.Sign() == 0 {
		return &Result{Warnings: []string{"lp supply is zero"}}, nil
	}
	balances, err := s.Source.LPBalances(ctx, pool.LPMint, pool.LPTokenPgm)
	if err != nil {
		return nil, fmt.Errorf("list LP holders of %s: %w", pool.LPMint, err)
	}
	if err := checkSize(len(balances), s.MaxHolders); err != nil {
		return nil, err
	}
	holders := make([]domain.Holder, 0, len(balances))
	for _, b := range balances {
		if b.Amount == 0 {
			continue
		}
		lp := new(big.Int).SetUint64(b.Amount)
		holders = append(holders, domain.Holder{
			Wallet:     b.Owner,
			Liquidity:  lp,
			AmountARaw: poolmath.MulDiv(pool.ReserveA, lp, pool.LPSupply),
			AmountBRaw: poolmath.MulDiv(pool.ReserveB, lp, pool.LPSupply),
		})
	}
	return &Result{Holders: holders}, nil
}

// RPCLPSource lists LP holders with one getProgramAccounts call on the token
// program, filtered by mint and sliced to owner+amount (40 of 165 bytes).
type RPCLPSource struct {
	Scanner Scanner
}

// LPBalances implements LPHolderSource.
func (s RPCLPSource) LPBalances(ctx context.Context, lpMint, tokenProgram string) ([]LPBalance, error) {
	if tokenProgram == "" {
		tokenProgram = solana.TokenProgramID
	}
	mint, err := solana.DecodePubkey(lpMint)
	if err != nil {
		return nil, err
	}
	filters := []rpc.Filter{rpc.Memcmp(0, mint[:])}
	if tokenProgram == solana.TokenProgramID {
		// Token-2022 accounts carry extensions, so the size filter only applies to SPL Token.
		filters = append(filters, rpc.DataSize(solana.TokenAccountSize))
	}
	accounts, err := s.Scanner.Scan(ctx, rpc.ProgramAccountsQuery{
		Program:   tokenProgram,
		Filters:   filters,
		DataSlice: &rpc.DataSlice{Offset: solana.TokenOwnerAmountSliceOffset, Length: solana.TokenOwnerAmountSliceLength},
	})
	if err != nil {
		return nil, err
	}
	byOwner := make(map[string]uint64, len(accounts))
	for _, a := range accounts {
		owner, amount, err := solana.ParseOwnerAmountSlice(a.Data)
		if err != nil || amount == 0 {
			continue
		}
		byOwner[owner] += amount
	}
	return sortedBalances(byOwner), nil
}

// FallbackLPSource tries Primary first and uses Secondary when Primary fails,
// times out, or returns nothing (e.g. the indexer has not seen a new LP mint yet).
type FallbackLPSource struct {
	Primary        LPHolderSource
	Secondary      LPHolderSource
	PrimaryTimeout time.Duration // a hung primary must not eat the whole compute budget
}

// LPBalances implements LPHolderSource.
func (s FallbackLPSource) LPBalances(ctx context.Context, lpMint, tokenProgram string) ([]LPBalance, error) {
	timeout := s.PrimaryTimeout
	if timeout <= 0 {
		timeout = defaultPrimaryTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	out, err := s.Primary.LPBalances(pctx, lpMint, tokenProgram)
	cancel()
	if err == nil && len(out) > 0 {
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err() // the caller is gone; do not start the fallback
	}
	if err != nil {
		slog.Warn("primary LP holder source failed; falling back", "lp_mint", lpMint, "error", err)
	}
	return s.Secondary.LPBalances(ctx, lpMint, tokenProgram)
}

func sortedBalances(byOwner map[string]uint64) []LPBalance {
	out := make([]LPBalance, 0, len(byOwner))
	for o, a := range byOwner {
		out = append(out, LPBalance{Owner: o, Amount: a})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Amount != out[j].Amount {
			return out[i].Amount > out[j].Amount
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}
