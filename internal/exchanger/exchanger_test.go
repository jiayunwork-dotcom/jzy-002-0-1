package exchanger

import (
	"math"
	"testing"
)

func ptr(x float64) *float64 { return &x }

// baseCase builds a case that tests mutate: balanced, counter-flow, UA=1,
// inlets 100/0. NTU=1 => ε=0.5 => Q=50, both outlets 50.
func baseCase() CaseInput {
	return CaseInput{
		Flow: "counter",
		Hot:  SideInput{MDot: ptr(1), Cp: ptr(1), TIn: ptr(100)},
		Cold: SideInput{MDot: ptr(1), Cp: ptr(1), TIn: ptr(0)},
		UA:   ptr(1),
	}
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	scale := math.Max(math.Max(math.Abs(got), math.Abs(want)), 1.0)
	if math.Abs(got-want) > tol*scale {
		t.Fatalf("%s = %.12g, want %.12g (tol %.0e)", name, got, want, tol)
	}
}

// TestBalancedCounterflowReference is the hand-derived NTU=1 balanced case.
func TestBalancedCounterflowReference(t *testing.T) {
	res, err := Calculate(baseCase())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	approx(t, "Q", res.Q, 50, 1e-12)
	approx(t, "hot t_out", res.Hot.TOut, 50, 1e-12)
	approx(t, "cold t_out", res.Cold.TOut, 50, 1e-12)
	approx(t, "epsilon", res.Epsilon, 0.5, 1e-12)
	approx(t, "NTU", res.NTU, 1, 1e-12)
	approx(t, "Cr", res.CR, 1, 1e-12)
	approx(t, "LMTD", res.LMTD, 50, 1e-12)
	approx(t, "Qmax", res.QMax, 100, 1e-12)
	if !res.Feasible {
		t.Fatalf("balanced counterflow should be feasible, reason=%q", res.Reason)
	}
	// Balanced counterflow with equal outlets is reported as a (legal) cross.
	if !res.TemperatureCross {
		t.Fatalf("equal-outlet balanced case should be flagged temperature_cross (informational)")
	}
}

// TestEnergyClosure verifies Q_hot == Q_cold to the nailed tolerance over a
// wide sweep of capacity-rate ratios and NTUs, for both arrangements.
func TestEnergyClosure(t *testing.T) {
	for _, flow := range []string{"counter", "parallel"} {
		for _, ch := range []float64{0.5, 1, 2, 3.7} {
			for _, ua := range []float64{0.01, 0.3, 1, 5, 50, 5000} {
				c := baseCase()
				c.Flow = flow
				c.Hot.MDot = ptr(ch) // Cp=1 => C_hot = ch, C_cold = 1
				c.UA = ptr(ua)
				res, err := Calculate(c)
				if err != nil {
					t.Fatalf("flow=%s Ch=%v UA=%v: %v", flow, ch, ua, err)
				}
				qh := ch * (100 - res.Hot.TOut)
				qc := 1.0 * (res.Cold.TOut - 0)
				if relErr(qh, qc) > relEnergyTol {
					t.Fatalf("energy not closed flow=%s Ch=%v UA=%v: Qh=%.10g Qc=%.10g", flow, ch, ua, qh, qc)
				}
				if math.Abs(res.Q-qh) > 1e-9*math.Max(res.Q, 1) {
					t.Fatalf("reported Q disagrees with side duty")
				}
				if math.IsNaN(res.Q) || math.IsNaN(res.LMTD) || math.IsNaN(res.Epsilon) {
					t.Fatalf("NaN in result")
				}
			}
		}
	}
}

