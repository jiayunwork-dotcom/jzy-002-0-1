package heatx

import (
	"math"
	"testing"
)

// ---- 测试辅助 -------------------------------------------------------------

func fp(v float64) *float64 { return &v }

// baseCounterCase 标准逆流工况:
// Ch=4200 W/K,Cc=2100 W/K,Thi=80℃,Tci=20℃,UA=2100 W/K
// → Cmin=2100,Cr=0.5,NTU=1。
func baseCounterCase() *Case {
	return &Case{
		Name:     "counter-base",
		FlowType: FlowCounter,
		Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
		Cold:     &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)},
		UA:       fp(2100),
	}
}

func approx(t *testing.T, got, want, tol float64, name string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.10g, 期望 %.10g, 偏差 %.3e 超过容差 %.3e",
			name, got, want, math.Abs(got-want), tol)
	}
}

// refCounterEps 测试内独立抄写的逆流闭式公式,防止流型分支选错。
func refCounterEps(ntu, cr float64) float64 {
	if math.Abs(1-cr) < 1e-12 {
		return ntu / (1 + ntu)
	}
	e := math.Exp(-ntu * (1 - cr))
	return (1 - e) / (1 - cr*e)
}

func refParallelEps(ntu, cr float64) float64 {
	return (1 - math.Exp(-ntu*(1+cr))) / (1 + cr)
}

// ---- 1. 闭式解、两法互证、能量闭合 ---------------------------------------

func TestCounterFlow_Basic(t *testing.T) {
	tol := DefaultTolerances()
	res, err := Calculate(baseCounterCase(), tol)
	if err != nil {
		t.Fatalf("正常工况不应报错: %+v", err)
	}
	// 手算锚点: e^-0.5=0.60653066; ε=(1-e)/(1-0.5e)=0.39346934/0.69673467≈0.5647334
	wantEps := refCounterEps(1, 0.5)
	approx(t, res.Effectiveness, wantEps, 1e-12, "effectiveness")
	approx(t, res.Effectiveness, 0.5647334, 1e-6, "effectiveness(手算锚点)")
	approx(t, res.NTU, 1, 1e-12, "ntu")
	approx(t, res.CR, 0.5, 1e-12, "cr")

	// 能量闭合
	qHot := 4200.0 * (80 - res.HotTOut)
	qCold := 2100.0 * (res.ColdTOut - 20)
	approx(t, qHot, res.Q, 1e-7, "Qh")
	approx(t, qCold, res.Q, 1e-7, "Qc")
	if res.Checks.EnergyDiffRel > tol.EnergyRel {
		t.Fatalf("能量闭合相对偏差 %g > %g", res.Checks.EnergyDiffRel, tol.EnergyRel)
	}
	// 两条独立路径互证
	if res.Checks.MethodDiffRel > tol.MethodRel {
		t.Fatalf("ε-NTU 与 LMTD 互证偏差 %g > %g", res.Checks.MethodDiffRel, tol.MethodRel)
	}
	// LMTD 与换热量自洽:Q = UA·LMTD
	approx(t, res.UAService*res.LMTD, res.Q, 1e-6, "UA·LMTD")
	// 物理上限
	if res.Q >= res.QMax {
		t.Fatalf("Q=%g 必须严格小于 Qmax=%g", res.Q, res.QMax)
	}
	// 热侧出口 > 冷侧出口(本工况无交叉)
	if res.ColdTOut >= res.HotTOut {
		t.Fatalf("本工况不应交叉: Tho=%g Tco=%g", res.HotTOut, res.ColdTOut)
	}
	if res.TemperatureCross {
		t.Fatal("本工况 temperature_cross 应为 false")
	}
}

