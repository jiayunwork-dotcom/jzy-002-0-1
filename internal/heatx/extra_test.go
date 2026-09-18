package heatx

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// TestNestedDuplicateKey 嵌套对象内的重复键必须定位到点路径,
// 批量场景还要带组号前缀。
func TestNestedDuplicateKey(t *testing.T) {
	h := Handler(DefaultServerConfig())
	body := `{"flow_type":"counter","hot":{"mass_flow":2,"mass_flow":3,"cp":2100,"t_in":80},
	          "cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}`
	status, out := doPOST(t, h, "/api/v1/calculate", body)
	if status != 400 {
		t.Fatalf("重复键应 400,实际 %d", status)
	}
	field := out["error"].(map[string]any)["fields"].([]any)[0].(map[string]any)["field"].(string)
	if field != "hot.mass_flow" {
		t.Fatalf("重复键路径应为 hot.mass_flow,实际 %q", field)
	}

	batch := `{"cases":[
	  {"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100},
	  {"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"cp":2000,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}
	]}`
	status, out = doPOST(t, h, "/api/v1/calculate/batch", batch)
	if status != 200 {
		t.Fatalf("批量应整体 200,实际 %d", status)
	}
	items := out["results"].([]any)
	e := items[1].(map[string]any)["error"].(map[string]any)
	field = e["fields"].([]any)[0].(map[string]any)["field"].(string)
	if !strings.Contains(field, "cases[1]") || !strings.Contains(field, "hot.cp") {
		t.Fatalf("应定位到 cases[1].hot.cp,实际 %q", field)
	}
}

// TestParallelHugeUANoCross 顺流 UA→∞ 时两侧出口趋近共同温度。
// 有限 UA 冷侧必须严格低于热侧;UA 大到双精度趋同时,服务应放行并以
// warning 标注"效能饱和",而不是把数值极限误判成物理交叉或直接报错。
func TestParallelHugeUANoCross(t *testing.T) {
	tol := DefaultTolerances()

	// 非饱和但很大:仍须严格不交叉
	cBig := &Case{
		FlowType: FlowParallel,
		Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
		Cold:     &SideInput{M: fp(3), Cp: fp(1400), TIn: fp(20)},
		UA:       fp(1e4),
	}
	r, err := Calculate(cBig, tol)
	if err != nil {
		t.Fatalf("UA=1e4 不应报错: %+v", err)
	}
	if r.ColdTOut >= r.HotTOut {
		t.Fatalf("非饱和顺流必须严格不交叉: Tho=%g Tco=%g", r.HotTOut, r.ColdTOut)
	}

	for _, ua := range []float64{1e6, 1e12, 1e15} {
		c := &Case{
			FlowType: FlowParallel,
			Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold:     &SideInput{M: fp(3), Cp: fp(1400), TIn: fp(20)}, // Ch=4200,Cc=4200
			UA:       fp(ua),
		}
		res, err := Calculate(c, tol)
		if err != nil {
			t.Fatalf("UA=%g 顺流极限不应报错: %+v", ua, err)
		}
		if math.IsNaN(res.HotTOut) || math.IsNaN(res.ColdTOut) {
			t.Fatalf("UA=%g 出口温度为 NaN", ua)
		}
		// 平衡顺流共同温度 = (Thi+Tci)/2 = 50
		approx(t, res.HotTOut, 50, 1e-6, "Tho→50@UA="+ftoa(ua))
		approx(t, res.ColdTOut, 50, 1e-6, "Tco→50@UA="+ftoa(ua))
		// 饱和必须有显式提示,不能静默给出"相等"
		if !hasWarningContaining(res.Warnings, "效能") {
			t.Fatalf("UA=%g 饱和极限应有警告,实际 %v", ua, res.Warnings)
		}
		// 关键物理量仍自洽
		approx(t, res.UAService*res.LMTD, res.Q, 1e-6, "UA·LMTD@UA="+ftoa(ua))
	}
}

