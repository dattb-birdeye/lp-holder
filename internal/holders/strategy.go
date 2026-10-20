// Package holders lists the liquidity holders of a pool. There is one
// strategy per ownership model:
//
//   - LPToken: fungible LP mint (Raydium AMM v4/CPMM, Lifinity v2, Meteora DAMM v1)
//   - Tick:    NFT positions with a tick range (Raydium CLMM, Orca Whirlpool)
//   - DAMMV2:  NFT positions sharing the pool price range (Meteora DAMM v2)
//   - DLMM:    bin positions valued through bin arrays (Meteora DLMM)
//
// Strategies return raw amounts; percentages and USD values are computed by
// the service layer so the rules live in one place.
package holders

import (
	"context"
	"fmt"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// Strategy lists holders of a decoded and enriched pool.
type Strategy interface {
	Holders(ctx context.Context, pool *domain.Pool) (*Result, error)
}

// Result is the raw output of a strategy.
type Result struct {
	Holders  []domain.Holder
	Warnings []string
	// Incomplete is set when some holders could not be fully resolved
	// (unresolved NFT owners, positions changed mid-scan). Such results are
	// cached for a shorter time.
	Incomplete bool
}

// Scanner runs getProgramAccounts-style scans.
type Scanner interface {
	Scan(ctx context.Context, q rpc.ProgramAccountsQuery) ([]rpc.Account, error)
}

// RPCScanner scans with the standard method, or with Helius'
// paginated getProgramAccountsV2 when UseV2 is set.
type RPCScanner struct {
	Client *rpc.Client
	UseV2  bool
}

// Scan implements Scanner.
func (s RPCScanner) Scan(ctx context.Context, q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
	if s.UseV2 {
		return s.Client.GetProgramAccountsV2(ctx, q)
	}
	return s.Client.GetProgramAccounts(ctx, q)
}

func checkSize(n, maxPositions int) error {
	if maxPositions > 0 && n > maxPositions {
		return fmt.Errorf("%w: %d positions (limit %d)", domain.ErrPoolTooLarge, n, maxPositions)
	}
	return nil
}