func TestParallelFlow_Basic(t *testing.T) {
	tol := DefaultTolerances()
	// 平衡热容顺流:Ch=Cc=4200,UA=4200,NTU=1,Cr=1
	c := &Case{
		FlowType: FlowParallel,
		Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
		Cold:     &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(20)},
		UA:       fp(4200),
	}
	res, err := Calculate(c, tol)
	if err != nil {
		t.Fatalf("正常顺流工况不应报错: %+v", err)
	}
	// 手算锚点: ε=(1-e^-2)/2≈0.4323324
	wantEps := refParallelEps(1, 1)
	approx(t, res.Effectiveness, wantEps, 1e-12, "effectiveness")
	approx(t, res.Effectiveness, 0.4323324, 1e-6, "effectiveness(手算锚点)")
	approx(t, res.CR, 1, 1e-12, "cr(平衡)")
	if res.Checks.MethodDiffRel > tol.MethodRel {
		t.Fatalf("两法互证偏差 %g", res.Checks.MethodDiffRel)
	}
	approx(t, res.UAService*res.LMTD, res.Q, 1e-6, "UA·LMTD")
	// 顺流绝不允许出口交叉
	if res.ColdTOut >= res.HotTOut {
		t.Fatalf("顺流出口交叉: Tho=%g Tco=%g", res.HotTOut, res.ColdTOut)
	}
}

// ---- 2. Cr→1 单独处理,无除零/发散 ----------------------------------------

func TestBalancedCapacityRatio(t *testing.T) {
	tol := DefaultTolerances()
	for _, ua := range []float64{1e-9, 1, 100, 4200, 1e9} {
		c := &Case{
			FlowType: FlowCounter,
			Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(100)},
			Cold:     &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(20)},
			UA:       fp(ua),
		}
		res, err := Calculate(c, tol)
		if err != nil {
			t.Fatalf("UA=%g 平衡热容工况报错: %+v", ua, err)
		}
		if math.IsNaN(res.Q) || math.IsInf(res.Q, 0) {
			t.Fatalf("UA=%g 时 Q 非有限值", ua)
		}
		// Cr=1 逆流: ε=NTU/(1+NTU)
		ntu := ua / 4200
		approx(t, res.Effectiveness, ntu/(1+ntu), 1e-10, "ε@UA="+ftoa(ua))
		if res.Checks.MethodDiffRel > 1e-8 {
			t.Fatalf("UA=%g 两法偏差 %g", ua, res.Checks.MethodDiffRel)
		}
	}
}

func TestNearBalancedCapacityRatio(t *testing.T) {
	tol := DefaultTolerances()
	// Cr 极接近 1 但不完全相等:闭式不平衡分支不应在 1-Cr 处数值发散
	c := &Case{
		FlowType: FlowCounter,
		Hot:      &SideInput{M: fp(1.0), Cp: fp(1000), TIn: fp(90)},
		Cold:     &SideInput{M: fp(1.0), Cp: fp(1000 + 1e-9), TIn: fp(10)},
		UA:       fp(500),
	}
	res, err := Calculate(c, tol)
	if err != nil {
		t.Fatalf("近平衡工况不应报错: %+v", err)
	}
	if math.IsNaN(res.Q) {
		t.Fatal("近平衡 Cr 下 Q 为 NaN")
	}
	// 平衡近似 ε=NTU/(1+NTU),NTU≈0.5 → ≈0.3333,两种分支应几乎相同
	approx(t, res.Effectiveness, 0.333333, 1e-5, "ε near balanced")
}

// ---- 3. 逆流温度交叉可发生且被标记 ---------------------------------------

func TestCounterFlow_TemperatureCrossFlagged(t *testing.T) {
	tol := DefaultTolerances()
	// 冷侧为小热容、UA 很大:冷侧出口可超过热侧出口(逆流允许)
	c := &Case{
		FlowType: FlowCounter,
		Hot:      &SideInput{M: fp(10), Cp: fp(4200), TIn: fp(90)}, // Ch=42000
		Cold:     &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(10)},  // Cc=2100
		UA:       fp(20000),                                        // NTU≈9.52
	}
	res, err := Calculate(c, tol)
	if err != nil {
		t.Fatalf("逆流交叉工况本身可行,不应报错: %+v", err)
	}
	if !res.TemperatureCross {
		t.Fatalf("应标记温度交叉: Tho=%g Tco=%g", res.HotTOut, res.ColdTOut)
	}
	if res.ColdTOut <= res.HotTOut {
		t.Fatalf("标记与数值矛盾: Tho=%g Tco=%g", res.HotTOut, res.ColdTOut)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("逆流交叉应附带工程警告")
	}
}

