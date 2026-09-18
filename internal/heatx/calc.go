package heatx

import (
	"math"
	"strconv"
)

// Calculate 执行单台换热器工况核算。
// 输入非法时返回 *APIError(调用方据此映射 HTTP 状态码),不返回任何结果。
func Calculate(c *Case, tol Tolerances) (*Result, *APIError) {
	s, verr := validate(c, tol)
	if verr != nil {
		return nil, verr
	}

	// ---- 路径一:ε-NTU 闭式公式 -------------------------------------------
	eps := effectiveness(s.flow, s.ntu, s.cr)
	if math.IsNaN(eps) || math.IsInf(eps, 0) || eps < 0 || eps > 1 {
		return nil, &APIError{
			Code: CodeMethodMismatch,
			Msg:  "效能计算得到非物理值,请检查 UA 与热容流率输入",
		}
	}
	qMax := s.cMin * s.dTin
	qNTU := eps * qMax

	// 共用同一套进出口温度定义(能量平衡):
	//   Tho = Thi - q/Ch,  Tco = Tci + q/Cc
	tho := s.thi - qNTU/s.ch
	tco := s.tci + qNTU/s.cc

	// ---- 路径二:LMTD 法独立迭代 -------------------------------------------
	qLMTD, lmtdVal := solveByLMTD(s, tol)

	// ---- 两法互证 ---------------------------------------------------------
	methodDiff := relErr(qNTU, qLMTD)
	if methodDiff > tol.MethodRel {
		return nil, &APIError{
			Code: CodeMethodMismatch,
			Msg:  "ε-NTU 法与 LMTD 迭代法结果超过互证容差,拒绝输出可能错误的结果",
			Fields: []FieldError{{
				Field: "q",
				Reason: ftoa(qNTU) + " W(ε-NTU) vs " + ftoa(qLMTD) +
					" W(LMTD),相对偏差 " + ftoa(methodDiff) + " > 容差 " + ftoa(tol.MethodRel),
			}},
		}
	}

	// ---- 能量闭合(由同一温度定义推出,仍做钉死容差校验) -------------------
	qFromHot := s.ch * (s.thi - tho)
	qFromCold := s.cc * (tco - s.tci)
	energyDiff := math.Abs(qFromHot - qFromCold)
	energyRel := energyDiff / qMax
	if energyRel > tol.EnergyRel {
		return nil, &APIError{
			Code: CodeMethodMismatch,
			Msg:  "能量不闭合:热侧放热量与冷侧吸热量之差超过容差",
			Fields: []FieldError{{
				Field: "q",
				Reason: ftoa(qFromHot) + " W vs " + ftoa(qFromCold) +
					" W,相对偏差 " + ftoa(energyRel) + " > 容差 " + ftoa(tol.EnergyRel),
			}},
		}
	}

	// ---- 物理上限(不可越过 Cmin·(Thi-Tci)) -------------------------------
	if qNTU > qMax*(1+tol.EnergyRel) {
		return nil, fieldErr(CodeMethodMismatch, "q",
			"换热量超过物理上限 Cmin·(Thi-Tci),结果不可信")
	}

	// ---- 温度交叉判定 -----------------------------------------------------
	crossGap := tco - tho
	cross := crossGap > tol.CrossRel*s.dTin
	warnings := []string{}
	if s.flow == FlowCounter && cross {
		warnings = append(warnings,
			"逆流工况出现温度交叉(冷侧出口高于热侧出口):单壳程逆流可发生,多管程/管壳式需用温差修正系数 F 另行核算")
	}
	if s.flow == FlowParallel {
		// 有限 UA 下顺流闭式解恒有 Tco<Tho。仅当达到效能饱和极限
		// (NTU 极大)时,真实的小正温差会在双精度出口温度上舍入为 0:
		// 这是数值极限而非物理交叉,放行并给出提示;其余任何 Tco>=Tho
		// 都属于非物理结果。
		if crossGap >= -tol.CrossRel*s.dTin && !isSaturated(s.flow, eps, s.cr) {
			return nil, &APIError{
				Code: CodeInfeasible,
				Msg:  "顺流工况非法:冷侧出口温度不得高于或等于热侧出口温度",
				Fields: []FieldError{{
					Field:  "cold.t_out/hot.t_out",
					Reason: "Tho=" + ftoa(tho) + " ℃, Tco=" + ftoa(tco) + " ℃",
				}},
			}
		}
		if isSaturated(s.flow, eps, s.cr) && crossGap >= -tol.CrossRel*s.dTin {
			warnings = append(warnings,
				"顺流工况已达效能极限 εmax=1/(1+Cr):两侧出口温度在双精度下趋同(物理上仍为冷侧略低),继续增大 UA 不再提升换热")
		}
	}

	// ---- 组装结果 ---------------------------------------------------------
	res := &Result{
		FlowType:         s.flow,
		HotTOut:          tho,
		ColdTOut:         tco,
		Q:                qNTU,
		QMax:             qMax,
		Effectiveness:    eps,
		NTU:              s.ntu,
		CR:               s.cr,
		LMTD:             lmtdVal,
		UAService:        s.ua,
		TemperatureCross: cross,
		Feasible:         true,
		Warnings:         warnings,
		Checks: MethodCheck{
			QNTU:            qNTU,
			QLMTD:           qLMTD,
			MethodDiffRel:   methodDiff,
			MethodTolerance: tol.MethodRel,
			EnergyDiffAbs:   energyDiff,
			EnergyDiffRel:   energyRel,
			EnergyTolerance: tol.EnergyRel,
		},
	}
	if c.Name != "" {
		res.Name = c.Name
	}
	if s.area > 0 {
		uService := s.ua / s.area
		area, uC, rfVal := s.area, s.uClean, s.rf
		res.Area = &area
		res.UClean = &uC
		res.UService = &uService
		res.FoulingR = &rfVal
		res.UAClean = &s.uaClean
	}

	// ---- 目标工况裕度(给了任一目标出口温度才计算) ------------------------
	if s.hotGoalSet || s.coldGoalSet {
		margin, merr := evaluateMargin(s, tol, qNTU, s.ua)
		if merr != nil {
			return nil, merr
		}
		res.Margin = margin
		if margin.DutyMargin < 0 {
			res.Warnings = append(res.Warnings,
				"运行裕度为负:该换热器在当前工况下达不到给定目标出口温度(物理可行但面积/热导不足)")
		}
	}
	return res, nil
}

