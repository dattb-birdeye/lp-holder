// Command lp-holder-cli prints the holders of one pool as JSON, using the same
// pipeline as the API. Handy for debugging a pool or checking a decoder change:
//
//	LP_RPC_URL=https://api.mainnet-beta.solana.com go run ./cmd/lp-holder-cli -pool <address>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"birdeye.so/lp-holder/internal/app"
	"birdeye.so/lp-holder/internal/config"
	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/httpapi"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/service"
)

// printCoverage compares the sum of all holders' token amounts with the pool
// reserves. ~100% means the decoding and math are consistent; the gap is
// unclaimed fees, rounding, or LP held outside the listed accounts.
func printCoverage(s *domain.Snapshot) {
	var sumA, sumB, sumPct float64
	for _, h := range s.Holders {
		sumA += h.AmountA
		sumB += h.AmountB
		sumPct += h.Percentage
	}
	resA := poolmath.ToUI(s.Pool.ReserveA, s.Pool.DecimalsA)
	resB := poolmath.ToUI(s.Pool.ReserveB, s.Pool.DecimalsB)
	fmt.Fprintf(os.Stderr, "coverage: holders=%d Σpercentage=%.4f%%\n", len(s.Holders), sumPct)
	fmt.Fprintf(os.Stderr, "  token A: Σholders=%.6f reserve=%.6f (%.4f%%)\n", sumA, resA, pct(sumA, resA))
	fmt.Fprintf(os.Stderr, "  token B: Σholders=%.6f reserve=%.6f (%.4f%%)\n", sumB, resB, pct(sumB, resB))
}

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b * 100
}

func main() {
	pool := flag.String("pool", "", "pool address (required)")
	limit := flag.Int("limit", 10, "rows to print")
	offset := flag.Int("offset", 0, "rows to skip")
	groupBy := flag.String("group-by", "position", "position | wallet")
	timeout := flag.Duration("timeout", 3*time.Minute, "overall timeout")
	check := flag.Bool("check", false, "print Σ holder amounts vs pool reserves (coverage) to stderr")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if *pool == "" {
		flag.Usage()
		os.Exit(2)
	}
	q := service.Query{Pool: *pool, Offset: *offset, Limit: *limit, GroupBy: service.GroupBy(*groupBy)}
	if err := run(q, *timeout, *check); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(q service.Query, timeout time.Duration, check bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	application, err := app.New(cfg)
	if err != nil {
		return err
	}
	defer application.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	started := time.Now()
	page, err := application.Service.Holders(ctx, q)
	if err != nil {
		return err
	}
	slog.Info("done", "duration", time.Since(started).Round(time.Millisecond), "total", page.Total)
	if check {
		printCoverage(page.Snapshot)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(httpapi.NewPageResponse(page))
}