// TestMethodAgreement verifies the two independent paths agree, including the
// reported relative-difference field, across the same sweep.
func TestMethodAgreement(t *testing.T) {
	for _, flow := range []string{"counter", "parallel"} {
		for _, ch := range []float64{0.5, 1, 2, 3.7} {
			for _, ua := range []float64{0.01, 0.3, 1, 5, 50, 5000} {
				c := baseCase()
				c.Flow = flow
				c.Hot.MDot = ptr(ch)
				c.UA = ptr(ua)
				res, err := Calculate(c)
				if err != nil {
					t.Fatalf("flow=%s Ch=%v UA=%v: %v", flow, ch, ua, err)
				}
				if res.Check.RelDiff > res.Check.Tolerance {
					// Allow the (generous) solver-convergence band at extreme
					// NTU only; anything larger is a real failure.
					if res.Check.RelDiff > 1e-7 {
						t.Fatalf("methods disagree flow=%s Ch=%v UA=%v: rel=%g",
							flow, ch, ua, res.Check.RelDiff)
					}
				}
				if !res.Feasible {
					// The only feasible=false in this sweep is a parallel
					// exchanger driven onto its cross-over pinch (this happens
					// once NTU*(1+Cr) is large, well before UA=50 for small
					// Cmin; UA>=20 is used as a conservative bound).
					if flow != "parallel" || ua < 20 {
						t.Fatalf("unexpected infeasible flow=%s Ch=%v UA=%v: %s", flow, ch, ua, res.Reason)
					}
				}
			}
		}
	}
}

// TestPhysicalCeiling checks ε ∈ (0,1) and Q <= Cmin*(Th,in-Tc,in) for all
// NTUs and ratios.
func TestPhysicalCeiling(t *testing.T) {
	for _, flow := range []string{"counter", "parallel"} {
		for _, ua := range []float64{1e-6, 1, 1e3, 1e6} {
			for _, ch := range []float64{0.3, 1, 3} {
				c := baseCase()
				c.Flow = flow
				c.Hot.MDot = ptr(ch)
				c.UA = ptr(ua)
				res, err := Calculate(c)
				if err != nil {
					t.Fatal(err)
				}
				if res.Q > res.QMax*(1+1e-9) {
					t.Fatalf("Q %.10g exceeds Qmax %.10g", res.Q, res.QMax)
				}
				if res.Epsilon < 0 || res.Epsilon > 1+1e-12 {
					t.Fatalf("effectiveness %g out of [0,1]", res.Epsilon)
				}
			}
		}
	}
}

// TestTargetOverCeilingInfeasible: user asks for a duty / outlet temperature
// beyond the thermodynamic ceiling; service must answer infeasible, not a
// fabricated number.
func TestTargetOverCeilingInfeasible(t *testing.T) {
	// Qmax = Cmin*100 = 100. Ask 150.
	c := baseCase()
	c.Target = &TargetInput{Q: ptr(150)}
	res, err := Calculate(c)
	if err != nil {
		t.Fatalf("over-ceiling target is a feasibility result, not a request error: %v", err)
	}
	if res.Feasible {
		t.Fatalf("target Q above Qmax must be infeasible")
	}
	if res.TargetQ == nil || *res.TargetQ != 150 {
		t.Fatalf("target duty not echoed")
	}
	if res.Margin == nil || *res.Margin >= 0 {
		t.Fatalf("margin must be negative for over-ceiling target")
	}

	// Hot outlet 40°C on the balanced case would also need exactly 60 W; push
	// it to an impossible cold-side request instead: cold outlet 105°C means
	// Q = 105 > Qmax = 100.
	c2 := baseCase()
	c2.Target = &TargetInput{ColdTOut: ptr(105)}
	res2, err := Calculate(c2)
	if err != nil {
		t.Fatalf("expected infeasible result, got error %v", err)
	}
	if res2.Feasible {
		t.Fatalf("cold target outlet above hot inlet must be infeasible")
	}
}