// ---- 4. 污垢:增大 Rf 必须使 Q 下降、逆流冷侧出口降低 ---------------------

func TestFoulingMonotonicity(t *testing.T) {
	tol := DefaultTolerances()
	mk := func(rf float64) *Case {
		return &Case{
			FlowType: FlowCounter,
			Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold:     &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)},
			Area:     fp(50), U: fp(100), // UA_clean=5000
			Rf: fp(rf), // 1/U_f = 0.01 + rf
		}
	}
	var prevQ, prevTco float64 = math.Inf(1), math.Inf(1)
	for _, rf := range []float64{0, 1e-5, 1e-4, 5e-4, 2e-3} {
		res, err := mk(rf).Calc(t, tol)
		if err != nil {
			t.Fatalf("rf=%g 不应报错: %+v", rf, err)
		}
		if res.Q >= prevQ {
			t.Fatalf("污垢增大后 Q 未严格下降: rf=%g Q=%g,上一 Q=%g", rf, res.Q, prevQ)
		}
		if res.ColdTOut >= prevTco {
			t.Fatalf("逆流加热工况污垢增大后冷侧出口未降低: rf=%g Tco=%g(上一 %g)",
				rf, res.ColdTOut, prevTco)
		}
		// 1/U_service = 1/U + Rf
		wantUs := 1 / (0.01 + rf)
		approx(t, *res.UService, wantUs, 1e-9, "U_service@rf="+ftoa(rf))
		prevQ, prevTco = res.Q, res.ColdTOut
	}
}

func TestFoulingUAIdentity(t *testing.T) {
	tol := DefaultTolerances()
	// 同一服务 UA:area/u/rf 组合与直给 UA 结果必须一致
	c1 := &Case{
		FlowType: FlowCounter,
		Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
		Cold:     &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)},
		Area:     fp(50), U: fp(100), Rf: fp(0.01), // Uf=50,UA=2500
	}
	c2 := baseCounterCase()
	c2.UA = fp(2500)
	r1, e1 := Calculate(c1, tol)
	r2, e2 := Calculate(c2, tol)
	if e1 != nil || e2 != nil {
		t.Fatalf("不应报错: %v %v", e1, e2)
	}
	approx(t, r1.Q, r2.Q, 1e-9, "Q(area/u/rf) vs Q(ua)")
	approx(t, r1.HotTOut, r2.HotTOut, 1e-12, "Tho")
	approx(t, r1.ColdTOut, r2.ColdTOut, 1e-12, "Tco")
}

// ---- 5. 物理上限:目标 Q>=Cmin·ΔTin 必须返回不可行 -----------------------

func TestTargetAbovePhysicalLimit(t *testing.T) {
	tol := DefaultTolerances()
	c := baseCounterCase()
	// Qmax=2100*60=126000 W。冷侧目标 85℃ 需要 Q=2100*65=136500>Qmax。
	c.Cold.TOutGoal = fp(85)
	_, err := Calculate(c, tol)
	if err == nil {
		t.Fatal("目标换热量超过物理上限必须返回错误")
	}
	if err.Code != CodeInfeasible {
		t.Fatalf("错误码应为 %s,实际 %s", CodeInfeasible, err.Code)
	}

	// 恰好等于上限(冷侧出口=热侧进口)同样不可行
	c2 := baseCounterCase()
	c2.Cold.TOutGoal = fp(80)
	_, err = Calculate(c2, tol)
	if err == nil || err.Code != CodeInfeasible {
		t.Fatalf("目标恰达 Qmax 也应判不可行, got %+v", err)
	}
}

