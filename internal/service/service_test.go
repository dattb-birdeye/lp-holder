package service

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/holders"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
	"birdeye.so/lp-holder/internal/testutil"
)

type fakeRPC struct {
	accounts  map[string]*rpc.Account
	infoCalls atomic.Int32
	delay     time.Duration
}

func (f *fakeRPC) GetAccountInfo(_ context.Context, address string) (*rpc.Account, error) {
	f.infoCalls.Add(1)
	time.Sleep(f.delay)
	if a, ok := f.accounts[address]; ok {
		return a, nil
	}
	return nil, rpc.ErrAccountNotFound
}

func (f *fakeRPC) GetMultipleAccounts(_ context.Context, keys []string) ([]*rpc.Account, error) {
	out := make([]*rpc.Account, len(keys))
	for i, k := range keys {
		out[i] = f.accounts[k]
	}
	return out, nil
}

type fakeLPSource []holders.LPBalance

func (f fakeLPSource) LPBalances(context.Context, string, string) ([]holders.LPBalance, error) {
	return f, nil
}

type fakePrices map[string]float64

func (f fakePrices) Prices(context.Context, []string) (map[string]float64, error) { return f, nil }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type cpmmEnv struct {
	rpc      *fakeRPC
	pool     *domain.Pool
	wallet1  string
	wallet2  string
	reserveA int64
	reserveB int64
}

// newCPMMEnv serves the real CPMM fixture with synthetic mints and vaults.
// The LP mint supply is half of the pool's lp_supply (half the LP burned).
func newCPMMEnv(t *testing.T) cpmmEnv {
	t.Helper()
	f := testutil.LoadFixture(t, "raydium_cpmm_pool")
	pool, err := protocol.RaydiumCPMM{}.DecodePool(f.Address, f.Data)
	if err != nil {
		t.Fatal(err)
	}
	env := cpmmEnv{pool: pool, wallet1: testutil.Pubkey(1), wallet2: testutil.Pubkey(2),
		reserveA: 1_000_000_000_000, reserveB: 2_000_000}
	tok := func(data []byte) *rpc.Account { return &rpc.Account{Owner: solana.TokenProgramID, Data: data} }
	env.rpc = &fakeRPC{accounts: map[string]*rpc.Account{
		f.Address:   {Owner: f.Owner, Data: f.Data},
		pool.MintA:  tok(testutil.Mint(1, 9)),
		pool.MintB:  tok(testutil.Mint(1, 6)),
		pool.VaultA: tok(testutil.TokenAccount(t, pool.MintA, testutil.Pubkey(3), uint64(env.reserveA)+pool.Reserve.ExcludeA)),
		pool.VaultB: tok(testutil.TokenAccount(t, pool.MintB, testutil.Pubkey(3), uint64(env.reserveB)+pool.Reserve.ExcludeB)),
		pool.LPMint: tok(testutil.Mint(pool.LPSupply.Uint64()/2, 9)),
	}}
	return env
}

func (e cpmmEnv) service(c *clock, prices PriceProvider) *Service {
	supply := e.pool.LPSupply.Uint64()
	src := fakeLPSource{{Owner: e.wallet2, Amount: supply / 8}, {Owner: e.wallet1, Amount: supply / 4}}
	return New(Config{CacheTTL: time.Minute, NegativeCacheTTL: time.Minute}, Deps{
		RPC:        e.rpc,
		Registry:   protocol.DefaultRegistry(),
		Strategies: map[domain.Protocol]holders.Strategy{domain.ProtocolRaydiumCPMM: holders.LPTokenStrategy{Source: src}},
		Prices:     prices,
		Now:        c.now,
	})
}

func approx(t *testing.T, field string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}

