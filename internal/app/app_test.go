package app

import (
	"testing"

	"birdeye.so/lp-holder/internal/config"
)

func TestNewWiresEveryProtocol(t *testing.T) {
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{
			"LP_RPC_URL":           "https://rpc.example",
			"LP_PRICE_BASE_URL":    "https://prices.example",
			"LP_CLICKHOUSE_SHARDS": "127.0.0.1:1|127.0.0.1:2,127.0.0.1:3",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Service == nil || len(a.closers) != 1 {
		t.Fatalf("app not wired: %+v", a)
	}
}