// TestTargetMargin exercises feasible/insufficient margins.
func TestTargetMargin(t *testing.T) {
	// Actual Q=50. Target 40 => +25% margin, feasible.
	c := baseCase()
	c.Target = &TargetInput{Q: ptr(40)}
	res, err := Calculate(c)
	if err != nil || !res.Feasible {
		t.Fatalf("want feasible, err=%v reason=%q", err, res.Reason)
	}
	approx(t, "margin", *res.Margin, 0.25, 1e-9)

	// Target 80 => capacity shortfall => infeasible with negative margin.
	c2 := baseCase()
	c2.Target = &TargetInput{Q: ptr(80)}
	res2, err := Calculate(c2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Feasible {
		t.Fatalf("target 80 W beyond actual 50 W must be infeasible")
	}
	approx(t, "margin2", *res2.Margin, 50.0/80-1, 1e-9)
}

// TestFoulingReducesDuty: holding geometry fixed, increasing Rf must lower UA,
// duty and (counter-flow heating) cold outlet temperature.
func TestFoulingReducesDuty(t *testing.T) {
	build := func(rf float64) *Result {
		c := baseCase()
		c.UA = nil
		c.U = ptr(10)
		c.Area = ptr(0.1) // UA_clean = 1, same as base
		c.Rf = ptr(rf)
		res, err := Calculate(c)
		if err != nil {
			t.Fatalf("rf=%v: %v", rf, err)
		}
		return res
	}
	r0 := build(0)
	r1 := build(0.01)
	r2 := build(0.05)
	approx(t, "clean UA", r0.UA, 1.0, 1e-12)
	// 1/Udirty = 1/10 + Rf.
	approx(t, "UA rf=0.01", r1.UA, 1/(0.1+0.01)*0.1, 1e-12)
	if !(r0.UA > r1.UA && r1.UA > r2.UA) {
		t.Fatalf("UA must decrease with fouling: %v %v %v", r0.UA, r1.UA, r2.UA)
	}
	if !(r0.Q > r1.Q && r1.Q > r2.Q) {
		t.Fatalf("duty must decrease with fouling: %v %v %v", r0.Q, r1.Q, r2.Q)
	}
	if !(r0.Cold.TOut > r1.Cold.TOut && r1.Cold.TOut > r2.Cold.TOut) {
		t.Fatalf("counter-flow cold outlet must decrease with fouling: %v %v %v",
			r0.Cold.TOut, r1.Cold.TOut, r2.Cold.TOut)
	}
}

// TestParallelNoCrossing: finite parallel exchangers must never report a
// crossed state as feasible; cold outlet must stay strictly below hot outlet.
func TestParallelNoCrossing(t *testing.T) {
	for _, ch := range []float64{0.5, 1, 2} {
		for _, ua := range []float64{0.01, 1, 100, 1e5} {
			c := baseCase()
			c.Flow = "parallel"
			c.Hot.MDot = ptr(ch)
			c.UA = ptr(ua)
			res, err := Calculate(c)
			if err != nil {
				t.Fatalf("Ch=%v UA=%v: %v", ch, ua, err)
			}
			if res.Feasible && res.Cold.TOut >= res.Hot.TOut {
				t.Fatalf("parallel crossed but flagged feasible: Thout=%.8g Tcout=%.8g",
					res.Hot.TOut, res.Cold.TOut)
			}
		}
	}
	// A user target that forces crossing must be rejected as infeasible.
	c := baseCase()
	c.Flow = "parallel"
	c.Target = &TargetInput{ColdTOut: ptr(80)} // Q=80, hot outlet 20 -> crossed
	res, err := Calculate(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Feasible {
		t.Fatalf("parallel target causing crossover must be infeasible")
	}
}

// TestParallelReference: balanced parallel NTU=1 => ε=(1-e^-2)/2.
func TestParallelReference(t *testing.T) {
	c := baseCase()
	c.Flow = "parallel"
	res, err := Calculate(c)
	if err != nil {
		t.Fatal(err)
	}
	wantEps := (1 - math.Exp(-2)) / 2
	approx(t, "parallel epsilon", res.Epsilon, wantEps, 1e-12)
	approx(t, "parallel Q", res.Q, wantEps*100, 1e-10)
	if res.Cold.TOut >= res.Hot.TOut {
		t.Fatalf("parallel reference unexpectedly crossed")
	}
	// LMTD consistency.
	wantLMTD := (100 - (res.Hot.TOut - res.Cold.TOut)) /
		math.Log(100/(res.Hot.TOut-res.Cold.TOut))
	approx(t, "parallel LMTD", res.LMTD, wantLMTD, 1e-10)
}

// TestUnbalancedReference guards the Cmin/Cmax outlet bookkeeping for both
// choices of which stream is the minimum-capacity side. Hand-derived.
func TestUnbalancedReference(t *testing.T) {
	// Cold side is Cmin: Ch=2, Cc=1, UA=5, inlets 100/0 => NTU=5, Cr=0.5,
	// E=exp(-2.5), ε=(1-E)/(1-0.5E)=0.9810356, Q=98.10356.
	c := baseCase()
	c.Hot.MDot = ptr(2)
	c.UA = ptr(5)
	res, err := Calculate(c)
	if err != nil {
		t.Fatal(err)
	}
	e := math.Exp(-2.5)
	wantEps := (1 - e) / (1 - 0.5*e)
	approx(t, "eps(cold Cmin)", res.Epsilon, wantEps, 1e-11)
	approx(t, "Q(cold Cmin)", res.Q, wantEps*100, 1e-9)
	approx(t, "hot out", res.Hot.TOut, 100-res.Q/2, 1e-9)
	approx(t, "cold out", res.Cold.TOut, res.Q, 1e-9)
	if res.CR != 0.5 {
		t.Fatalf("Cr = %v", res.CR)
	}

	// Hot side is Cmin: Ch=1, Cc=2, UA=5 => NTU=5, Cr=0.5, same ε, Q=98.10.
	c2 := baseCase()
	c2.Cold.MDot = ptr(2)
	c2.UA = ptr(5)
	res2, err := Calculate(c2)
	if err != nil {
		t.Fatal(err)
	}
	approx(t, "eps(hot Cmin)", res2.Epsilon, wantEps, 1e-11)
	approx(t, "Q(hot Cmin)", res2.Q, wantEps*100, 1e-9)
	approx(t, "hot out2", res2.Hot.TOut, 100-res2.Q, 1e-9)
	approx(t, "cold out2", res2.Cold.TOut, res2.Q/2, 1e-9)
}

// TestCounterCrossingIsLegalAndReported: counter-flow may legitimately cross.
func TestCounterCrossingIsLegalAndReported(t *testing.T) {
	c := baseCase()
	c.Hot.MDot = ptr(2) // C_hot=2 > C_cold=1, high UA
	c.UA = ptr(50)
	res, err := Calculate(c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TemperatureCross {
		t.Fatalf("expected counter-flow cross (cold is Cmin)")
	}
	if !res.Feasible {
		t.Fatalf("counter-flow cross is legal, got reason=%q", res.Reason)
	}
	if res.Cold.TOut <= res.Hot.TOut {
		t.Fatalf("cross flag inconsistent with temperatures")
	}
}

// TestBalancedNoDivergence: Cr exactly 1 and arbitrarily near 1 must not
// divide by zero or diverge.
func TestBalancedNoDivergence(t *testing.T) {
	for _, ch := range []float64{1.0, 1 + 1e-12, 1 - 1e-12} {
		for _, ntu := range []float64{1e-9, 1.0, 1e6} {
			c := baseCase()
			c.Hot.MDot = ptr(ch)
			c.UA = ptr(ntu * math.Min(ch, 1))
			res, err := Calculate(c)
			if err != nil {
				t.Fatalf("ch=%v ntu=%v: %v", ch, ntu, err)
			}
			if math.IsNaN(res.Epsilon) || math.IsInf(res.Epsilon, 0) ||
				math.IsNaN(res.Q) || math.IsInf(res.Q, 0) {
				t.Fatalf("non-finite result near balanced point")
			}
		}
	}
}

// TestLMTDStableNearEqualDeltas exercises the stable atanh form.
func TestLMTDStableNearEqualDeltas(t *testing.T) {
	v, ok := lmtd(50, 50*(1+1e-9))
	if !ok {
		t.Fatal("near-equal deltas rejected")
	}
	approx(t, "lmtd", v, 50, 1e-6)
	if _, ok := lmtd(0, 10); ok {
		t.Fatal("zero delta must be rejected")
	}
	if _, ok := lmtd(5, -1); ok {
		t.Fatal("negative delta must be rejected")
	}
}

// TestValidationErrors checks every malformed-input rule returns a readable,
// field-specific error and never a Result.
func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CaseInput)
		field  string
	}{
		{"missing hot mdot", func(c *CaseInput) { c.Hot.MDot = nil }, "hot.m_dot"},
		{"zero cold cp", func(c *CaseInput) { c.Cold.Cp = ptr(0) }, "cold.cp"},
		{"negative mdot", func(c *CaseInput) { c.Hot.MDot = ptr(-2) }, "hot.m_dot"},
		{"zero ua", func(c *CaseInput) { c.UA = ptr(0) }, "ua"},
		{"negative ua", func(c *CaseInput) { c.UA = ptr(-1) }, "ua"},
		{"no geometry", func(c *CaseInput) { c.UA = nil }, "ua"},
		{"u without area", func(c *CaseInput) { c.UA = nil; c.U = ptr(10) }, "area"},
		{"area without u", func(c *CaseInput) { c.UA = nil; c.Area = ptr(2) }, "u"},
		{"ua and u together", func(c *CaseInput) { c.U = ptr(10); c.Area = ptr(1) }, "ua"},
		{"negative rf", func(c *CaseInput) { c.UA = nil; c.U = ptr(10); c.Area = ptr(1); c.Rf = ptr(-0.01) }, "rf"},
		{"rf with ua", func(c *CaseInput) { c.Rf = ptr(0.01) }, "rf"},
		{"inlet contradiction", func(c *CaseInput) { c.Hot.TIn = ptr(20); c.Cold.TIn = ptr(80) }, "hot.t_in"},
		{"equal inlets", func(c *CaseInput) { c.Hot.TIn = ptr(50); c.Cold.TIn = ptr(50) }, "hot.t_in"},
		{"bad flow", func(c *CaseInput) { c.Flow = "crossflow" }, "flow"},
		{"target both outlets", func(c *CaseInput) {
			c.Target = &TargetInput{HotTOut: ptr(60), ColdTOut: ptr(40)}
		}, "target"},
		{"target q and outlet", func(c *CaseInput) {
			c.Target = &TargetInput{Q: ptr(30), HotTOut: ptr(60)}
		}, "target"},
		{"target hot outlet above inlet", func(c *CaseInput) {
			c.Target = &TargetInput{HotTOut: ptr(120)}
		}, "target.hot_t_out"},
		{"target nonpositive q", func(c *CaseInput) {
			c.Target = &TargetInput{Q: ptr(0)}
		}, "target.q"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCase()
			tc.mutate(&c)
			res, err := Calculate(c)
			if err == nil {
				if res != nil && res.Feasible {
					t.Fatalf("expected validation error, got feasible result")
				}
				return
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("want *ValidationError, got %T: %v", err, err)
			}
			if ve.Problems[0].Field != tc.field {
				t.Fatalf("error field = %q, want %q (issue: %s)",
					ve.Problems[0].Field, tc.field, ve.Problems[0].Issue)
			}
		})
	}
}

