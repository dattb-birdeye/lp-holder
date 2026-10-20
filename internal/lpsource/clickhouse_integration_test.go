package lpsource

import (
	"context"
	"os"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/config"
)

// TestClickHouseLPBalancesIntegration runs the real query against a
// ClickHouse that has wallet_info.token_holder_local. Skipped by default:
//
//	LP_IT_CLICKHOUSE_SHARDS=localhost:9000 LP_IT_TOKEN=<mint> go test ./internal/lpsource -run Integration -v
func TestClickHouseLPBalancesIntegration(t *testing.T) {
	shards, token := os.Getenv("LP_IT_CLICKHOUSE_SHARDS"), os.Getenv("LP_IT_TOKEN")
	if shards == "" || token == "" {
		t.Skip("set LP_IT_CLICKHOUSE_SHARDS and LP_IT_TOKEN to run")
	}
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{"LP_RPC_URL": "https://unused", "LP_CLICKHOUSE_SHARDS": shards, "LP_CLICKHOUSE_MAX_ROWS": "500"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewClickHouse(cfg.ClickHouse)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := src.LPBalances(ctx, token, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || len(out) > 500 {
		t.Fatalf("got %d rows", len(out))
	}
	seen := map[string]bool{}
	for i, b := range out {
		if b.Amount == 0 || seen[b.Owner] {
			t.Fatalf("row %d invalid or duplicate owner: %+v", i, b)
		}
		if i > 0 && out[i-1].Amount < b.Amount {
			t.Fatalf("rows must be sorted by amount desc at %d", i)
		}
		seen[b.Owner] = true
	}
	t.Logf("%d owners, top=%+v", len(out), out[0])
}
