package config

import (
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := LoadFrom(envOf(map[string]string{"LP_RPC_URL": "https://rpc.example"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.RPC.MaxConcurrency != 8 || !cfg.RPC.UseDAS || cfg.RPC.UseGPAV2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Cache.TTL != time.Minute || cfg.Holders.MaxPositions != 50_000 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Price.Enabled() || cfg.ClickHouse.Enabled() {
		t.Fatal("optional integrations must be off by default")
	}
}

func TestOverridesAndShards(t *testing.T) {
	cfg, err := LoadFrom(envOf(map[string]string{
		"LP_RPC_URL":           "https://rpc.example/?api-key=k",
		"LP_RPC_USE_GPA_V2":    "true",
		"LP_CACHE_TTL":         "30s",
		"LP_PRICE_BASE_URL":    "https://public-api.birdeye.so",
		"LP_CLICKHOUSE_SHARDS": "a:9000|b:9000, c:9000 ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RPC.UseGPAV2 || cfg.Cache.TTL != 30*time.Second || !cfg.Price.Enabled() {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if len(cfg.ClickHouse.Shards) != 2 || len(cfg.ClickHouse.Shards[0]) != 2 || cfg.ClickHouse.Shards[1][0] != "c:9000" {
		t.Fatalf("shards = %v", cfg.ClickHouse.Shards)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"LP_RPC_URL is required":         {},
		"must be an http(s) URL":         {"LP_RPC_URL": "ws://x"},
		"invalid integer":                {"LP_RPC_URL": "https://x", "LP_MAX_POSITIONS": "many"},
		"invalid duration":               {"LP_RPC_URL": "https://x", "LP_CACHE_TTL": "soon"},
		"invalid boolean":                {"LP_RPC_URL": "https://x", "LP_RPC_USE_DAS": "maybe"},
		"must look like db.table":        {"LP_RPC_URL": "https://x", "LP_CLICKHOUSE_SHARDS": "a:9000", "LP_CLICKHOUSE_TABLE": "x; DROP"},
		"LP_RPC_MAX_CONCURRENCY must be": {"LP_RPC_URL": "https://x", "LP_RPC_MAX_CONCURRENCY": "0"},
		"LP_COMPUTE_TIMEOUT must be > 0": {"LP_RPC_URL": "https://x", "LP_COMPUTE_TIMEOUT": "0s"},
		"LP_RPC_MAX_RETRIES must be":     {"LP_RPC_URL": "https://x", "LP_RPC_MAX_RETRIES": "-1"},
		"LP_CACHE_MAX_HOLDERS must be":   {"LP_RPC_URL": "https://x", "LP_CACHE_MAX_HOLDERS": "0"},
		"LP_MAX_CONCURRENT_COMPUTES":     {"LP_RPC_URL": "https://x", "LP_MAX_CONCURRENT_COMPUTES": "0"},
		"LP_CLICKHOUSE_MAX_ROWS must be": {"LP_RPC_URL": "https://x", "LP_CLICKHOUSE_SHARDS": "a:9000", "LP_CLICKHOUSE_MAX_ROWS": "0"},
	}
	for want, env := range cases {
		_, err := LoadFrom(envOf(env))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}
