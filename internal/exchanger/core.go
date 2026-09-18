package exchanger

import "math"

// endpoints is the single shared definition of the four terminal temperatures
// for a heat duty q (always taken positive, heat flowing hot -> cold). Both
// the ε-NTU path and the LMTD path use these functions, so cross-comparison
// validates the heat-transfer law rather than two rephrasings of the same line.
type endpoints struct {
	cMin float64
	cMax float64
	// hotIsMin records which side carries C_min; it decides how the duty
	// splits between the two outlet temperatures.
	hotIsMin bool
	tHIn     float64
	tCIn     float64
}

// tHot returns the hot-side outlet temperature for duty q.
func (e endpoints) tHot(q float64) float64 {
	cHot := e.cMax
	if e.hotIsMin {
		cHot = e.cMin
	}
	return e.tHIn - q/cHot
}

// tCold returns the cold-side outlet temperature for duty q.
func (e endpoints) tCold(q float64) float64 {
	// The cold stream carries C_max exactly when the hot stream carries
	// C_min, and vice versa.
	cCold := e.cMax
	if !e.hotIsMin {
		cCold = e.cMin
	}
	return e.tCIn + q/cCold
}

// endDeltas returns the two terminal temperature differences in the order
// (d1, d2) such that LMTD = (d1-d2)/ln(d1/d2):
//
//	counter:  d1 = Th_in - Tc_out,  d2 = Th_out - Tc_in
//	parallel: d1 = Th_in - Tc_in,   d2 = Th_out - Tc_out
func endDeltas(a Arrangement, ep endpoints, q float64) (d1, d2 float64) {
	thOut := ep.tHot(q)
	tcOut := ep.tCold(q)
	if a == Counter {
		return ep.tHIn - tcOut, thOut - ep.tCIn
	}
	return ep.tHIn - ep.tCIn, thOut - tcOut
}

// lmtd evaluates the log-mean temperature difference. The regime q == 0 is a
// limit: there is no heat transfer, and downstream code only evaluates lmtd at
// q = 0 to form a sign in the bracketing solver, where +Inf is the correct
// mathematical value.
//
// Near d1 == d2 the naive (d1-d2)/log(d1/d2) loses significance; it is
// evaluated from the stable identity
//
//	LMTD = dMean * u/atanh(u),  u = (d1-d2)/(d1+d2)
//
// since log(d1/d2) = 2*atanh(u). A Taylor series covers |u| -> 0. The
// returned boolean is false if either terminal difference is non-positive (an
// illegal/over-specified state).
func lmtd(d1, d2 float64) (float64, bool) {
	if d1 <= 0 || d2 <= 0 {
		return 0, false
	}
	if d1 == d2 {
		return d1, true
	}
	// Keep the ratio bounded without changing its value.
	if d1 < d2 {
		d1, d2 = d2, d1
	}
	mean := 0.5 * (d1 + d2)
	u := (d1 - d2) / (d1 + d2) // 0 < u < 1
	if u < 1e-4 {
		// u/atanh(u) = 1 - u²/3 - 2u⁴/45 - ...
		u2 := u * u
		return mean * (1 - u2/3 - 2*u2*u2/45), true
	}
	return mean * u / math.Atanh(u), true
}

// effectiveness returns the ε-NTU effectiveness for the arrangement.
//
//	cr = C_min/C_max ∈ (0,1]; ntu = UA/C_min.
//
// Balanced flow (cr == 1) for counter-flow is a genuine removable singularity
// with limit ε = NTU/(1+NTU). The same singularity bites numerically whenever
// NTU*(1-Cr) drops below ~1e-8: exp(-NTU(1-Cr)) then rounds to exactly 1 in
// float64 and the unguarded ratio (1-E)/(1-Cr*E) evaluates to 0/0 — a spurious
// zero duty (the "divergence near balanced flow" the rating rules require be
// handled). In that regime the balanced-limit formula is used; its deviation
// from the true near-balanced value is O(NTU*(1-Cr)), far tighter than the
// method agreement tolerance.
func effectiveness(a Arrangement, cr, ntu float64) float64 {
	if a == Parallel {
		// 1-exp(-NTU(1+Cr)) / (1+Cr). Cr=1 is perfectly regular here.
		return (1 - math.Exp(-ntu*(1+cr))) / (1 + cr)
	}
	if cr == 1 || ntu*(1-cr) < 1e-8 {
		return ntu / (1 + ntu)
	}
	// Counter-flow, written around E=exp(-NTU(1-Cr)) to stay stable when the
	// exponent argument is large (distant from the balanced point).
	e := math.Exp(-ntu * (1 - cr))
	return (1 - e) / (1 - cr*e)
}

// lmtdSolve finds the duty q at which UA*LMTD(q) = q by bisection. The scalar
// function g(q) = UA*LMTD(q) - q has exactly one root on the physical
// interval:
//
//   - LMTD is strictly decreasing in q from (Th_in-Tc_in) at q=0 down to 0 at
//     the arrangement's thermal pinch;
//   - g(0) = UA*(Th_in-Tc_in) > 0 and g <= 0 at the pinch.
//
// For counter-flow the pinch sits at qMax (both terminal deltas vanish); for
// parallel flow it sits earlier, at qPinch = (Th_in-Tc_in)/(1/Cmin+1/Cmax),
// where the two streams reach a common outlet temperature. The search upper
// bound is chosen strictly inside the positive-LMTD region so every function
// evaluation is well defined. Bisection cannot diverge and needs no
// derivative; 100 halvings drive the interval to machine width. This is the
// independent LMTD path.
func lmtdSolve(a Arrangement, ep endpoints, ua, qMax float64) (float64, float64, int) {
	dtIn := ep.tHIn - ep.tCIn
	qPinch := qMax // counter-flow pinch
	if a == Parallel {
		qPinch = dtIn / (1/ep.cMin + 1/ep.cMax)
	}
	// Start infinitesimally below the pinch: LMTD -> 0 there so g < 0, giving
	// a guaranteed sign change versus g(0) > 0.
	lo, hi := 0.0, math.Nextafter(qPinch, math.Inf(-1))
	if hi <= 0 {
		hi = qPinch * (1 - 1e-12)
	}
	glo := ua * dtIn // g(0) = UA*LMTD(0) > 0
	d1, d2 := endDeltas(a, ep, hi)
	lHi, ok := lmtd(d1, d2)
	if !ok {
		lHi = 0
	}
	ghi := ua*lHi - hi // <= 0 at the pinch
	if !(glo > 0) || !(ghi < 0) {
		// Degenerate numeric corner: return the pinch duty and let the caller
		// decide feasibility rather than emitting an unsupported value.
		return qPinch, qPinch, 0
	}
	const iters = 100
	mid := 0.0
	for i := 0; i < iters; i++ {
		mid = 0.5 * (lo + hi)
		d1, d2 := endDeltas(a, ep, mid)
		lm, ok := lmtd(d1, d2)
		if !ok {
			// mid sits at/above the crossing boundary: push the bracket down.
			hi = mid
			ghi = -1
			continue
		}
		gm := ua*lm - mid
		if gm == 0 {
			return mid, qPinch, i + 1
		}
		if (gm < 0) == (glo < 0) {
			lo, glo = mid, gm
		} else {
			hi, ghi = mid, gm
		}
	}
	_ = ghi
	return mid, qPinch, iters
}
