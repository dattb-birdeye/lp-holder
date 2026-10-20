// Package poolmath converts on-chain liquidity into token amounts.
//
// All amounts are raw integer token units (*big.Int). Prices passed in are
// Q64.64 fixed point (sqrt prices) exactly as stored on-chain.
package poolmath

import (
	"math"
	"math/big"
)

var (
	q64      = new(big.Int).Lsh(big.NewInt(1), 64)
	q64Float = new(big.Float).SetInt(q64)
)

// MinTick / MaxTick are the Uniswap-v3 tick bounds used by Raydium CLMM and Orca.
const (
	MinTick = -443636
	MaxTick = 443636
)

// TickToSqrtPriceX64 returns sqrt(1.0001^tick) in Q64.64.
//
// It uses float64 (≈1e-15 relative error), which is far below what the API
// displays. Port the protocol's integer tick math if bit-exact results are needed.
func TickToSqrtPriceX64(tick int32) *big.Int {
	tick = max(MinTick, min(tick, MaxTick)) // chain data is untrusted; keep Pow finite
	f := new(big.Float).SetFloat64(math.Pow(1.0001, float64(tick)/2))
	f.Mul(f, q64Float)
	v, _ := f.Int(nil)
	if v.Sign() <= 0 {
		return big.NewInt(1)
	}
	return v
}

// ConcentratedAmounts returns token amounts of liquidity L between two sqrt
// prices for Uniswap-v3 style pools (Raydium CLMM, Orca Whirlpool), where L is
// a plain integer and sqrt prices are Q64.64:
//
//	a = L * (upper - p) * 2^64 / (p * upper)
//	b = L * (p - lower) / 2^64
//
// p is clamped to [lower, upper], so out-of-range positions hold one token only.
func ConcentratedAmounts(liquidity, sqrtPrice, sqrtLower, sqrtUpper *big.Int) (a, b *big.Int) {
	if liquidity == nil || liquidity.Sign() <= 0 || sqrtPrice == nil || sqrtLower.Sign() <= 0 || sqrtLower.Cmp(sqrtUpper) >= 0 {
		return new(big.Int), new(big.Int)
	}
	p := clamp(sqrtPrice, sqrtLower, sqrtUpper)

	a = new(big.Int)
	if p.Cmp(sqrtUpper) < 0 {
		num := new(big.Int).Mul(liquidity, new(big.Int).Sub(sqrtUpper, p))
		num.Lsh(num, 64)
		a.Quo(num, new(big.Int).Mul(p, sqrtUpper))
	}
	b = new(big.Int)
	if p.Cmp(sqrtLower) > 0 {
		b.Mul(liquidity, new(big.Int).Sub(p, sqrtLower))
		b.Rsh(b, 64)
	}
	return a, b
}

// DAMMV2Amounts returns token amounts for Meteora DAMM v2, where liquidity is
// itself Q64 scaled (cp-amm get_delta_amount_a/b):
//
//	a = L * (max - p) / (p * max)
//	b = L * (p - min) >> 128
func DAMMV2Amounts(liquidity, sqrtPrice, sqrtMin, sqrtMax *big.Int) (a, b *big.Int) {
	if liquidity == nil || liquidity.Sign() <= 0 || sqrtPrice == nil || sqrtMin == nil || sqrtMax == nil ||
		sqrtMin.Sign() <= 0 || sqrtMin.Cmp(sqrtMax) >= 0 {
		return new(big.Int), new(big.Int) // also avoids a zero denominator below
	}
	p := clamp(sqrtPrice, sqrtMin, sqrtMax)

	a = new(big.Int)
	if p.Cmp(sqrtMax) < 0 {
		num := new(big.Int).Mul(liquidity, new(big.Int).Sub(sqrtMax, p))
		a.Quo(num, new(big.Int).Mul(p, sqrtMax))
	}
	b = new(big.Int)
	if p.Cmp(sqrtMin) > 0 {
		b.Mul(liquidity, new(big.Int).Sub(p, sqrtMin))
		b.Rsh(b, 128)
	}
	return a, b
}

// MulDiv returns a*b/c (floor), or 0 when c is zero.
func MulDiv(a, b, c *big.Int) *big.Int {
	if c == nil || c.Sign() == 0 {
		return new(big.Int)
	}
	v := new(big.Int).Mul(a, b)
	return v.Quo(v, c)
}

// ToUI converts a raw amount to UI units.
func ToUI(raw *big.Int, decimals uint8) float64 {
	if raw == nil || raw.Sign() == 0 {
		return 0
	}
	f := new(big.Float).SetInt(raw)
	f.Quo(f, new(big.Float).SetFloat64(math.Pow10(int(decimals))))
	v, _ := f.Float64()
	return v
}

// PriceFromSqrtX64 returns the UI price of token A in token B.
func PriceFromSqrtX64(sqrtPrice *big.Int, decimalsA, decimalsB uint8) float64 {
	if sqrtPrice == nil || sqrtPrice.Sign() == 0 {
		return 0
	}
	f := new(big.Float).SetInt(sqrtPrice)
	f.Quo(f, q64Float)
	s, _ := f.Float64()
	return s * s * math.Pow10(int(decimalsA)-int(decimalsB))
}

// DLMMPrice returns the UI price of token X in token Y for an active bin:
// (1 + binStep/10_000)^activeID. Meteora bin ids are signed around 0; there
// is no 2^23 offset (that is Trader Joe's Liquidity Book).
func DLMMPrice(activeID int32, binStep uint16, decimalsX, decimalsY uint8) float64 {
	return math.Pow(1+float64(binStep)/10_000, float64(activeID)) * math.Pow10(int(decimalsX)-int(decimalsY))
}

func clamp(v, lo, hi *big.Int) *big.Int {
	switch {
	case v.Cmp(lo) < 0:
		return lo
	case v.Cmp(hi) > 0:
		return hi
	default:
		return v
	}
}