func TestActualQNeverExceedsPhysicalLimit(t *testing.T) {
	tol := DefaultTolerances()
	// UA 取到天文数字,效能趋限但 Q 永不越过 Qmax
	c := baseCounterCase()
	c.UA = fp(1e15)
	res, err := Calculate(c, tol)
	if err != nil {
		t.Fatalf("大 UA 极限工况不应报错: %+v", err)
	}
	if res.Q > res.QMax*(1+1e-12) {
		t.Fatalf("Q=%g 越过 Qmax=%g", res.Q, res.QMax)
	}
	if res.Effectiveness > 1 {
		t.Fatalf("效能 %g 超过 1", res.Effectiveness)
	}
}

// ---- 6. 顺流目标出口交叉必须拒绝 -----------------------------------------

func TestParallelTargetCrossRejected(t *testing.T) {
	tol := DefaultTolerances()
	c := &Case{
		FlowType: FlowParallel,
		Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
		Cold:     &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(20)},
		UA:       fp(1e6), // 设备极大,看似"什么都能做到"
	}
	// 平衡顺流极限:两侧出口趋同于 50℃;要求冷侧 55℃ 必然交叉
	c.Cold.TOutGoal = fp(55)
	c.Hot.TOutGoal = fp(45)
	_, err := Calculate(c, tol)
	if err == nil {
		t.Fatal("顺流目标交叉必须拒绝")
	}
	if err.Code != CodeInfeasible && err.Code != CodeInvalidValue {
		t.Fatalf("应返回不可行/非法类错误,实际 %s", err.Code)
	}
}

// ---- 7. 运行裕度正负与闭式反解 -------------------------------------------

func TestMarginSufficientAndInsufficient(t *testing.T) {
	tol := DefaultTolerances()

	// 目标温和(冷侧 40℃,需 Q=42000 W),UA=2100 设备实际给 ~71.2 kW → 正裕度
	c := baseCounterCase()
	c.Cold.TOutGoal = fp(40)
	res, err := Calculate(c, tol)
	if err != nil {
		t.Fatalf("可行目标不应报错: %+v", err)
	}
	if res.Margin == nil {
		t.Fatal("给了目标出口必须返回裕度")
	}
	if res.Margin.DutyMargin <= 0 {
		t.Fatalf("本工况裕度应为正,实际 %g", res.Margin.DutyMargin)
	}
	if res.Margin.UAMargin <= 0 {
		t.Fatalf("热导裕度应为正,实际 %g", res.Margin.UAMargin)
	}
	// 用反算 NTU 闭式式复核所需 UA
	epsReq := 42000.0 / 126000
	ntuReq, _ := requiredNTU(FlowCounter, epsReq, 0.5)
	approx(t, res.Margin.RequiredUA, ntuReq*2100, 1e-8, "UA_required")

	// 目标过高:冷侧 70℃(需 105 kW),小 UA 设备达不到 → 负裕度但工况仍出结果
	c2 := baseCounterCase()
	c2.UA = fp(300)
	c2.Cold.TOutGoal = fp(70)
	res2, err := Calculate(c2, tol)
	if err != nil {
		t.Fatalf("目标物理可达但面积不足时应返回结果+负裕度警告,不应整体报错: %+v", err)
	}
	if res2.Margin.DutyMargin >= 0 {
		t.Fatalf("裕度应为负,实际 %g", res2.Margin.DutyMargin)
	}
	if len(res2.Warnings) == 0 {
		t.Fatal("负裕度应有警告")
	}
}

func TestTargetEnergyMismatchRejected(t *testing.T) {
	tol := DefaultTolerances()
	c := baseCounterCase()
	// 两侧目标各自推出的 Q 不一致
	c.Hot.TOutGoal = fp(70)  // Qh=42000
	c.Cold.TOutGoal = fp(50) // Qc=63000
	_, err := Calculate(c, tol)
	if err == nil {
		t.Fatal("两侧目标能量不闭合必须拒绝")
	}
	if err.Code != CodeContradiction {
		t.Fatalf("错误码应为 %s,实际 %s", CodeContradiction, err.Code)
	}
}

