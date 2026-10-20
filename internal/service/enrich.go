package service

import (
	"context"
	"fmt"
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// enrich fills decimals, reserves, LP supply and spot price.
//
// One getMultipleAccounts call covers mints, vaults and the LP mint (two calls
// for Meteora DAMM v1, which needs the shared vaults' LP mints). This replaces
// the old per-field lookups (token info service, total supply service,
// 2x getTokenAccountBalance, default decimals 9/6).
func (s *Service) enrich(ctx context.Context, pool *domain.Pool) ([]string, error) {
	keys := []string{pool.MintA, pool.MintB}
	meteoraVaults := pool.Reserve.MeteoraVaultA != ""
	if meteoraVaults {
		r := pool.Reserve
		keys = append(keys, r.MeteoraVaultA, r.MeteoraVaultB, r.VaultLPAccountA, r.VaultLPAccountB)
	} else {
		keys = append(keys, pool.VaultA, pool.VaultB)
	}
	if pool.LPMint != "" {
		keys = append(keys, pool.LPMint)
	}

	accts, err := s.rpc.GetMultipleAccounts(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("fetch pool accounts: %w", err)
	}

	if err := setDecimals(pool, accts[0], accts[1]); err != nil {
		return nil, err
	}

	var warnings []string
	if meteoraVaults {
		w, err := s.meteoraVaultReserves(ctx, pool, accts[2], accts[3], accts[4], accts[5])
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	} else {
		pool.ReserveA = vaultReserve(accts[2], pool.Reserve.ExcludeA, &warnings, "A")
		pool.ReserveB = vaultReserve(accts[3], pool.Reserve.ExcludeB, &warnings, "B")
	}

	if pool.LPMint != "" {
		lpAcct := accts[len(accts)-1]
		if lpAcct == nil {
			return nil, fmt.Errorf("lp mint %s not found", pool.LPMint)
		}
		pool.LPTokenPgm = lpAcct.Owner
		mint, err := solana.ParseMint(lpAcct.Data)
		if err != nil {
			return nil, fmt.Errorf("parse lp mint: %w", err)
		}
		pool.LPMintSupply = new(big.Int).SetUint64(mint.Supply)
		if pool.LPSupply == nil {
			pool.LPSupply = pool.LPMintSupply
		}
		if w := burnedLPWarning(pool); w != "" {
			warnings = append(warnings, w)
		}
	}

	pool.PriceAInB = spotPrice(pool)
	return warnings, nil
}

// burnedLPWarning explains why LP shares sum below 100%: burned LP tokens
// still count in the pool's LP supply but are held by nobody.
func burnedLPWarning(pool *domain.Pool) string {
	if pool.LPSupply.Sign() == 0 || pool.LPMintSupply.Cmp(pool.LPSupply) >= 0 {
		return ""
	}
	burned := new(big.Int).Sub(pool.LPSupply, pool.LPMintSupply)
	return fmt.Sprintf("%.4f%% of the pool's LP supply is burned; holder percentages sum to at most %.4f%%",
		ratioPercent(burned, pool.LPSupply), ratioPercent(pool.LPMintSupply, pool.LPSupply))
}

func setDecimals(pool *domain.Pool, mintA, mintB *rpc.Account) error {
	for i, acct := range []*rpc.Account{mintA, mintB} {
		if acct == nil || !solana.IsTokenProgram(acct.Owner) {
			return fmt.Errorf("token mint %d of pool %s not found", i, pool.Address)
		}
		m, err := solana.ParseMint(acct.Data)
		if err != nil {
			return fmt.Errorf("parse mint: %w", err)
		}
		if i == 0 {
			pool.DecimalsA = m.Decimals
		} else {
			pool.DecimalsB = m.Decimals
		}
	}
	return nil
}

func vaultReserve(acct *rpc.Account, exclude uint64, warnings *[]string, side string) *big.Int {
	if acct == nil {
		*warnings = append(*warnings, "vault "+side+" not found; reserve set to 0")
		return new(big.Int)
	}
	ta, err := solana.ParseTokenAccount(acct.Data)
	if err != nil {
		*warnings = append(*warnings, "vault "+side+" is not a token account; reserve set to 0")
		return new(big.Int)
	}
	if exclude >= ta.Amount {
		return new(big.Int)
	}
	return new(big.Int).SetUint64(ta.Amount - exclude)
}

// meteoraVaultReserves computes DAMM v1 reserves:
// reserve = pool's vault-LP balance * vault.total_amount / vault LP supply.
func (s *Service) meteoraVaultReserves(ctx context.Context, pool *domain.Pool, vaultA, vaultB, lpAcctA, lpAcctB *rpc.Account) ([]string, error) {
	if vaultA == nil || vaultB == nil || lpAcctA == nil || lpAcctB == nil {
		return nil, fmt.Errorf("meteora vault accounts of pool %s not found", pool.Address)
	}
	va, err := protocol.DecodeMeteoraVault(vaultA.Data)
	if err != nil {
		return nil, err
	}
	vb, err := protocol.DecodeMeteoraVault(vaultB.Data)
	if err != nil {
		return nil, err
	}
	balA, err := solana.ParseTokenAccount(lpAcctA.Data)
	if err != nil {
		return nil, err
	}
	balB, err := solana.ParseTokenAccount(lpAcctB.Data)
	if err != nil {
		return nil, err
	}
	mints, err := s.rpc.GetMultipleAccounts(ctx, []string{va.LPMint, vb.LPMint})
	if err != nil {
		return nil, fmt.Errorf("fetch meteora vault lp mints: %w", err)
	}
	supply := make([]*big.Int, 2)
	for i, acct := range mints {
		if acct == nil {
			return nil, fmt.Errorf("meteora vault lp mint not found")
		}
		m, err := solana.ParseMint(acct.Data)
		if err != nil {
			return nil, err
		}
		supply[i] = new(big.Int).SetUint64(m.Supply)
	}
	pool.ReserveA = poolmath.MulDiv(new(big.Int).SetUint64(balA.Amount), new(big.Int).SetUint64(va.TotalAmount), supply[0])
	pool.ReserveB = poolmath.MulDiv(new(big.Int).SetUint64(balB.Amount), new(big.Int).SetUint64(vb.TotalAmount), supply[1])
	// Show the inner SPL vaults; the Meteora vault accounts are not token accounts.
	pool.VaultA, pool.VaultB = va.TokenVault, vb.TokenVault
	return []string{"meteora damm v1 reserves ignore the vault's locked profit (small overestimate)"}, nil
}

func spotPrice(pool *domain.Pool) float64 {
	switch {
	case pool.ActiveBinID != nil && pool.BinStep != nil:
		return poolmath.DLMMPrice(*pool.ActiveBinID, *pool.BinStep, pool.DecimalsA, pool.DecimalsB)
	case pool.SqrtPriceX64 != nil:
		return poolmath.PriceFromSqrtX64(pool.SqrtPriceX64, pool.DecimalsA, pool.DecimalsB)
	default:
		a := poolmath.ToUI(pool.ReserveA, pool.DecimalsA)
		if a == 0 {
			return 0
		}
		return poolmath.ToUI(pool.ReserveB, pool.DecimalsB) / a
	}
}