// evaluateMargin 按目标出口温度核算所需热量/所需热导与运行裕度,
// 并校验目标是否越过物理上限、顺流是否交叉。
func evaluateMargin(s *solvedCase, tol Tolerances, qActual, uaActual float64) (*Margin, *APIError) {
	var qReq float64
	switch {
	case s.hotGoalSet && s.coldGoalSet:
		qh := s.ch * (s.thi - s.thGoal)
		qc := s.cc * (s.tcGoal - s.tci)
		if qh <= 0 || qc <= 0 {
			return nil, fieldErr(CodeInfeasible, "hot.t_out_target/cold.t_out_target",
				"目标出口温度对应的换热量必须为正")
		}
		if relErr(qh, qc) > tol.TargetEnergyRel {
			return nil, &APIError{
				Code: CodeContradiction,
				Msg:  "目标工况能量不闭合:两侧目标出口温度对应的换热量不一致",
				Fields: []FieldError{{
					Field: "hot.t_out_target/cold.t_out_target",
					Reason: ftoa(qh) + " W(热侧) vs " + ftoa(qc) + " W(冷侧),相对偏差 " +
						ftoa(relErr(qh, qc)) + " > 容差 " + ftoa(tol.TargetEnergyRel),
				}},
			}
		}
		qReq = (qh + qc) / 2
	case s.hotGoalSet:
		qReq = s.ch * (s.thi - s.thGoal)
	case s.coldGoalSet:
		qReq = s.cc * (s.tcGoal - s.tci)
	}

	qMax := s.cMin * s.dTin
	if qReq <= 0 {
		return nil, fieldErr(CodeInfeasible, "t_out_target", "目标换热量必须为正")
	}
	if qReq >= qMax*(1+tol.EnergyRel) {
		return nil, &APIError{
			Code: CodeInfeasible,
			Msg:  "目标工况不可行:所需换热量达到或超过物理上限 Cmin·(Thi-Tci)",
			Fields: []FieldError{{
				Field:  "t_out_target",
				Reason: "Q_required=" + ftoa(qReq) + " W >= Q_max=" + ftoa(qMax) + " W",
			}},
		}
	}

	epsReq := qReq / qMax
	ntuReq, err := requiredNTU(s.flow, epsReq, s.cr)
	if err != nil {
		return nil, &APIError{
			Code: CodeInfeasible,
			Msg:  "目标工况不可行:该流型下目标效能无法达到",
			Fields: []FieldError{{
				Field: "t_out_target",
				Reason: "ε_required=" + ftoa(epsReq) + ",Cr=" + ftoa(s.cr) +
					",流型=" + s.flow,
			}},
		}
	}
	uaReq := ntuReq * s.cMin

	// 顺流目标交叉的最终防线(闭式反解给出的极限点也在此拦截)。
	thoGoal := s.thi - qReq/s.ch
	tcoGoal := s.tci + qReq/s.cc
	if s.flow == FlowParallel && tcoGoal >= thoGoal-tol.CrossRel*s.dTin {
		return nil, &APIError{
			Code: CodeInfeasible,
			Msg:  "顺流目标工况非法:冷侧出口温度不得高于或等于热侧出口温度",
			Fields: []FieldError{{
				Field:  "t_out_target",
				Reason: "Tho=" + ftoa(thoGoal) + " ℃, Tco=" + ftoa(tcoGoal) + " ℃",
			}},
		}
	}

	return &Margin{
		ActualQ:       qActual,
		RequiredQ:     qReq,
		DutyMargin:    qActual/qReq - 1,
		ActualUA:      uaActual,
		RequiredUA:    uaReq,
		UAMargin:      uaActual/uaReq - 1,
		FoulingMargin: s.uaClean/uaActual - 1,
	}, nil
}

// relErr 以二者量级为分母的对称相对误差;两者都为 0 时返回 0。
func relErr(a, b float64) float64 {
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return 0
	}
	return math.Abs(a-b) / scale
}

func ftoa(v float64) string {
	return strconv.FormatFloat(v, 'g', 12, 64)
}
