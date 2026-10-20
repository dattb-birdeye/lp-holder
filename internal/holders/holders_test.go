package holders

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
	"birdeye.so/lp-holder/internal/testutil"
)

// fakeScanner answers scans with a function of the query.
type fakeScanner func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error)

func (f fakeScanner) Scan(_ context.Context, q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
	return f(q)
}

func sliceOf(data []byte, q rpc.ProgramAccountsQuery) []byte {
	if q.DataSlice == nil {
		return data
	}
	return data[q.DataSlice.Offset : q.DataSlice.Offset+q.DataSlice.Length]
}

func bigFrom(t *testing.T, v any) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(v.(string), 10)
	if !ok {
		t.Fatalf("bad big int %v", v)
	}
	return n
}

func TestRPCLPSourceAggregatesOwnersAndFilters(t *testing.T) {
	owner1, owner2 := testutil.Pubkey(1), testutil.Pubkey(2)
	var seen rpc.ProgramAccountsQuery
	src := RPCLPSource{Scanner: fakeScanner(func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
		seen = q
		return []rpc.Account{
			{Pubkey: "a", Data: testutil.OwnerAmountSlice(t, owner1, 10)},
			{Pubkey: "b", Data: testutil.OwnerAmountSlice(t, owner2, 30)},
			{Pubkey: "c", Data: testutil.OwnerAmountSlice(t, owner1, 25)},
			{Pubkey: "d", Data: testutil.OwnerAmountSlice(t, owner2, 0)},
			{Pubkey: "e", Data: []byte{1, 2}}, // malformed slice is skipped
		}, nil
	})}
	out, err := src.LPBalances(context.Background(), testutil.Pubkey(9), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Owner != owner1 || out[0].Amount != 35 || out[1].Amount != 30 {
		t.Fatalf("aggregation wrong: %+v", out)
	}
	if seen.Program != solana.TokenProgramID || len(seen.Filters) != 2 || seen.DataSlice.Length != 40 {
		t.Fatalf("SPL Token scan must filter by mint+size and slice owner+amount: %+v", seen)
	}

	if _, err := src.LPBalances(context.Background(), testutil.Pubkey(9), solana.Token2022ProgramID); err != nil {
		t.Fatal(err)
	}
	if len(seen.Filters) != 1 {
		t.Fatalf("Token-2022 scan must not use a dataSize filter: %+v", seen.Filters)
	}
	if _, err := src.LPBalances(context.Background(), "not-a-key", ""); err == nil {
		t.Fatal("invalid mint must fail")
	}
}

type staticSource struct {
	out []LPBalance
	err error
	n   atomic.Int32
}

func (s *staticSource) LPBalances(context.Context, string, string) ([]LPBalance, error) {
	s.n.Add(1)
	return s.out, s.err
}

func TestFallbackLPSource(t *testing.T) {
	good := &staticSource{out: []LPBalance{{Owner: "x", Amount: 1}}}
	cases := []struct {
		name          string
		primary       *staticSource
		wantSecondary bool
		wantErr       bool
	}{
		{"primary ok", &staticSource{out: []LPBalance{{Owner: "p", Amount: 1}}}, false, false},
		{"primary empty", &staticSource{}, true, false},
		{"primary error", &staticSource{err: errors.New("down")}, true, false},
		// The primary's own deadline expired but the caller is still waiting.
		{"primary timed out", &staticSource{err: context.DeadlineExceeded}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			good.n.Store(0)
			_, err := FallbackLPSource{Primary: tc.primary, Secondary: good}.LPBalances(context.Background(), "m", "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if (good.n.Load() == 1) != tc.wantSecondary {
				t.Fatalf("secondary called %d times", good.n.Load())
			}
		})
	}

	t.Run("caller cancelled", func(t *testing.T) {
		good.n.Store(0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := FallbackLPSource{Primary: &staticSource{err: context.Canceled}, Secondary: good}.LPBalances(ctx, "m", "")
		if !errors.Is(err, context.Canceled) || good.n.Load() != 0 {
			t.Fatalf("a cancelled caller must not trigger the fallback: err=%v calls=%d", err, good.n.Load())
		}
	})
}

