package poolmath

import (
	"math"
	"math/big"
	"testing"
)

func TestConcentratedAmountsByPriceRegion(t *testing.T) {
	L := big.NewInt(1_000_000_000)
	lower, upper := TickToSqrtPriceX64(-100), TickToSqrtPriceX64(100)

	// Price below range: only token A.
	a, b := ConcentratedAmounts(L, TickToSqrtPriceX64(-200), lower, upper)
	if a.Sign() <= 0 || b.Sign() != 0 {
		t.Errorf("below range: a=%s b=%s", a, b)
	}
	// Price above range: only token B.
	a, b = ConcentratedAmounts(L, TickToSqrtPriceX64(200), lower, upper)
	if a.Sign() != 0 || b.Sign() <= 0 {
		t.Errorf("above range: a=%s b=%s", a, b)
	}
	// In range at tick 0 (price 1): symmetric range holds ~equal amounts.
	a, b = ConcentratedAmounts(L, TickToSqrtPriceX64(0), lower, upper)
	ratio := new(big.Float).Quo(new(big.Float).SetInt(a), new(big.Float).SetInt(b))
	if r, _ := ratio.Float64(); math.Abs(r-1) > 1e-3 {
		t.Errorf("in range: a/b = %v, want ~1", r)
	}
	// Degenerate inputs.
	if a, b := ConcentratedAmounts(big.NewInt(0), lower, lower, upper); a.Sign() != 0 || b.Sign() != 0 {
		t.Error("zero liquidity must give zero amounts")
	}
	if a, _ := ConcentratedAmounts(L, lower, upper, lower); a.Sign() != 0 {
		t.Error("inverted range must give zero amounts")
	}
}

func TestDAMMV2AmountsClampAndScale(t *testing.T) {
	q := new(big.Int).Lsh(big.NewInt(1), 64)
	min := new(big.Int).Quo(q, big.NewInt(2)) // sqrt price 0.5
	max := new(big.Int).Mul(q, big.NewInt(2)) // sqrt price 2
	// L is Q64 scaled: L_real = 1e9.
	L := new(big.Int).Mul(big.NewInt(1_000_000_000), q)

	a, b := DAMMV2Amounts(L, q, min, max) // sqrt price 1
	// a = L_real*(1/1 - 1/2) = 5e8 ; b = L_real*(1 - 0.5) = 5e8
	if a.Int64() != 500_000_000 || b.Int64() != 500_000_000 {
		t.Errorf("a=%s b=%s, want 5e8 each", a, b)
	}
	a, b = DAMMV2Amounts(L, new(big.Int).Mul(q, big.NewInt(4)), min, max) // above max
	if a.Sign() != 0 || b.Int64() != 1_500_000_000 {
		t.Errorf("above range: a=%s b=%s", a, b)
	}
	if a, b := DAMMV2Amounts(nil, q, min, max); a.Sign() != 0 || b.Sign() != 0 {
		t.Error("nil liquidity must give zero")
	}
}

func TestUnitsAndPrices(t *testing.T) {
	if got := ToUI(big.NewInt(1_500_000), 6); got != 1.5 {
		t.Errorf("ToUI = %v", got)
	}
	if ToUI(nil, 6) != 0 {
		t.Error("ToUI(nil) must be 0")
	}
	// sqrt price 1.0 in Q64.64 with decimals 9/6 -> price 1e3.
	q := new(big.Int).Lsh(big.NewInt(1), 64)
	if got := PriceFromSqrtX64(q, 9, 6); math.Abs(got-1000) > 1e-9 {
		t.Errorf("PriceFromSqrtX64 = %v", got)
	}
	if PriceFromSqrtX64(nil, 9, 6) != 0 {
		t.Error("nil sqrt price must give 0")
	}
	// Meteora bin 0 is price 1; there is no 2^23 offset.
	if got := DLMMPrice(0, 10, 6, 6); got != 1 {
		t.Errorf("DLMMPrice(0) = %v, want 1", got)
	}
	if got := DLMMPrice(-2259, 10, 9, 6); got < 50 || got > 500 {
		t.Errorf("DLMMPrice SOL/USDC = %v, implausible", got)
	}
	if MulDiv(big.NewInt(10), big.NewInt(3), big.NewInt(0)).Sign() != 0 {
		t.Error("MulDiv by zero must be 0")
	}
	if MulDiv(big.NewInt(10), big.NewInt(3), big.NewInt(4)).Int64() != 7 {
		t.Error("MulDiv must floor")
	}
	if TickToSqrtPriceX64(MinTick).Sign() <= 0 {
		t.Error("min tick sqrt price must be positive")
	}
}