func TestHoldersValuesLPTokenPool(t *testing.T) {
	env := newCPMMEnv(t)
	svc := env.service(&clock{t: time.Unix(1_700_000_000, 0)}, nil)
	page, err := svc.Holders(context.Background(), Query{Pool: env.pool.Address})
	if err != nil {
		t.Fatal(err)
	}
	snap := page.Snapshot
	if page.Total != 2 || page.Holders[0].Wallet != env.wallet1 {
		t.Fatalf("holders must be sorted by share: %+v", page.Holders)
	}
	if snap.Pool.DecimalsA != 9 || snap.Pool.DecimalsB != 6 {
		t.Fatalf("decimals from mints: %d/%d", snap.Pool.DecimalsA, snap.Pool.DecimalsB)
	}
	if snap.Pool.ReserveA.Int64() != env.reserveA || snap.Pool.ReserveB.Int64() != env.reserveB {
		t.Fatalf("fees must be excluded from reserves: %s/%s", snap.Pool.ReserveA, snap.Pool.ReserveB)
	}
	h := page.Holders[0]
	// Integer math as on-chain: reserve * balance / supply, floored.
	supply := env.pool.LPSupply.Uint64()
	balance := supply / 4
	approx(t, "percentage", h.Percentage, float64(balance)/float64(supply)*100)
	wantA := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(env.reserveA), new(big.Int).SetUint64(balance)), env.pool.LPSupply)
	wantB := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(env.reserveB), new(big.Int).SetUint64(balance)), env.pool.LPSupply)
	if h.AmountARaw.Cmp(wantA) != 0 || h.AmountBRaw.Cmp(wantB) != 0 {
		t.Fatalf("raw amounts = %s/%s, want %s/%s", h.AmountARaw, h.AmountBRaw, wantA, wantB)
	}
	approx(t, "amountB ui", h.AmountB, float64(wantB.Int64())/1e6)
	approx(t, "price", snap.Pool.PriceAInB, (float64(env.reserveB)/1e6)/(float64(env.reserveA)/1e9))
	if len(snap.Warnings) != 1 || !strings.Contains(snap.Warnings[0], "50.0000% of the pool's LP supply is burned") {
		t.Fatalf("burned LP must be reported: %v", snap.Warnings)
	}

	page, err = svc.Holders(context.Background(), Query{Pool: env.pool.Address, Offset: 1, Limit: 1})
	if err != nil || len(page.Holders) != 1 || page.Holders[0].Wallet != env.wallet2 {
		t.Fatalf("pagination: %+v %v", page, err)
	}
	page, _ = svc.Holders(context.Background(), Query{Pool: env.pool.Address, Offset: 99})
	if len(page.Holders) != 0 || page.Total != 2 {
		t.Fatalf("offset past end must give an empty page: %+v", page)
	}
}

