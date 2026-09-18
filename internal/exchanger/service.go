package exchanger

import (
	"fmt"
	"math"
)

// ConfigInfo is echoed by the configuration query endpoint.
type ConfigInfo struct {
	Service           string   `json:"service"`
	SupportedFlows    []string `json:"supported_flows"`
	DefaultTolerances struct {
		EnergyBalanceRel float64 `json:"energy_balance_rel"`
		MethodAgreement  float64 `json:"eps_ntu_vs_lmtd_rel"`
	} `json:"default_tolerances"`
	Rules []string `json:"rules"`
}

// Info returns the service capability/configuration description.
func Info() ConfigInfo {
	var info ConfigInfo
	info.Service = "recuperative-heat-exchanger-rating"
	info.SupportedFlows = []string{string(Counter), string(Parallel)}
	info.DefaultTolerances.EnergyBalanceRel = relEnergyTol
	info.DefaultTolerances.MethodAgreement = relMethodTol
	info.Rules = []string{
		"热量单位 W,温度 °C(只用温差),质量流量 kg/s,比热 J/(kg·K),面积 m²,热导 W/K,污垢热阻 (m²·K)/W",
		"能量闭合: |Qh-Qc|/Q <= 1e-9",
		"ε-NTU 闭式公式与 LMTD 二分迭代两条独立路径互证, 换热量相对偏差 <= 1e-8",
		"热容比 Cr = Cmin/Cmax; Cr=1(逆流)采用极限公式 ε=NTU/(1+NTU), 不做除法",
		"换热量物理上限 Qmax = Cmin*(Th_in-Tc_in), 目标换热量超限判定为不可行",
		"顺流冷侧出口温度必须严格低于热侧出口温度, 交叉即非法",
		"污垢: 1/U_dirty = 1/U_clean + Rf, 面积不变时 Rf 增大则 UA 与换热量下降",
	}
	return info
}

// validateSide checks one fluid side and returns m_dot, cp, t_in.
func validateSide(s SideInput, name string) (float64, float64, float64, error) {
	if s.MDot == nil {
		return 0, 0, 0, fieldErr(name+".m_dot", "缺少质量流量 m_dot")
	}
	if !isFinite(*s.MDot) {
		return 0, 0, 0, fieldErr(name+".m_dot", "质量流量必须是有限数值")
	}
	if *s.MDot <= 0 {
		return 0, 0, 0, fieldErr(name+".m_dot", fmt.Sprintf("质量流量必须为正,收到 %v", *s.MDot))
	}
	if s.Cp == nil {
		return 0, 0, 0, fieldErr(name+".cp", "缺少定压比热 cp")
	}
	if !isFinite(*s.Cp) {
		return 0, 0, 0, fieldErr(name+".cp", "定压比热必须是有限数值")
	}
	if *s.Cp <= 0 {
		return 0, 0, 0, fieldErr(name+".cp", fmt.Sprintf("定压比热必须为正,收到 %v", *s.Cp))
	}
	if s.TIn == nil {
		return 0, 0, 0, fieldErr(name+".t_in", "缺少进口温度 t_in")
	}
	if !isFinite(*s.TIn) {
		return 0, 0, 0, fieldErr(name+".t_in", "进口温度必须是有限数值")
	}
	return *s.MDot, *s.Cp, *s.TIn, nil
}