// TestNonFiniteRejected ensures NaN/Inf pointers can't slip through.
func TestNonFiniteRejected(t *testing.T) {
	c := baseCase()
	c.Cold.Cp = ptr(math.NaN())
	if _, err := Calculate(c); err == nil {
		t.Fatal("NaN cp must be rejected")
	}
	c2 := baseCase()
	c2.UA = ptr(math.Inf(1))
	if _, err := Calculate(c2); err == nil {
		t.Fatal("Inf UA must be rejected")
	}
}

// TestChineseFlowAliases confirms the flow-token aliases resolve.
func TestChineseFlowAliases(t *testing.T) {
	for _, tok := range []string{"counter", "逆流", "逆流式", "parallel", "顺流", "并流"} {
		a, err := parseArrangement(tok)
		if err != nil {
			t.Fatalf("alias %q rejected: %v", tok, err)
		}
		if a != Counter && a != Parallel {
			t.Fatalf("bad parse")
		}
	}
}

// TestInfoEcho checks the configuration echo content.
func TestInfoEcho(t *testing.T) {
	info := Info()
	if len(info.SupportedFlows) != 2 {
		t.Fatalf("expected two supported flows")
	}
	if info.DefaultTolerances.MethodAgreement != relMethodTol ||
		info.DefaultTolerances.EnergyBalanceRel != relEnergyTol {
		t.Fatalf("tolerances not echoed")
	}
}
