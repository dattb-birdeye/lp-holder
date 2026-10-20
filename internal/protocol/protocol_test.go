package protocol

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// Fixtures are real mainnet accounts (see README "Layout verification").
// expected.json holds values computed by an independent Python implementation.

const (
	sol  = "So11111111111111111111111111111111111111112"
	usdc = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

type fixture struct {
	Address string `json:"address"`
	Owner   string `json:"owner"`
	Data    string `json:"data"`
}

func loadFixture(t *testing.T, name string) (fixture, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	data, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return f, data
}

func loadExpected(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatalf("read expected.json: %v", err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse expected.json: %v", err)
	}
	return out
}

func bigFrom(t *testing.T, v any) *big.Int {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string big int, got %T", v)
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad big int %q", s)
	}
	return n
}

func applySlice(t *testing.T, data []byte, q rpc.ProgramAccountsQuery) []byte {
	t.Helper()
	if q.DataSlice == nil {
		return data
	}
	end := q.DataSlice.Offset + q.DataSlice.Length
	if end > len(data) {
		t.Fatalf("slice [%d,%d) exceeds account size %d", q.DataSlice.Offset, end, len(data))
	}
	return data[q.DataSlice.Offset:end]
}

func detectAndDecode(t *testing.T, name string, want domain.Protocol) *domain.Pool {
	t.Helper()
	f, data := loadFixture(t, name)
	adapter, err := DefaultRegistry().Detect(f.Owner, data)
	if err != nil {
		t.Fatalf("detect %s: %v", name, err)
	}
	if adapter.Protocol() != want {
		t.Fatalf("detect %s: got %s, want %s", name, adapter.Protocol(), want)
	}
	pool, err := adapter.DecodePool(f.Address, data)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return pool
}

func assertEq[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}

func TestDecodeLPTokenPools(t *testing.T) {
	t.Run("raydium amm v4", func(t *testing.T) {
		p := detectAndDecode(t, "raydium_ammv4_pool", domain.ProtocolRaydiumAMMV4)
		assertEq(t, "kind", p.Kind, domain.KindLPToken)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "vaultA", p.VaultA, "DQyrAcCrDXQ7NeoqGgDCZwBvWDcYmFCjSb9JtteuvPpz")
		assertEq(t, "vaultB", p.VaultB, "HLmqeL62xR1QoZ1HKKbXRrdN1p3phKpxRMb2VVopvBBz")
		assertEq(t, "lpMint", p.LPMint, "8HoQnePLqPj4M7PUDzfw8e3Ymdwgc7NLGnaTUapubyvu")
		if p.LPSupply == nil || p.LPSupply.Sign() <= 0 {
			t.Errorf("lp supply should be positive, got %v", p.LPSupply)
		}
	})
	t.Run("raydium cpmm", func(t *testing.T) {
		p := detectAndDecode(t, "raydium_cpmm_pool", domain.ProtocolRaydiumCPMM)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, "Dz9mQ9NzkBcCsuGPFJ3r1bS4wgqKMHBPiVuniW8Mbonk")
		assertEq(t, "lpMint", p.LPMint, "5rASbyrUYh4eVmZpgN6MxVY2w83dC4PFg9U9WYc9HW7g")
		assertEq(t, "vaultA", p.VaultA, "HgNPDD8bpbSrGyHegiCT5xrYxHTfwLfZydwGkjNCJRKA")
		assertEq(t, "vaultB", p.VaultB, "9xsCiNwYQXM3ZeHFSVj9JQdP1vREJREpN23f6wvxA1ty")
		if p.Reserve.ExcludeA == 0 || p.Reserve.ExcludeB == 0 {
			t.Errorf("expected protocol/fund fees to be excluded, got %+v", p.Reserve)
		}
	})
	t.Run("lifinity v2", func(t *testing.T) {
		p := detectAndDecode(t, "lifinity_v2_pool", domain.ProtocolLifinityV2)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, "USDSwr9ApdHk5bvJKMjzff41FfuX8bSxdKcR81vTwcA")
	})
	t.Run("meteora damm v1", func(t *testing.T) {
		p := detectAndDecode(t, "meteora_dammv1_pool", domain.ProtocolMeteoraDAMMV1)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "vaultA", p.Reserve.MeteoraVaultA, "FERjPVNEa7Udq8CEv68h6tPL46Tq7ieE49HrE2wea3XT")
		assertEq(t, "vaultB", p.Reserve.MeteoraVaultB, "3ESUFCnRNgZ7Mn2mPPUMmXYaKU8jpnV9VtA17M7t2mHQ")
		assertEq(t, "aVaultLP", p.Reserve.VaultLPAccountA, "ArUtneeaGousStHK3R3nCsJZS2mLQuvq7aLkRxvgNEcm")
		assertEq(t, "bVaultLP", p.Reserve.VaultLPAccountB, "FuwcYk4xhC9Nm3M1yBZWzSBYemwHs1v8ygeLrYBr8doM")
	})
	t.Run("meteora vault", func(t *testing.T) {
		_, data := loadFixture(t, "meteora_vault")
		v, err := DecodeMeteoraVault(data)
		if err != nil {
			t.Fatal(err)
		}
		assertEq(t, "lpMint", v.LPMint, "FZN7QZ8ZUUAxMPfxYEYkH3cXUASzH8EqA6B4tyCL8f1j")
		if v.TotalAmount == 0 {
			t.Error("vault total amount should be positive")
		}
	})
}

