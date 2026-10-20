package service

import (
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/holders"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
	"birdeye.so/lp-holder/internal/testutil"
)

type noHolders struct{}

func (noHolders) Holders(context.Context, *domain.Pool) (*holders.Result, error) {
	return &holders.Result{}, nil
}

func newTestService(r PoolRPC, p domain.Protocol, s holders.Strategy) *Service {
	return New(Config{}, Deps{
		RPC:        r,
		Registry:   protocol.DefaultRegistry(),
		Strategies: map[domain.Protocol]holders.Strategy{p: s},
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
}

func tokenAcct(data []byte) *rpc.Account {
	return &rpc.Account{Owner: solana.TokenProgramID, Data: data}
}

// meteoraVault builds a synthetic Meteora vault account.
func meteoraVault(t *testing.T, total uint64, tokenVault, lpMint string) *rpc.Account {
	t.Helper()
	data := make([]byte, 200)
	d := solana.Discriminator("Vault")
	copy(data, d[:])
	binary.LittleEndian.PutUint64(data[11:], total)
	tv, _ := solana.DecodePubkey(tokenVault)
	lp, _ := solana.DecodePubkey(lpMint)
	copy(data[19:], tv[:])
	copy(data[115:], lp[:])
	return &rpc.Account{Owner: protocol.MeteoraDAMMV1ProgramID, Data: data}
}

func TestEnrichMeteoraDAMMV1UsesVaultShares(t *testing.T) {
	f := testutil.LoadFixture(t, "meteora_dammv1_pool")
	pool, err := protocol.MeteoraDAMMV1{}.DecodePool(f.Address, f.Data)
	if err != nil {
		t.Fatal(err)
	}
	r := pool.Reserve
	lpA, lpB, innerA, innerB := testutil.Pubkey(50), testutil.Pubkey(51), testutil.Pubkey(52), testutil.Pubkey(53)
	fake := &fakeRPC{accounts: map[string]*rpc.Account{
		f.Address:   {Owner: f.Owner, Data: f.Data},
		pool.MintA:  tokenAcct(testutil.Mint(1, 9)),
		pool.MintB:  tokenAcct(testutil.Mint(1, 6)),
		pool.LPMint: tokenAcct(testutil.Mint(1000, 9)),
		// Shared vaults hold far more than this pool owns.
		r.MeteoraVaultA:   meteoraVault(t, 1_000_000_000_000, innerA, lpA),
		r.MeteoraVaultB:   meteoraVault(t, 5_000_000_000, innerB, lpB),
		r.VaultLPAccountA: tokenAcct(testutil.TokenAccount(t, lpA, f.Address, 10)),
		r.VaultLPAccountB: tokenAcct(testutil.TokenAccount(t, lpB, f.Address, 30)),
		lpA:               tokenAcct(testutil.Mint(1000, 9)),
		lpB:               tokenAcct(testutil.Mint(300, 6)),
	}}
	snap, err := newTestService(fake, domain.ProtocolMeteoraDAMMV1, noHolders{}).Snapshot(context.Background(), f.Address)
	if err != nil {
		t.Fatal(err)
	}
	// reserve = pool vault-LP balance * vault total / vault LP supply
	if snap.Pool.ReserveA.Int64() != 10_000_000_000 || snap.Pool.ReserveB.Int64() != 500_000_000 {
		t.Fatalf("reserves = %s / %s", snap.Pool.ReserveA, snap.Pool.ReserveB)
	}
	if snap.Pool.VaultA != innerA || snap.Pool.VaultB != innerB {
		t.Fatal("response must show the inner SPL vaults")
	}
}

func TestEnrichSpotPriceSources(t *testing.T) {
	cases := []struct {
		fixture  string
		protocol domain.Protocol
		decimals [2]uint8
	}{
		{"meteora_dlmm_pair", domain.ProtocolMeteoraDLMM, [2]uint8{9, 6}},
		{"orca_whirlpool_pool", domain.ProtocolOrcaWhirlpool, [2]uint8{9, 6}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			f := testutil.LoadFixture(t, tc.fixture)
			adapter, _ := protocol.DefaultRegistry().Detect(f.Owner, f.Data)
			pool, _ := adapter.DecodePool(f.Address, f.Data)
			fake := &fakeRPC{accounts: map[string]*rpc.Account{
				f.Address:   {Owner: f.Owner, Data: f.Data},
				pool.MintA:  tokenAcct(testutil.Mint(1, tc.decimals[0])),
				pool.MintB:  tokenAcct(testutil.Mint(1, tc.decimals[1])),
				pool.VaultA: tokenAcct(testutil.TokenAccount(t, pool.MintA, f.Address, 5)),
				// vault B missing: reserve 0 with a warning, not a failure
			}}
			snap, err := newTestService(fake, tc.protocol, noHolders{}).Snapshot(context.Background(), f.Address)
			if err != nil {
				t.Fatal(err)
			}
			if p := snap.Pool.PriceAInB; p < 10 || p > 10_000 {
				t.Fatalf("SOL/USDC spot price %v implausible", p)
			}
			if snap.Pool.ReserveB.Sign() != 0 || len(snap.Warnings) == 0 || !strings.Contains(snap.Warnings[0], "vault B") {
				t.Fatalf("missing vault must warn: %v", snap.Warnings)
			}
		})
	}
}

func TestEnrichFailures(t *testing.T) {
	f := testutil.LoadFixture(t, "orca_whirlpool_pool")
	pool, _ := protocol.OrcaWhirlpool{}.DecodePool(f.Address, f.Data)
	base := func() map[string]*rpc.Account {
		return map[string]*rpc.Account{
			f.Address:  {Owner: f.Owner, Data: f.Data},
			pool.MintA: tokenAcct(testutil.Mint(1, 9)),
			pool.MintB: tokenAcct(testutil.Mint(1, 6)),
		}
	}

	noMint := base()
	delete(noMint, pool.MintB)
	if _, err := newTestService(&fakeRPC{accounts: noMint}, domain.ProtocolOrcaWhirlpool, noHolders{}).
		Snapshot(context.Background(), f.Address); err == nil {
		t.Fatal("missing mint must fail")
	}

	if _, err := newTestService(&fakeRPC{accounts: base()}, domain.ProtocolRaydiumCLMM, noHolders{}).
		Snapshot(context.Background(), f.Address); !errors.Is(err, domain.ErrUnsupportedPool) {
		t.Fatalf("missing strategy must be unsupported, got %v", err)
	}

	failing := strategyFunc(func() (*holders.Result, error) { return nil, errors.New("scan failed") })
	svc := newTestService(&fakeRPC{accounts: base()}, domain.ProtocolOrcaWhirlpool, failing)
	for range 2 {
		if _, err := svc.Snapshot(context.Background(), f.Address); err == nil {
			t.Fatal("strategy error must propagate")
		}
	}
}

func TestPriceFailureOnlyWarns(t *testing.T) {
	f := testutil.LoadFixture(t, "orca_whirlpool_pool")
	pool, _ := protocol.OrcaWhirlpool{}.DecodePool(f.Address, f.Data)
	fake := &fakeRPC{accounts: map[string]*rpc.Account{
		f.Address:  {Owner: f.Owner, Data: f.Data},
		pool.MintA: tokenAcct(testutil.Mint(1, 9)),
		pool.MintB: tokenAcct(testutil.Mint(1, 6)),
	}}
	one := strategyFunc(func() (*holders.Result, error) {
		return &holders.Result{Holders: []domain.Holder{{Wallet: "w", AmountARaw: big.NewInt(1e9), AmountBRaw: new(big.Int), Liquidity: big.NewInt(1)}}}, nil
	})
	svc := New(Config{}, Deps{
		RPC: fake, Registry: protocol.DefaultRegistry(),
		Strategies: map[domain.Protocol]holders.Strategy{domain.ProtocolOrcaWhirlpool: one},
		Prices:     failingPrices{},
	})
	snap, err := svc.Snapshot(context.Background(), f.Address)
	if err != nil {
		t.Fatal(err)
	}
	if snap.HasUSDPrice || snap.Holders[0].ValueUSD != 0 || len(snap.Warnings) == 0 {
		t.Fatalf("price failure must only warn: %+v", snap)
	}
	want := poolmath.PriceFromSqrtX64(pool.SqrtPriceX64, 9, 6)
	if got := snap.Holders[0].ValueInB; got < want*0.999999 || got > want*1.000001 {
		t.Fatalf("1 SOL must be valued at spot price %v, got %v", want, got)
	}
}

func TestSnapshotRespectsCallerCancellation(t *testing.T) {
	env := newCPMMEnv(t)
	env.rpc.delay = 200 * time.Millisecond
	svc := env.service(&clock{t: time.Unix(1_700_000_000, 0)}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := svc.Snapshot(ctx, env.pool.Address); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller timeout must return promptly, got %v", err)
	}
	// The detached computation still completes and fills the cache.
	time.Sleep(300 * time.Millisecond)
	if _, ok := svc.snapshots.get(env.pool.Address); !ok {
		t.Fatal("detached computation must populate the cache")
	}
}

type strategyFunc func() (*holders.Result, error)

func (f strategyFunc) Holders(context.Context, *domain.Pool) (*holders.Result, error) { return f() }

type failingPrices struct{}

func (failingPrices) Prices(context.Context, []string) (map[string]float64, error) {
	return nil, errors.New("price api down")
}