func hasWarningContaining(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// TestSweepTwoMethodsAgree 宽参数域随机扫描:
// 两种方法在所有可行工况上必须互证一致,能量闭合,且顺流不交叉。
func TestSweepTwoMethodsAgree(t *testing.T) {
	tol := DefaultTolerances()
	rng := rand.New(rand.NewSource(20260918))
	for iter := 0; iter < 400; iter++ {
		flow := FlowCounter
		if iter%2 == 0 {
			flow = FlowParallel
		}
		ch := math.Exp(rng.Float64()*math.Log(1e5) + math.Log(10)) // 10..1e6
		cc := math.Exp(rng.Float64()*math.Log(1e5) + math.Log(10))
		ntu := math.Exp(rng.Float64()*math.Log(1e4) + math.Log(1e-3)) // 1e-3..10
		cmin := math.Min(ch, cc)
		ua := ntu * cmin
		thi := rng.Float64()*200 - 50 // -50..150
		dT := rng.Float64()*190 + 1   // 1..190
		c := &Case{
			FlowType: flow,
			Hot:      &SideInput{M: fp(ch / 4186), Cp: fp(4186), TIn: fp(thi + dT)},
			Cold:     &SideInput{M: fp(cc / 1005), Cp: fp(1005), TIn: fp(thi)},
			UA:       fp(ua),
		}
		res, err := Calculate(c, tol)
		if err != nil {
			t.Fatalf("iter=%d flow=%s Ch=%g Cc=%g NTU=%g 不应报错: %+v",
				iter, flow, ch, cc, ntu, err)
		}
		if res.Checks.MethodDiffRel > 1e-8 {
			t.Fatalf("iter=%d 两法偏差 %g", iter, res.Checks.MethodDiffRel)
		}
		if res.Checks.EnergyDiffRel > tol.EnergyRel {
			t.Fatalf("iter=%d 能量不闭合 %g", iter, res.Checks.EnergyDiffRel)
		}
		if flow == FlowParallel {
			if res.ColdTOut > res.HotTOut {
				t.Fatalf("iter=%d 顺流交叉 Tho=%g Tco=%g", iter, res.HotTOut, res.ColdTOut)
			}
			// 双精度饱和带内可能相等(有限 UA 物理上不交叉)
			if res.ColdTOut == res.HotTOut && res.Effectiveness < 1/(1+res.CR)*(1-1e-9) {
				t.Fatalf("iter=%d 非饱和顺流出口相等 Tho=Tco=%g", iter, res.HotTOut)
			}
		}
		if res.Q > res.QMax*(1+1e-12) || res.Effectiveness > 1+1e-12 {
			t.Fatalf("iter=%d 越过物理上限 Q=%g Qmax=%g eps=%g",
				iter, res.Q, res.QMax, res.Effectiveness)
		}
	}
}

// TestFoulingParallelMonotonicity 顺流下污垢增大同样必须降负荷,
// 且两侧出口都向进口方向回退。
func TestFoulingParallelMonotonicity(t *testing.T) {
	tol := DefaultTolerances()
	mk := func(rf float64) *Case {
		return &Case{
			FlowType: FlowParallel,
			Hot:      &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold:     &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)},
			Area:     fp(30), U: fp(120),
			Rf: fp(rf),
		}
	}
	var prevQ, prevTco, prevTho float64 = math.Inf(1), math.Inf(1), math.Inf(-1)
	for _, rf := range []float64{0, 5e-5, 3e-4, 1.5e-3, 5e-3} {
		res, err := mk(rf).Calc(t, tol)
		if err != nil {
			t.Fatalf("rf=%g: %+v", rf, err)
		}
		if res.Q >= prevQ {
			t.Fatalf("顺流污垢增大 Q 未降: %g -> %g", prevQ, res.Q)
		}
		if res.ColdTOut >= prevTco {
			t.Fatalf("顺流污垢增大冷侧出口未降: %g -> %g", prevTco, res.ColdTOut)
		}
		if res.HotTOut <= prevTho {
			t.Fatalf("顺流污垢增大热侧出口应回升: %g -> %g", prevTho, res.HotTOut)
		}
		prevQ, prevTco, prevTho = res.Q, res.ColdTOut, res.HotTOut
	}
}

// TestOverflowInputs 单字段有限但乘积溢出必须明确报错而非算出 NaN/Inf。
func TestOverflowInputs(t *testing.T) {
	tol := DefaultTolerances()
	huge := 1e200
	cases := []*Case{
		{FlowType: FlowCounter, Hot: &SideInput{M: fp(huge), Cp: fp(huge), TIn: fp(80)},
			Cold: &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)}, UA: fp(2100)},
		{FlowType: FlowCounter, Hot: &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold: &SideInput{M: fp(huge), Cp: fp(huge), TIn: fp(20)}, UA: fp(2100)},
		{FlowType: FlowCounter, Hot: &SideInput{M: fp(2), Cp: fp(2100), TIn: fp(80)},
			Cold: &SideInput{M: fp(1), Cp: fp(2100), TIn: fp(20)}, Area: fp(huge), U: fp(huge)},
	}
	for i, c := range cases {
		_, err := Calculate(c, tol)
		if err == nil {
			t.Fatalf("case %d: 溢出输入必须报错", i)
		}
		if err.Code != CodeInvalidValue {
			t.Fatalf("case %d: 错误码=%s 期望 INVALID_VALUE", i, err.Code)
		}
	}
}

// TestBatchTooLarge 超过组数上限应整体拒绝并给出可读说明。
func TestBatchTooLarge(t *testing.T) {
	h := Handler(DefaultServerConfig())
	var sb strings.Builder
	sb.WriteString(`{"cases":[`)
	for i := 0; i < MaxBatchCases+1; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}`)
	}
	sb.WriteString(`]}`)
	status, out := doPOST(t, h, "/api/v1/calculate/batch", sb.String())
	if status != 400 {
		t.Fatalf("超限应 400,实际 %d", status)
	}
	if out["error"].(map[string]any)["code"] != CodeBatchTooLarge {
		t.Fatalf("错误码应为 %s", CodeBatchTooLarge)
	}
}