func TestDecodePositionPools(t *testing.T) {
	t.Run("raydium clmm", func(t *testing.T) {
		p := detectAndDecode(t, "raydium_clmm_pool", domain.ProtocolRaydiumCLMM)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "decimalsA", p.DecimalsA, uint8(9))
		assertEq(t, "decimalsB", p.DecimalsB, uint8(6))
		assertEq(t, "vaultA", p.VaultA, "4ct7br2vTPzfdmY3S5HLtTxcGSBfn6pnw98hsS6v359A")
		assertPlausibleSOLPrice(t, poolmath.PriceFromSqrtX64(p.SqrtPriceX64, 9, 6))
	})
	t.Run("orca whirlpool", func(t *testing.T) {
		p := detectAndDecode(t, "orca_whirlpool_pool", domain.ProtocolOrcaWhirlpool)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "vaultA", p.VaultA, "EUuUbDcafPrmVTD5M6qoJAoyyNbihBhugADAxRMn5he9")
		assertEq(t, "vaultB", p.VaultB, "2WLWEuKDgkDUccTpbwYp1GToYktiSB1cXvreHUwiSUVP")
		assertPlausibleSOLPrice(t, poolmath.PriceFromSqrtX64(p.SqrtPriceX64, 9, 6))
	})
	t.Run("meteora dlmm", func(t *testing.T) {
		p := detectAndDecode(t, "meteora_dlmm_pair", domain.ProtocolMeteoraDLMM)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "reserveX", p.VaultA, "DwZz4S1Z1LBXomzmncQRVKCYhjCqSAMQ6RPKbUAadr7H")
		assertEq(t, "reserveY", p.VaultB, "4N22J4vW2juHocTntJNmXywSonYjkndCwahjZ2cYLDgb")
		assertEq(t, "binStep", *p.BinStep, uint16(10))
		assertPlausibleSOLPrice(t, poolmath.DLMMPrice(*p.ActiveBinID, *p.BinStep, 9, 6))
	})
	t.Run("meteora damm v2", func(t *testing.T) {
		exp := loadExpected(t)["dammv2"]
		p := detectAndDecode(t, "meteora_dammv2_pool", domain.ProtocolMeteoraDAMMV2)
		assertEq(t, "mintA", p.MintA, sol)
		assertEq(t, "mintB", p.MintB, usdc)
		assertEq(t, "sqrtMin", p.SqrtMinPriceX64.String(), bigFrom(t, exp["sqrt_min"]).String())
		assertEq(t, "sqrtMax", p.SqrtMaxPriceX64.String(), bigFrom(t, exp["sqrt_max"]).String())
		assertEq(t, "sqrtPrice", p.SqrtPriceX64.String(), bigFrom(t, exp["sqrt_price"]).String())
	})
}