func TestSnapshotIsSharedAndCached(t *testing.T) {
	env := newCPMMEnv(t)
	env.rpc.delay = 30 * time.Millisecond
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	svc := env.service(c, nil)

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Holders(context.Background(), Query{Pool: env.pool.Address}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := env.rpc.infoCalls.Load(); n != 1 {
		t.Fatalf("concurrent requests must share one computation, got %d", n)
	}
	_, _ = svc.Holders(context.Background(), Query{Pool: env.pool.Address})
	if n := env.rpc.infoCalls.Load(); n != 1 {
		t.Fatalf("cached snapshot must be reused, got %d calls", n)
	}
	c.add(2 * time.Minute)
	_, _ = svc.Holders(context.Background(), Query{Pool: env.pool.Address})
	if n := env.rpc.infoCalls.Load(); n != 2 {
		t.Fatalf("expired snapshot must be recomputed, got %d calls", n)
	}
}

func TestErrorsAndNegativeCache(t *testing.T) {
	env := newCPMMEnv(t)
	other := testutil.Pubkey(77)
	env.rpc.accounts[other] = &rpc.Account{Owner: testutil.Pubkey(78), Data: []byte{1, 2, 3}}
	svc := env.service(&clock{t: time.Unix(1_700_000_000, 0)}, nil)
	ctx := context.Background()

	if _, err := svc.Holders(ctx, Query{Pool: "not base58!"}); !errors.Is(err, domain.ErrInvalidAddress) {
		t.Fatalf("want ErrInvalidAddress, got %v", err)
	}
	missing := testutil.Pubkey(99)
	for range 2 {
		if _, err := svc.Holders(ctx, Query{Pool: missing}); !errors.Is(err, domain.ErrPoolNotFound) {
			t.Fatalf("want ErrPoolNotFound, got %v", err)
		}
	}
	if n := env.rpc.infoCalls.Load(); n != 1 {
		t.Fatalf("not-found must be negatively cached, got %d calls", n)
	}
	if _, err := svc.Holders(ctx, Query{Pool: other}); !errors.Is(err, domain.ErrUnsupportedPool) {
		t.Fatalf("want ErrUnsupportedPool, got %v", err)
	}
}

func TestUSDValueFromEitherTokenPrice(t *testing.T) {
	env := newCPMMEnv(t)
	c := &clock{t: time.Unix(1_700_000_000, 0)}

	snapB, err := env.service(c, fakePrices{env.pool.MintB: 2}).Snapshot(context.Background(), env.pool.Address)
	if err != nil {
		t.Fatal(err)
	}
	h := snapB.Holders[0]
	approx(t, "usd via B", h.ValueUSD, h.ValueInB*2)
	if !snapB.HasUSDPrice || snapB.TotalUSD <= 0 {
		t.Fatal("total usd must be set")
	}

	snapA, err := env.service(c, fakePrices{env.pool.MintA: 10}).Snapshot(context.Background(), env.pool.Address)
	if err != nil {
		t.Fatal(err)
	}
	h = snapA.Holders[0]
	approx(t, "usd via A", h.ValueUSD, h.ValueInB/snapA.Pool.PriceAInB*10)
}

func TestValuePositionPoolsByValueShare(t *testing.T) {
	pool := &domain.Pool{Kind: domain.KindPosition, DecimalsA: 0, DecimalsB: 0, PriceAInB: 2}
	hs := []domain.Holder{
		{Wallet: "a", AmountARaw: big.NewInt(10), AmountBRaw: big.NewInt(0), Liquidity: big.NewInt(1)},  // value 20
		{Wallet: "b", AmountARaw: big.NewInt(0), AmountBRaw: big.NewInt(60), Liquidity: big.NewInt(99)}, // value 60
	}
	total := value(pool, hs, 0.5)
	if hs[0].Wallet != "b" {
		t.Fatal("sorted by value share, not by raw liquidity")
	}
	approx(t, "share b", hs[0].Percentage, 75)
	approx(t, "usd b", hs[0].ValueUSD, 30)
	approx(t, "total usd", total, 40)

	// No value known (e.g. empty reserves): fall back to liquidity share.
	empty := []domain.Holder{
		{Wallet: "a", Liquidity: big.NewInt(1)},
		{Wallet: "b", Liquidity: big.NewInt(3)},
	}
	value(&domain.Pool{Kind: domain.KindPosition}, empty, 0)
	approx(t, "liquidity share", empty[0].Percentage, 75)
}

func TestGroupByWallet(t *testing.T) {
	in := []domain.Holder{
		{Wallet: "w1", PositionAddress: "p1", Percentage: 10, AmountA: 1, Liquidity: big.NewInt(1)},
		{Wallet: "w2", PositionAddress: "p2", Percentage: 15, AmountA: 2, Liquidity: big.NewInt(2)},
		{Wallet: "w1", PositionAddress: "p3", Percentage: 20, AmountA: 3, Liquidity: big.NewInt(3)},
		{Wallet: "", PositionAddress: "p4", Percentage: 1},
		{Wallet: "", PositionAddress: "p5", Percentage: 1},
	}
	out := groupByWallet(in)
	if len(out) != 4 || out[0].Wallet != "w1" || out[0].PositionCount != 2 || out[0].Percentage != 30 || out[0].AmountA != 4 {
		t.Fatalf("grouped = %+v", out)
	}
	if out[0].Liquidity.Int64() != 4 {
		t.Fatalf("liquidity must be summed: %s", out[0].Liquidity)
	}
}

func TestTTLCacheEvictionAndWeight(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	cache := newTTLCache[int](2, 100, c.now)
	cache.set("a", 1, time.Minute, 1)
	c.add(time.Second)
	cache.set("b", 2, time.Minute, 1)
	cache.set("c", 3, time.Minute, 1) // entry bound: evicts oldest ("a")
	if _, ok := cache.get("a"); ok {
		t.Fatal("oldest entry must be evicted")
	}
	if v, ok := cache.get("c"); !ok || v != 3 {
		t.Fatal("newest entry must be kept")
	}
	cache.set("big", 4, time.Minute, 99) // weight bound: needs room for 99
	if _, ok := cache.get("big"); !ok || cache.weight > 100 {
		t.Fatalf("weight bound not enforced: weight=%d", cache.weight)
	}
	cache.set("huge", 5, time.Minute, 101) // larger than the whole budget: not cached
	if _, ok := cache.get("huge"); ok {
		t.Fatal("over-budget value must not be cached")
	}
	c.add(2 * time.Minute)
	if _, ok := cache.get("big"); ok || len(cache.items) != 1 {
		t.Fatalf("expired entries must be dropped on read, items=%d", len(cache.items))
	}
	cache.set("x", 1, 0, 1)
	if _, ok := cache.get("x"); ok {
		t.Fatal("ttl 0 disables caching")
	}
}

func TestQueryNormalize(t *testing.T) {
	q := Query{Limit: 10_000, Offset: -5, GroupBy: "nonsense"}.Normalize()
	if q.Limit != maxPageLimit || q.Offset != 0 || q.GroupBy != GroupByPosition {
		t.Fatalf("normalize = %+v", q)
	}
	if q := (Query{}).Normalize(); q.Limit != defaultPageLimit {
		t.Fatalf("default limit = %d", q.Limit)
	}
}