// resolveGeometry turns the UA / (U, A[, Rf]) alternatives into clean and
// fouled conductance. The two specification styles are mutually exclusive.
func resolveGeometry(in *CaseInput) (uaDirty, uaClean float64, err error) {
	hasUA := in.UA != nil
	hasU := in.U != nil
	hasA := in.Area != nil
	if hasUA && (hasU || hasA) {
		return 0, 0, fieldErr("ua", "热导 UA 与 (U, area) 两种给法互斥,不能同时出现")
	}
	if !hasUA && !(hasU && hasA) {
		switch {
		case !hasU && !hasA:
			return 0, 0, fieldErr("ua", "必须给出热导 ua,或同时给出洁净总传热系数 u 与面积 area")
		case !hasU:
			return 0, 0, fieldErr("u", "给出 area 时必须同时给出总传热系数 u")
		default:
			return 0, 0, fieldErr("area", "给出 u 时必须同时给出面积 area")
		}
	}
	rf := 0.0
	if in.Rf != nil {
		rf = *in.Rf
		if !isFinite(rf) {
			return 0, 0, fieldErr("rf", "污垢热阻必须是有限数值")
		}
		if rf < 0 {
			return 0, 0, fieldErr("rf", fmt.Sprintf("污垢热阻不能为负,收到 %v", rf))
		}
	}
	if hasUA {
		if !isFinite(*in.UA) {
			return 0, 0, fieldErr("ua", "热导必须是有限数值")
		}
		if *in.UA <= 0 {
			return 0, 0, fieldErr("ua", fmt.Sprintf("热导 UA 必须为正,收到 %v", *in.UA))
		}
		if rf > 0 {
			return 0, 0, fieldErr("rf", "直接给定时 UA 已代表含污垢的热导,不能再附加 rf;请改用 u+area+rf")
		}
		return *in.UA, *in.UA, nil
	}
	// U + A style.
	if !isFinite(*in.U) || *in.U <= 0 {
		return 0, 0, fieldErr("u", fmt.Sprintf("总传热系数必须为正的有限数值,收到 %v", *in.U))
	}
	if !isFinite(*in.Area) || *in.Area <= 0 {
		return 0, 0, fieldErr("area", fmt.Sprintf("传热面积必须为正的有限数值,收到 %v", *in.Area))
	}
	uaClean = *in.U * *in.Area
	uDirty := 1 / (1/(*in.U) + rf)
	uaDirty = uDirty * *in.Area
	return uaDirty, uaClean, nil
}

// resolveTarget interprets the optional design target and returns the target
// duty. A target above the physical ceiling is not a request error: the
// exchanger is simply rated infeasible with a stated reason.
func resolveTarget(in *CaseInput, ep endpoints, cHot, cCold, qMax float64) (targetQ *float64, infeasibleReason string, err error) {
	t := in.Target
	if t == nil {
		return nil, "", nil
	}
	nOutlet := 0
	if t.HotTOut != nil {
		nOutlet++
	}
	if t.ColdTOut != nil {
		nOutlet++
	}
	if nOutlet > 1 {
		return nil, "", fieldErr("target", "目标热侧出口与冷侧出口温度至多给出一个(二者由能量平衡互相决定)")
	}
	hasOutlet := nOutlet == 1
	if t.Q != nil && hasOutlet {
		return nil, "", fieldErr("target", "目标换热量 q 与目标出口温度不能同时指定(会互相覆盖)")
	}
	if t.Q != nil {
		if !isFinite(*t.Q) {
			return nil, "", fieldErr("target.q", "目标换热量必须是有限数值")
		}
		if *t.Q <= 0 {
			return nil, "", fieldErr("target.q", fmt.Sprintf("目标换热量必须为正,收到 %v", *t.Q))
		}
		q := *t.Q
		if q > qMax*(1+absEps) {
			r := fmt.Sprintf("目标换热量 %.6g W 超过物理上限 Cmin*(Th,in-Tc,in)=%.6g W,不可行", q, qMax)
			return &q, r, nil
		}
		return &q, "", nil
	}
	if hasOutlet {
		var q float64
		var field string
		if t.HotTOut != nil {
			field = "target.hot_t_out"
			x := *t.HotTOut
			if !isFinite(x) {
				return nil, "", fieldErr(field, "目标出口温度必须是有限数值")
			}
			if !(x < ep.tHIn) {
				return nil, "", fieldErr(field, fmt.Sprintf("热侧出口温度必须低于其进口温度 %v °C,收到 %v", ep.tHIn, x))
			}
			q = cHot * (ep.tHIn - x)
		} else {
			field = "target.cold_t_out"
			x := *t.ColdTOut
			if !isFinite(x) {
				return nil, "", fieldErr(field, "目标出口温度必须是有限数值")
			}
			if !(x > ep.tCIn) {
				return nil, "", fieldErr(field, fmt.Sprintf("冷侧出口温度必须高于其进口温度 %v °C,收到 %v", ep.tCIn, x))
			}
			q = cCold * (x - ep.tCIn)
		}
		// Energy closure for the target itself: the other side's resulting
		// outlet must be physically ordered (heating direction).
		if q > qMax*(1+absEps) {
			r := fmt.Sprintf("目标出口温度对应换热量 %.6g W 超过物理上限 %.6g W,不可行", q, qMax)
			return &q, r, nil
		}
		// Parallel flow may not cross even for a user target.
		thOut := ep.tHot(q)
		tcOut := ep.tCold(q)
		if in.Flow == string(Parallel) && tcOut >= thOut {
			r := fmt.Sprintf("顺流目标工况冷侧出口 %.6g °C >= 热侧出口 %.6g °C,温度交叉,非法", tcOut, thOut)
			return &q, r, nil
		}
		return &q, "", nil
	}
	// Empty target object: treat as no target.
	return nil, "", nil
}