// assertPlausibleSOLPrice guards against garbage offsets: a wrong offset gives
// prices that are off by many orders of magnitude.
func assertPlausibleSOLPrice(t *testing.T, price float64) {
	t.Helper()
	if price < 10 || price > 10_000 {
		t.Errorf("SOL/USDC price %v is implausible; layout offsets are probably wrong", price)
	}
}

func TestTickPositionSlices(t *testing.T) {
	exp := loadExpected(t)
	cases := []struct {
		name, poolFixture, posFixture, expKey, mintKey string
		adapter                                        TickAdapter
	}{
		{"raydium clmm", "raydium_clmm_pool", "raydium_clmm_position", "raydium_clmm", "nft_mint", RaydiumCLMM{}},
		{"orca", "orca_whirlpool_pool", "orca_whirlpool_position", "orca", "position_mint", OrcaWhirlpool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := exp[tc.expKey]
			poolF, poolData := loadFixture(t, tc.poolFixture)
			_, posData := loadFixture(t, tc.posFixture)
			pool, err := tc.adapter.DecodePool(poolF.Address, poolData)
			if err != nil {
				t.Fatal(err)
			}
			pos, err := tc.adapter.DecodePositionSlice(applySlice(t, posData, tc.adapter.PositionQuery(pool.Address)))
			if err != nil {
				t.Fatal(err)
			}
			assertEq(t, "nft", pos.NFTMint, e[tc.mintKey].(string))
			assertEq(t, "tickLower", pos.TickLower, int32(e["tick_lower"].(float64)))
			assertEq(t, "tickUpper", pos.TickUpper, int32(e["tick_upper"].(float64)))
			assertEq(t, "liquidity", pos.Liquidity.String(), bigFrom(t, e["liquidity"]).String())

			a, b := poolmath.ConcentratedAmounts(pos.Liquidity, pool.SqrtPriceX64,
				poolmath.TickToSqrtPriceX64(pos.TickLower), poolmath.TickToSqrtPriceX64(pos.TickUpper))
			assertClose(t, "amountA", a, bigFrom(t, e["amount_a_raw"]))
			assertClose(t, "amountB", b, bigFrom(t, e["amount_b_raw"]))
		})
	}
}

func TestDAMMV2PositionSlice(t *testing.T) {
	e := loadExpected(t)["dammv2"]
	poolF, poolData := loadFixture(t, "meteora_dammv2_pool")
	_, posData := loadFixture(t, "meteora_dammv2_position")
	adapter := MeteoraDAMMV2{}
	pool, err := adapter.DecodePool(poolF.Address, poolData)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := adapter.DecodePositionSlice(applySlice(t, posData, adapter.PositionQuery(pool.Address)))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "nft", pos.NFTMint, e["nft_mint"].(string))
	assertEq(t, "liquidity", pos.Liquidity.String(), bigFrom(t, e["liquidity"]).String())

	a, b := poolmath.DAMMV2Amounts(pos.Liquidity, pool.SqrtPriceX64, pool.SqrtMinPriceX64, pool.SqrtMaxPriceX64)
	assertEq(t, "amountA", a.String(), bigFrom(t, e["amount_a_raw"]).String())
	assertEq(t, "amountB", b.String(), bigFrom(t, e["amount_b_raw"]).String())
}

