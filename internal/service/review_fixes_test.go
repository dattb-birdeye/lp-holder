package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/holders"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana/rpc"
	"birdeye.so/lp-holder/internal/testutil"
)

func whirlpoolRPC(t *testing.T) (*fakeRPC, string) {
	t.Helper()
	f := testutil.LoadFixture(t, "orca_whirlpool_pool")
	pool, _ := protocol.OrcaWhirlpool{}.DecodePool(f.Address, f.Data)
	return &fakeRPC{accounts: map[string]*rpc.Account{
		f.Address:  {Owner: f.Owner, Data: f.Data},
		pool.MintA: tokenAcct(testutil.Mint(1, 9)),
		pool.MintB: tokenAcct(testutil.Mint(1, 6)),
	}}, f.Address
}

func serviceWith(r PoolRPC, s holders.Strategy, cfg Config, now func() time.Time) *Service {
	return New(cfg, Deps{
		RPC: r, Registry: protocol.DefaultRegistry(),
		Strategies: map[domain.Protocol]holders.Strategy{domain.ProtocolOrcaWhirlpool: s},
		Now:        now,
	})
}

func TestPanicInComputeIsRecovered(t *testing.T) {
	r, pool := whirlpoolRPC(t)
	boom := strategyFunc(func() (*holders.Result, error) { panic("decoder bug") })
	_, err := serviceWith(r, boom, Config{}, nil).Snapshot(context.Background(), pool)
	if err == nil {
		t.Fatal("a panic must surface as an error, not crash the process")
	}
}

func TestBusyWhenComputeSlotsAreFull(t *testing.T) {
	r, pool := whirlpoolRPC(t)
	r.delay = 150 * time.Millisecond
	svc := serviceWith(r, noHolders{}, Config{MaxConcurrentComputes: 1}, nil)

	done := make(chan error, 1)
	go func() { _, err := svc.Snapshot(context.Background(), pool); done <- err }()
	time.Sleep(30 * time.Millisecond)

	other := testutil.Pubkey(90)
	if _, err := svc.Snapshot(context.Background(), other); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("second cold pool must get ErrBusy, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// ErrBusy is transient: it must not be negatively cached.
	if _, err := svc.Snapshot(context.Background(), other); !errors.Is(err, domain.ErrPoolNotFound) {
		t.Fatalf("after the slot frees up the pool is computed normally, got %v", err)
	}
}

func TestIncompleteSnapshotsUseShortTTL(t *testing.T) {
	r, pool := whirlpoolRPC(t)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	partial := strategyFunc(func() (*holders.Result, error) { return &holders.Result{Incomplete: true}, nil })
	svc := serviceWith(r, partial, Config{CacheTTL: time.Hour}, c.now)

	if _, err := svc.Snapshot(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	c.add(incompleteTTL + time.Second)
	if _, err := svc.Snapshot(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if n := r.infoCalls.Load(); n != 2 {
		t.Fatalf("incomplete snapshot must expire after %s, got %d computations", incompleteTTL, n)
	}
}

func TestExpiredBudgetIsNotCached(t *testing.T) {
	r, pool := whirlpoolRPC(t)
	slow := strategyFunc(func() (*holders.Result, error) {
		time.Sleep(60 * time.Millisecond) // outlives the compute budget, but "succeeds"
		return &holders.Result{}, nil
	})
	svc := serviceWith(r, slow, Config{ComputeTimeout: 20 * time.Millisecond}, nil)
	if _, err := svc.Snapshot(context.Background(), pool); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if _, ok := svc.snapshots.get(pool); ok {
		t.Fatal("a result produced after the deadline must not be cached")
	}
}

func TestWalletViewIsBuiltOnce(t *testing.T) {
	env := newCPMMEnv(t)
	svc := env.service(&clock{t: time.Unix(1_700_000_000, 0)}, nil)
	p1, err := svc.Holders(context.Background(), Query{Pool: env.pool.Address, GroupBy: GroupByWallet})
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := svc.Holders(context.Background(), Query{Pool: env.pool.Address, GroupBy: GroupByWallet})
	if &p1.Holders[0] != &p2.Holders[0] {
		t.Fatal("grouped rows must be reused across requests")
	}
	if cap(p1.Holders) != len(p1.Holders) {
		t.Fatal("page capacity must be capped to protect the shared snapshot")
	}
}