func TestLPTokenStrategy(t *testing.T) {
	pool := &domain.Pool{
		Address:  "pool",
		LPMint:   testutil.Pubkey(5),
		LPSupply: big.NewInt(1000),
		ReserveA: big.NewInt(500),
		ReserveB: big.NewInt(2000),
	}
	src := &staticSource{out: []LPBalance{{Owner: "w1", Amount: 250}, {Owner: "w2", Amount: 0}}}
	res, err := LPTokenStrategy{Source: src}.Holders(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Holders) != 1 {
		t.Fatalf("zero balances must be skipped: %+v", res.Holders)
	}
	h := res.Holders[0]
	if h.AmountARaw.Int64() != 125 || h.AmountBRaw.Int64() != 500 || h.Liquidity.Int64() != 250 {
		t.Fatalf("amounts must be reserve*balance/supply: %+v", h)
	}

	res, err = LPTokenStrategy{Source: src}.Holders(context.Background(), &domain.Pool{LPMint: "m", LPSupply: new(big.Int)})
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("zero supply must warn, got %+v %v", res, err)
	}
	if _, err := (LPTokenStrategy{Source: src}).Holders(context.Background(), &domain.Pool{}); err == nil {
		t.Fatal("missing LP mint must fail")
	}
	src.err = errors.New("boom")
	if _, err := (LPTokenStrategy{Source: src}).Holders(context.Background(), pool); err == nil {
		t.Fatal("source error must propagate")
	}
}

type fakeNFTRPC struct {
	dasErr      error
	dasOwners   map[string]string
	dasCalls    atomic.Int32
	largest     map[string]string // mint -> token account
	tokenOwners map[string]string // token account -> owner
}

func (f *fakeNFTRPC) GetAssetBatch(_ context.Context, ids []string) (map[string]string, error) {
	f.dasCalls.Add(1)
	if f.dasErr != nil {
		return nil, f.dasErr
	}
	out := map[string]string{}
	for _, id := range ids {
		if o, ok := f.dasOwners[id]; ok {
			out[id] = o
		}
	}
	return out, nil
}

func (f *fakeNFTRPC) GetTokenLargestAccounts(_ context.Context, mint string) ([]rpc.TokenBalance, error) {
	ta, ok := f.largest[mint]
	if !ok {
		return nil, errors.New("unknown mint")
	}
	return []rpc.TokenBalance{{Address: "closed", Amount: "0"}, {Address: ta, Amount: "1"}}, nil
}

func (f *fakeNFTRPC) GetMultipleAccounts(_ context.Context, keys []string) ([]*rpc.Account, error) {
	out := make([]*rpc.Account, len(keys))
	for i, k := range keys {
		if o, ok := f.tokenOwners[k]; ok {
			out[i] = &rpc.Account{Pubkey: k, Data: tokenAccountData(o)}
		}
	}
	return out, nil
}

func tokenAccountData(owner string) []byte {
	data := make([]byte, solana.TokenAccountSize)
	pk, _ := solana.DecodePubkey(owner)
	copy(data[32:], pk[:])
	data[64] = 1
	return data
}

func TestNFTOwnerResolverDASThenFallback(t *testing.T) {
	m1, m2, m3 := testutil.Pubkey(11), testutil.Pubkey(12), testutil.Pubkey(13)
	w1, w2 := testutil.Pubkey(21), testutil.Pubkey(22)
	f := &fakeNFTRPC{
		dasOwners:   map[string]string{m1: w1},
		largest:     map[string]string{m2: testutil.Pubkey(31)},
		tokenOwners: map[string]string{testutil.Pubkey(31): w2},
	}
	owners, err := NewNFTOwnerResolver(f, true, 2).Resolve(context.Background(), []string{m1, m2, m3})
	if err != nil {
		t.Fatal(err)
	}
	if owners[m1] != w1 || owners[m2] != w2 || owners[m3] != "" {
		t.Fatalf("owners = %v", owners)
	}
}

func TestNFTOwnerResolverDisablesDASWhenUnsupported(t *testing.T) {
	m := testutil.Pubkey(11)
	f := &fakeNFTRPC{
		dasErr:      &rpc.Error{Code: -32601, Message: "Method not found"},
		largest:     map[string]string{m: testutil.Pubkey(31)},
		tokenOwners: map[string]string{testutil.Pubkey(31): testutil.Pubkey(21)},
	}
	r := NewNFTOwnerResolver(f, true, 2)
	for range 2 {
		owners, err := r.Resolve(context.Background(), []string{m})
		if err != nil || owners[m] == "" {
			t.Fatalf("fallback must resolve: %v %v", owners, err)
		}
	}
	if f.dasCalls.Load() != 1 {
		t.Fatalf("DAS must be disabled after method-not-found, called %d times", f.dasCalls.Load())
	}
	if owners, _ := r.Resolve(context.Background(), nil); len(owners) != 0 {
		t.Fatal("empty input must give empty map")
	}
}

type mapOwners map[string]string

func (m mapOwners) Resolve(context.Context, []string) (map[string]string, error) { return m, nil }