func TestDLMMPositionValuation(t *testing.T) {
	e := loadExpected(t)["dlmm"]
	_, posData := loadFixture(t, "meteora_dlmm_position")
	adapter := MeteoraDLMM{}
	pair := "BGm1tav58oGcsQJehL9WXBFXF7D27vZsKefj4xJKD5Y"

	shares, err := DecodeDLMMSharesSlice(applySlice(t, posData, adapter.PositionSharesQuery(pair)))
	if err != nil {
		t.Fatal(err)
	}
	lower, upper, err := DecodeDLMMRangeSlice(applySlice(t, posData, adapter.PositionRangeQuery(pair)))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "owner", shares.Owner, e["owner"].(string))
	assertEq(t, "lower", lower, int32(e["lower"].(float64)))
	assertEq(t, "upper", upper, int32(e["upper"].(float64)))

	bins := make(map[int32]DLMMBin)
	for _, name := range []string{"meteora_dlmm_binarray_34", "meteora_dlmm_binarray_33"} {
		_, data := loadFixture(t, name)
		if err := DecodeDLMMBinArray(data, bins); err != nil {
			t.Fatal(err)
		}
	}
	x, y := new(big.Int), new(big.Int)
	for i := 0; i <= int(upper-lower); i++ {
		bin, ok := bins[lower+int32(i)]
		if !ok || shares.Shares[i].Sign() == 0 {
			continue
		}
		x.Add(x, poolmath.MulDiv(shares.Shares[i], new(big.Int).SetUint64(bin.AmountX), bin.LiquiditySupply))
		y.Add(y, poolmath.MulDiv(shares.Shares[i], new(big.Int).SetUint64(bin.AmountY), bin.LiquiditySupply))
	}
	assertEq(t, "amountX", x.String(), bigFrom(t, e["amount_x_raw"]).String())
	assertEq(t, "amountY", y.String(), bigFrom(t, e["amount_y_raw"]).String())
}

func TestDetectRejectsUnknownAccounts(t *testing.T) {
	_, data := loadFixture(t, "orca_whirlpool_position")
	if _, err := DefaultRegistry().Detect(OrcaWhirlpoolProgramID, data); err == nil {
		t.Fatal("a position account must not be detected as a pool")
	}
	if _, err := DefaultRegistry().Detect("11111111111111111111111111111111", data); err == nil {
		t.Fatal("unknown program must be rejected")
	}
}

func TestDecodersRejectShortData(t *testing.T) {
	short := make([]byte, 16)
	for _, a := range []Adapter{RaydiumAMMV4{}, RaydiumCPMM{}, LifinityV2{}, MeteoraDAMMV1{},
		RaydiumCLMM{}, OrcaWhirlpool{}, MeteoraDLMM{}, MeteoraDAMMV2{}} {
		if _, err := a.DecodePool("x", short); err == nil {
			t.Errorf("%s: expected error on short data", a.Protocol())
		}
	}
}

// assertClose allows a tiny relative error from float64 tick math.
func assertClose(t *testing.T, field string, got, want *big.Int) {
	t.Helper()
	diff := new(big.Int).Abs(new(big.Int).Sub(got, want))
	tolerance := new(big.Int).Quo(want, big.NewInt(1_000_000_000)) // 1e-9 relative
	if tolerance.Sign() == 0 {
		tolerance = big.NewInt(1)
	}
	if diff.Cmp(tolerance) > 0 {
		t.Errorf("%s: got %s, want %s (diff %s)", field, got, want, diff)
	}
}

func TestDLMMExtendedShares(t *testing.T) {
	e := loadExpected(t)["dlmm_extended"]
	_, data := loadFixture(t, "meteora_dlmm_position_extended")
	pair := "BGm1tav58oGcsQJehL9WXBFXF7D27vZsKefj4xJKD5Y"
	lower, upper, err := DecodeDLMMRangeSlice(applySlice(t, data, MeteoraDLMM{}.PositionRangeQuery(pair)))
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "lower", lower, int32(e["lower"].(float64)))
	assertEq(t, "upper", upper, int32(e["upper"].(float64)))

	ext, err := DecodeDLMMExtendedShares(data, int(upper-lower)+1)
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, "count", len(ext), int(e["ext_count"].(float64)))
	sum := new(big.Int)
	for _, s := range ext {
		sum.Add(sum, s)
	}
	assertEq(t, "first", ext[0].String(), bigFrom(t, e["ext_first"]).String())
	assertEq(t, "sum", sum.String(), bigFrom(t, e["ext_sum"]).String())

	if _, err := DecodeDLMMExtendedShares(data[:8200], int(upper-lower)+1); err == nil {
		t.Error("truncated extension must fail")
	}
	if ext, err := DecodeDLMMExtendedShares(data, 70); err != nil || ext != nil {
		t.Errorf("70-bin position has no extension, got %v, %v", ext, err)
	}
}