// ---- 8. 输入异常:缺字段/非数值/矛盾/热导规格 -----------------------------

func TestInputValidation(t *testing.T) {
	tol := DefaultTolerances()

	tests := []struct {
		name      string
		mutate    func(c *Case)
		wantCode  string
		wantField string // 错误字段应包含的子串
	}{
		{"缺热侧", func(c *Case) { c.Hot = nil }, CodeMissingField, "hot"},
		{"质量流量为零", func(c *Case) { c.Hot.M = fp(0) }, CodeInvalidValue, "mass_flow"},
		{"比热为负", func(c *Case) { c.Cold.Cp = fp(-1) }, CodeInvalidValue, "cp"},
		{"UA为负", func(c *Case) { c.UA = fp(-10) }, CodeInvalidValue, "ua"},
		{"缺UA与面积", func(c *Case) { c.UA = nil }, CodeInvalidValue, "ua"},
		{"只给面积不给U", func(c *Case) { c.UA = nil; c.Area = fp(10) }, CodeInvalidValue, "area/u"},
		{"UA与面积互相覆盖", func(c *Case) { c.Area = fp(10); c.U = fp(100) }, CodeInvalidValue, "ua"},
		{"直给UA还加污垢", func(c *Case) { c.Rf = fp(1e-4) }, CodeInvalidValue, "fouling_r"},
		{"污垢为负", func(c *Case) { c.UA = nil; c.Area = fp(10); c.U = fp(100); c.Rf = fp(-1) }, CodeInvalidValue, "fouling_r"},
		{"热侧进口低于冷侧", func(c *Case) { c.Hot.TIn = fp(15) }, CodeInvalidValue, "t_in"},
		{"非法流型", func(c *Case) { c.FlowType = "crossflow" }, CodeInvalidValue, "flow_type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCounterCase()
			tc.mutate(c)
			_, err := Calculate(c, tol)
			if err == nil {
				t.Fatalf("%s: 必须返回错误", tc.name)
			}
			if err.Code != tc.wantCode {
				t.Fatalf("%s: 错误码=%s,期望 %s", tc.name, err.Code, tc.wantCode)
			}
			if len(err.Fields) == 0 {
				t.Fatalf("%s: 必须指明字段", tc.name)
			}
			if !contains(err.Fields[0].Field, tc.wantField) {
				t.Fatalf("%s: 字段=%q,应包含 %q", tc.name, err.Fields[0].Field, tc.wantField)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---- 9. 流型别名(含中文) -------------------------------------------------

func TestFlowAliases(t *testing.T) {
	tol := DefaultTolerances()
	for _, alias := range []string{"counterflow", "counter-current", "逆流"} {
		c := baseCounterCase()
		c.FlowType = alias
		res, err := Calculate(c, tol)
		if err != nil {
			t.Fatalf("别名 %s 应被接受: %+v", alias, err)
		}
		if res.FlowType != FlowCounter {
			t.Fatalf("别名 %s 未规范化为 counter", alias)
		}
	}
	for _, alias := range []string{"concurrent", "co-current", "顺流"} {
		c := &Case{
			FlowType: alias,
			Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold:     &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(20)},
			UA:       fp(4200),
		}
		res, err := Calculate(c, tol)
		if err != nil {
			t.Fatalf("别名 %s 应被接受: %+v", alias, err)
		}
		if res.FlowType != FlowParallel {
			t.Fatalf("别名 %s 未规范化为 parallel", alias)
		}
	}
}

// Calc 让构造更顺手的测试辅助。
func (c *Case) Calc(t *testing.T, tol Tolerances) (*Result, *APIError) {
	t.Helper()
	return Calculate(c, tol)
}
