// Package judge is intel-numbers' judging: results per campaign and ad with
// likely ranges, alerts, and suggestions. It reads Intel's own tables (and
// launch_api, when Launch publishes it) and writes Intel's own tables; it
// never talks to Taboola or RedTrack.
package judge

import "math"

// z returns the two-sided normal quantile for a likely range at level
// (0.9 → 1.645).
func z(level float64) float64 {
	if level <= 0 || level >= 1 {
		level = 0.9
	}
	return normQuantile(0.5 + level/2)
}

// normQuantile is the standard normal quantile (Acklam's approximation,
// good to about 1e-9, plenty for ranges).
func normQuantile(p float64) float64 {
	const (
		a1, a2, a3, a4, a5, a6 = -3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00
		b1, b2, b3, b4, b5     = -5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01
		c1, c2, c3, c4, c5, c6 = -7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00
		d1, d2, d3, d4         = 7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00
		low                    = 0.02425
	)
	switch {
	case p <= 0:
		return math.Inf(-1)
	case p >= 1:
		return math.Inf(1)
	case p < low:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) / ((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	case p > 1-low:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) / ((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	}
	q := p - 0.5
	r := q * q
	return (((((a1*r+a2)*r+a3)*r+a4)*r+a5)*r + a6) * q / (((((b1*r+b2)*r+b3)*r+b4)*r+b5)*r + 1)
}

// Rate is a share with its likely range.
type Rate struct {
	Value, Low, High float64
	OK               bool // false when there was nothing to divide by
}

// wilson is the Wilson score range of k successes in n tries: honest at
// small counts and at 0, where a plain ± range is not.
func wilson(k, n, level float64) Rate {
	if n <= 0 {
		return Rate{}
	}
	if k > n {
		k = n
	}
	zz := z(level)
	p := k / n
	den := 1 + zz*zz/n
	mid := (p + zz*zz/(2*n)) / den
	half := zz * math.Sqrt(p*(1-p)/n+zz*zz/(4*n*n)) / den
	return Rate{Value: p, Low: math.Max(0, mid-half), High: math.Min(1, mid+half), OK: true}
}

// shrunk is k in n pulled toward a prior rate worth strength tries (the
// campaign's rate for a small ad), with a Wilson-style range on the pooled
// counts. The range stays the ad's own when it has plenty of tries.
func shrunk(k, n, prior, strength, level float64) Rate {
	if n <= 0 && strength <= 0 {
		return Rate{}
	}
	kk, nn := k+prior*strength, n+strength
	r := wilson(kk, nn, level)
	// The pooled range is narrower than the ad's own evidence allows; widen
	// it back to the width n alone would give, around the pooled value.
	own := wilson(k, math.Max(n, 1), level)
	if n > 0 {
		half := (own.High - own.Low) / 2
		r.Low = math.Max(0, r.Value-half)
		r.High = math.Min(1, r.Value+half)
	}
	return r
}

// NoSaleOdds is how often an ad whose true cost per sale is usual gets to
// spend without a sale: exp(-spend/usual), counting chance only.
func NoSaleOdds(spend, usual float64) float64 {
	if usual <= 0 {
		return 1
	}
	return math.Exp(-spend / usual)
}

// costPerSaleRange is the likely range of the true cost per sale after sales
// sales on spend (Poisson counts): wide at small counts.
func costPerSaleRange(spend float64, sales int64, level float64) (low, high float64) {
	if sales <= 0 || spend <= 0 {
		return 0, 0
	}
	zz := z(level)
	// Wilson–Hilferty style bounds on a Poisson count.
	k := float64(sales)
	lo := k * math.Pow(1-1/(9*k)-zz/(3*math.Sqrt(k)), 3)
	k1 := k + 1
	hi := k1 * math.Pow(1-1/(9*k1)+zz/(3*math.Sqrt(k1)), 3)
	if lo <= 0 {
		lo = 1e-9
	}
	return spend / hi, spend / lo
}
