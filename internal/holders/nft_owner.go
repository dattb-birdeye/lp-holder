package holders

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

const (
	dasBatchSize               = 1000 // Helius getAssetBatch limit
	defaultFallbackConcurrency = 8
)

// OwnerResolver maps position NFT mints to the wallets holding them.
type OwnerResolver interface {
	Resolve(ctx context.Context, mints []string) (map[string]string, error)
}

// NFTRPC is the subset of the RPC client used to resolve NFT owners.
type NFTRPC interface {
	GetAssetBatch(ctx context.Context, ids []string) (map[string]string, error)
	GetTokenLargestAccounts(ctx context.Context, mint string) ([]rpc.TokenBalance, error)
	GetMultipleAccounts(ctx context.Context, addresses []string) ([]*rpc.Account, error)
}

// NFTOwnerResolver resolves owners with DAS getAssetBatch (1 call per 1000
// mints) and falls back to getTokenLargestAccounts + getMultipleAccounts,
// which works on any RPC and for both SPL Token and Token-2022 NFTs.
//
// The fallback costs one call per mint, so it is only used when DAS is
// disabled or unsupported by the endpoint. A transient DAS failure fails the
// computation instead of silently fanning out to thousands of calls.
type NFTOwnerResolver struct {
	rpc                 NFTRPC
	fallbackConcurrency int
	dasDisabled         atomic.Bool
}

// NewNFTOwnerResolver creates a resolver. useDAS=false skips DAS entirely.
func NewNFTOwnerResolver(client NFTRPC, useDAS bool, fallbackConcurrency int) *NFTOwnerResolver {
	if fallbackConcurrency <= 0 {
		fallbackConcurrency = defaultFallbackConcurrency
	}
	r := &NFTOwnerResolver{rpc: client, fallbackConcurrency: fallbackConcurrency}
	r.dasDisabled.Store(!useDAS)
	return r
}

// Resolve implements OwnerResolver. Mints that cannot be resolved are absent
// from the result; callers decide how to report them.
func (r *NFTOwnerResolver) Resolve(ctx context.Context, mints []string) (map[string]string, error) {
	owners := make(map[string]string, len(mints))
	if len(mints) == 0 {
		return owners, nil
	}
	if !r.dasDisabled.Load() {
		if err := r.resolveDAS(ctx, mints, owners); err != nil {
			return nil, err
		}
	}
	missing := make([]string, 0)
	for _, m := range mints {
		if _, ok := owners[m]; !ok {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return owners, nil
	}
	fallback, err := r.resolveLargestAccounts(ctx, missing)
	if err != nil {
		return nil, err
	}
	for m, o := range fallback {
		owners[m] = o
	}
	return owners, nil
}

func (r *NFTOwnerResolver) resolveDAS(ctx context.Context, mints []string, dst map[string]string) error {
	for start := 0; start < len(mints); start += dasBatchSize {
		batch := mints[start:min(start+dasBatchSize, len(mints))]
		owners, err := r.rpc.GetAssetBatch(ctx, batch)
		if rpc.IsMethodNotFound(err) {
			// Not a DAS endpoint: use the fallback for the lifetime of the process.
			r.dasDisabled.Store(true)
			slog.Warn("DAS getAssetBatch unsupported by RPC endpoint; using token-account fallback")
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve nft owners via DAS: %w", err)
		}
		for m, o := range owners {
			dst[m] = o
		}
	}
	return nil
}

func (r *NFTOwnerResolver) resolveLargestAccounts(ctx context.Context, mints []string) (map[string]string, error) {
	// Step 1: mint -> token account currently holding the NFT.
	tokenAccounts := make([]string, len(mints))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.fallbackConcurrency)
	for i, mint := range mints {
		g.Go(func() error {
			accounts, err := r.rpc.GetTokenLargestAccounts(gctx, mint)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				slog.Debug("getTokenLargestAccounts failed", "mint", mint, "error", err)
				return nil // one bad mint must not fail the whole pool; reported as unresolved
			}
			for _, a := range accounts {
				if a.Amount != "" && a.Amount != "0" {
					tokenAccounts[i] = a.Address
					break
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Step 2: token account -> owner, batched.
	keys := make([]string, 0, len(mints))
	keyIdx := make([]int, 0, len(mints))
	for i, ta := range tokenAccounts {
		if ta != "" {
			keys = append(keys, ta)
			keyIdx = append(keyIdx, i)
		}
	}
	accounts, err := r.rpc.GetMultipleAccounts(ctx, keys)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(keys))
	for j, acct := range accounts {
		if acct == nil {
			continue
		}
		if ta, err := solana.ParseTokenAccount(acct.Data); err == nil {
			owners[mints[keyIdx[j]]] = ta.Owner
		}
	}
	return owners, nil
}