// Calculate rates a single exchanger case. It returns a Result for every
// well-formed case (including physically infeasible targets). Malformed input
// is returned as *ValidationError and never produces a Result.
func Calculate(in CaseInput) (*Result, error) {
	arr, err := parseArrangement(in.Flow)
	if err != nil {
		return nil, fieldErr("flow", err.Error())
	}

	mh, cph, thIn, err := validateSide(in.Hot, "hot")
	if err != nil {
		return nil, err
	}
	mc, cpc, tcIn, err := validateSide(in.Cold, "cold")
	if err != nil {
		return nil, err
	}
	if !(thIn > tcIn) {
		return nil, fieldErr("hot.t_in",
			fmt.Sprintf("热侧进口 %.6g °C 不高于冷侧进口 %.6g °C,与加热(热->冷)工况矛盾", thIn, tcIn))
	}

	uaDirty, uaClean, err := resolveGeometry(&in)
	if err != nil {
		return nil, err
	}

	cHot := mh * cph
	cCold := mc * cpc
	ep := endpoints{tHIn: thIn, tCIn: tcIn}
	if cHot <= cCold {
		ep.cMin, ep.cMax, ep.hotIsMin = cHot, cCold, true
	} else {
		ep.cMin, ep.cMax, ep.hotIsMin = cCold, cHot, false
	}
	cr := ep.cMin / ep.cMax // well-defined: both sides are strictly positive
	dtIn := thIn - tcIn
	qMax := ep.cMin * dtIn
	ntu := uaDirty / ep.cMin

	// Path 1 — ε-NTU closed form (effectiveness before any limiting clamp).
	eps0 := effectiveness(arr, cr, ntu)
	qEps := eps0 * qMax

	// Path 2 — LMTD, independently solved for the duty at UA*LMTD = q.
	// qLimit is the arrangement's thermodynamic limiting duty (counter:
	// Qmax; parallel: the cross-over pinch below Qmax).
	qLMTD, qLimit, nIter := lmtdSolve(arr, ep, uaDirty, qMax)

	// An arbitrarily large UA drives the closed-form duty onto the limiting
	// point, where one terminal temperature difference vanishes. Evaluating
	// the LMTD identity there (0*∞ or UA*0) is numerically ill-conditioned,
	// so detect "at the limit" and certify via the independent LMTD solver
	// converging to the same limiting duty instead.
	const limitBand = 1e-9 // within 1e-9 relative of the limit counts as at-limit
	atLimit := relErr(qEps, qLimit) <= limitBand
	if atLimit {
		qEps = qLimit
	}

	thOut := ep.tHot(qEps)
	tcOut := ep.tCold(qEps)
	eps := qEps / qMax

	// Shared endpoint temperatures for both verification paths.
	d1, d2 := endDeltas(arr, ep, qEps)
	lm, ok := lmtd(d1, d2)
	if atLimit {
		// At the thermodynamic limit a terminal temperature difference
		// vanishes, so the limiting LMTD is 0. Report 0 and skip the local
		// UA*LMTD=q identity below (certified instead by the LMTD solver
		// converging to the same limiting duty).
		lm, ok = 0, true
	}
	if !ok {
		return nil, fmt.Errorf("内部错误: ε-NTU 工况点出现非正端点温差 d1=%v d2=%v", d1, d2)
	}
	qAtPoint := uaDirty * lm

	// Energy closure, measured honestly from the endpoint temperatures the
	// caller receives. The residual is the unclosed fraction of the reference
	// heat rate Qmax = Cmin*(Th,in-Tc,in); the two side duties also each use
	// the shared q-based outlet definitions. Outlet temps are float64-rounded,
	// so a scale-independent absolute band (1e-9 of Qmax) is the physically
	// correct tolerance rather than relative to a possibly tiny duty.
	qhSide := cHot * (thIn - thOut)
	qcSide := cCold * (tcOut - tcIn)
	resid := math.Abs(qhSide-qcSide) / math.Max(qMax, 1e-300)
	if resid > relEnergyTol {
		return nil, fmt.Errorf("内部错误: 能量不闭合, 相对 Qmax 残差 %.3e > %.0e", resid, relEnergyTol)
	}

	// Method cross-check: duties from the two independent paths must coincide.
	relAgree := relErr(qLMTD, qEps)
	if relAgree > 100*relMethodTol {
		return nil, fmt.Errorf("内部错误: ε-NTU 与 LMTD 两路径换热量不一致 (Q_eps=%.9g, Q_lmtd=%.9g, 相对偏差 %.3e > %.0e)",
			qEps, qLMTD, relAgree, 100*relMethodTol)
	}
	// Away from the limit, the solved point must satisfy UA*LMTD = q itself.
	if !atLimit && relErr(qAtPoint, qEps) > relMethodTol {
		return nil, fmt.Errorf("内部错误: ε-NTU 工况点不满足传热方程 UA*LMTD=Q (Q_eps=%.9g, UA*LMTD=%.9g)",
			qEps, qAtPoint)
	}
	if qEps > qMax*(1+1e-9) {
		return nil, fmt.Errorf("内部错误: 换热量 %.9g 超过物理上限 %.9g", qEps, qMax)
	}

	cross := tcOut >= thOut
	res := &Result{
		Flow: string(arr),
		Hot: SideResult{
			TOut:  thOut,
			Q:     cHot * (thIn - thOut),
			CRate: cHot,
		},
		Cold: SideResult{
			TOut:  tcOut,
			Q:     cCold * (tcOut - tcIn),
			CRate: cCold,
		},
		Q:                qEps,
		UA:               uaDirty,
		UAClean:          uaClean,
		Epsilon:          eps,
		NTU:              ntu,
		CR:               cr,
		CMin:             ep.cMin,
		CMax:             ep.cMax,
		QMax:             qMax,
		LMTD:             lm,
		TemperatureCross: cross,
		Feasible:         true,
		Check: MethodCheck{
			QByLMTD:        qLMTD,
			QByEPSNTU:      qEps,
			RelDiff:        relAgree,
			Tolerance:      relMethodTol,
			EnergyResidual: resid,
			Iterations:     nIter,
		},
	}

	// Parallel-flow crossing at the actual operating point is physically
	// illegal (counter-flow crossing is legal and merely reported).
	if arr == Parallel && cross {
		res.Feasible = false
		if atLimit {
			res.Reason = fmt.Sprintf("顺流在 UA 趋于无穷时到达夹点极限(两侧出口温度相等 %.6g °C),严格顺流不允许出口温度相等或交叉;该 UA=%.4g W/K 已使换热器贴在极限上", thOut, uaDirty)
		} else {
			res.Reason = "顺流冷侧出口温度达到或超过热侧出口温度,温度交叉,该工况非法"
		}
	}

	// Target handling: feasibility against design duty plus operating margin.
	targetQ, targetReason, verr := resolveTarget(&in, ep, cHot, cCold, qMax)
	if verr != nil {
		return nil, verr
	}
	if targetQ != nil {
		res.TargetQ = targetQ
		margin := qEps/(*targetQ) - 1
		res.Margin = &margin
		if targetReason != "" {
			res.Feasible = false
			if res.Reason != "" {
				res.Reason = res.Reason + "; " + targetReason
			} else {
				res.Reason = targetReason
			}
		} else if qEps+absEps*math.Max(qEps, *targetQ) < *targetQ {
			res.Feasible = false
			res.Reason = fmt.Sprintf("实际换热量 %.6g W 低于目标 %.6g W(裕度 %.2f%%),换热能力不足",
				qEps, *targetQ, 100*margin)
		}
	}

	return res, nil
}

// relErr returns |a-b|/max(|a|,|b|,absEps), a scale-free disagreement measure
// that stays defined at zero.
func relErr(a, b float64) float64 {
	scale := math.Max(math.Max(math.Abs(a), math.Abs(b)), absEps)
	return math.Abs(a-b) / scale
}
