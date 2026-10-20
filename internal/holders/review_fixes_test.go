package holders

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana/rpc"
	"birdeye.so/lp-holder/internal/testutil"
)

func TestTransientDASErrorFailsInsteadOfFanningOut(t *testing.T) {
	f := &fakeNFTRPC{dasErr: errors.New("502 bad gateway"), largest: map[string]string{}}
	if _, err := NewNFTOwnerResolver(f, true, 2).Resolve(context.Background(), []string{testutil.Pubkey(1)}); err == nil {
		t.Fatal("a transient DAS failure must fail the computation")
	}
}

func TestFallbackResolverPropagatesCancellation(t *testing.T) {
	f := &fakeNFTRPC{largest: map[string]string{}} // every lookup fails
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewNFTOwnerResolver(f, false, 2).Resolve(ctx, []string{testutil.Pubkey(1)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

type blockingSource struct{}

func (blockingSource) LPBalances(ctx context.Context, _, _ string) ([]LPBalance, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFallbackLPSourceTimesOutHungPrimary(t *testing.T) {
	good := &staticSource{out: []LPBalance{{Owner: "x", Amount: 1}}}
	src := FallbackLPSource{Primary: blockingSource{}, Secondary: good, PrimaryTimeout: 20 * time.Millisecond}
	out, err := src.LPBalances(context.Background(), "m", "")
	if err != nil || len(out) != 1 {
		t.Fatalf("hung primary must fall back to secondary: %v %v", out, err)
	}
}

func TestLPTokenStrategyRespectsMaxHolders(t *testing.T) {
	src := &staticSource{out: []LPBalance{{Owner: "a", Amount: 1}, {Owner: "b", Amount: 1}}}
	pool := &domain.Pool{LPMint: "m", LPSupply: testutil.BigOne(), ReserveA: testutil.BigOne(), ReserveB: testutil.BigOne()}
	if _, err := (LPTokenStrategy{Source: src, MaxHolders: 1}).Holders(context.Background(), pool); !errors.Is(err, domain.ErrPoolTooLarge) {
		t.Fatalf("want ErrPoolTooLarge, got %v", err)
	}
}

func rangeSlice(lower, upper int32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, uint32(lower))
	binary.LittleEndian.PutUint32(b[4:], uint32(upper))
	return b
}

func TestDLMMSkipsInvalidRangesAndVanishedPositions(t *testing.T) {
	pos := testutil.LoadFixture(t, "meteora_dlmm_position")
	wide := testutil.LoadFixture(t, "meteora_dlmm_position_extended")
	scanner := fakeScanner(func(q rpc.ProgramAccountsQuery) ([]rpc.Account, error) {
		if q.DataSlice == nil {
			return nil, nil // no bin arrays: amounts are 0, shares still counted
		}
		if q.DataSlice.Length == 8 {
			return []rpc.Account{
				{Pubkey: "huge", Data: rangeSlice(-2_000_000_000, 2_000_000_000)}, // would overflow int32 width
				{Pubkey: "inverted", Data: rangeSlice(10, 5)},
				{Pubkey: wide.Address, Data: sliceOf(wide.Data, q)},
				{Pubkey: pos.Address, Data: sliceOf(pos.Data, q)},
			}, nil
		}
		shares := sliceOf(pos.Data, q)
		return []rpc.Account{
			{Pubkey: "huge", Data: shares}, {Pubkey: "inverted", Data: shares},
			{Pubkey: wide.Address, Data: sliceOf(wide.Data, q)}, {Pubkey: pos.Address, Data: shares},
		}, nil
	})
	vanished := accountsFunc(func(keys []string) ([]*rpc.Account, error) { return make([]*rpc.Account, len(keys)), nil })
	res, err := DLMMStrategy{Scanner: scanner, Accounts: vanished}.Holders(context.Background(), &domain.Pool{Address: "pair"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Holders) != 1 || res.Holders[0].PositionAddress != pos.Address {
		t.Fatalf("only the valid position must remain: %+v", res.Holders)
	}
	if !res.Incomplete || len(res.Warnings) == 0 {
		t.Fatalf("skipped positions must be reported: %+v", res)
	}
}