func TestTickStrategyWithMainnetFixture(t *testing.T) {
	e := testutil.Expected(t)["raydium_clmm"]
	poolF := testutil.LoadFixture(t, "raydium_clmm_pool")
	posF := testutil.LoadFixture(t, "raydium_clmm_position")
	adapter := protocol.RaydiumCLMM{}
	pool, err := adapter.DecodePool(poolF.Address, poolF.Data)
	if err != nil {
		t.Fatal(err)
	}
	wallet := testutil.Pubkey(40)
	scanner := fakeScanner(func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
		empty := make([]byte, q.DataSlice.Length) // zero-liquidity position, skipped
		return []rpc.Account{{Pubkey: posF.Address, Data: sliceOf(posF.Data, q)}, {Pubkey: "empty", Data: empty}}, nil
	})
	strategy := TickStrategy{Adapter: adapter, Scanner: scanner, Owners: mapOwners{e["nft_mint"].(string): wallet}}
	res, err := strategy.Holders(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Holders) != 1 || res.Holders[0].Wallet != wallet || *res.Holders[0].TickLower != int32(e["tick_lower"].(float64)) {
		t.Fatalf("holders = %+v", res.Holders)
	}
	if diff := new(big.Int).Sub(res.Holders[0].AmountARaw, bigFrom(t, e["amount_a_raw"])); diff.CmpAbs(big.NewInt(1000)) > 0 {
		t.Fatalf("amount A off by %s", diff)
	}

	strategy.MaxPositions = 1
	if _, err := strategy.Holders(context.Background(), pool); !errors.Is(err, domain.ErrPoolTooLarge) {
		t.Fatalf("want ErrPoolTooLarge, got %v", err)
	}
}

func TestDAMMV2StrategyWithMainnetFixture(t *testing.T) {
	e := testutil.Expected(t)["dammv2"]
	poolF := testutil.LoadFixture(t, "meteora_dammv2_pool")
	posF := testutil.LoadFixture(t, "meteora_dammv2_position")
	pool, err := protocol.MeteoraDAMMV2{}.DecodePool(poolF.Address, poolF.Data)
	if err != nil {
		t.Fatal(err)
	}
	scanner := fakeScanner(func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
		return []rpc.Account{{Pubkey: posF.Address, Data: sliceOf(posF.Data, q)}}, nil
	})
	res, err := DAMMV2Strategy{Scanner: scanner, Owners: mapOwners{}}.Holders(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	h := res.Holders[0]
	if h.AmountARaw.Cmp(bigFrom(t, e["amount_a_raw"])) != 0 || h.AmountBRaw.Cmp(bigFrom(t, e["amount_b_raw"])) != 0 {
		t.Fatalf("amounts = %s / %s", h.AmountARaw, h.AmountBRaw)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("unresolved owner must be reported: %v", res.Warnings)
	}
}

func TestDLMMStrategyWithMainnetFixtures(t *testing.T) {
	e := testutil.Expected(t)["dlmm"]
	pos := testutil.LoadFixture(t, "meteora_dlmm_position")
	wide := testutil.LoadFixture(t, "meteora_dlmm_position_extended")
	bins := []rpc.Account{
		{Pubkey: "b34", Data: testutil.LoadFixture(t, "meteora_dlmm_binarray_34").Data},
		{Pubkey: "b33", Data: testutil.LoadFixture(t, "meteora_dlmm_binarray_33").Data},
	}
	scanner := fakeScanner(func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
		if q.DataSlice == nil {
			return bins, nil
		}
		return []rpc.Account{
			{Pubkey: pos.Address, Data: sliceOf(pos.Data, q)},
			{Pubkey: wide.Address, Data: sliceOf(wide.Data, q)},
		}, nil
	})
	var fetched []string
	accounts := accountsFunc(func(keys []string) ([]*rpc.Account, error) {
		fetched = keys
		return []*rpc.Account{{Pubkey: wide.Address, Data: wide.Data}}, nil
	})
	res, err := DLMMStrategy{Scanner: scanner, Accounts: accounts}.Holders(context.Background(), &domain.Pool{Address: "pair"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 || fetched[0] != wide.Address {
		t.Fatalf("only wide positions must be fetched in full, got %v", fetched)
	}
	byAddr := map[string]domain.Holder{}
	for _, h := range res.Holders {
		byAddr[h.PositionAddress] = h
	}
	h := byAddr[pos.Address]
	if h.Wallet != e["owner"] || h.AmountARaw.Cmp(bigFrom(t, e["amount_x_raw"])) != 0 || h.AmountBRaw.Cmp(bigFrom(t, e["amount_y_raw"])) != 0 {
		t.Fatalf("dlmm holder = %+v", h)
	}
	ext := testutil.Expected(t)["dlmm_extended"]
	if w, ok := byAddr[wide.Address]; !ok || *w.BinIDUpper != int32(ext["upper"].(float64)) {
		t.Fatalf("extended position missing: %+v", w)
	}
}

type accountsFunc func([]string) ([]*rpc.Account, error)

func (f accountsFunc) GetMultipleAccounts(_ context.Context, keys []string) ([]*rpc.Account, error) {
	return f(keys)
}
