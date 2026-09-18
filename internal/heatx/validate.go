package heatx

import (
	"math"
)

// validate 对单台输入做完整静态校验,返回按字段定位的错误。
func validate(c *Case, tol Tolerances) (*solvedCase, *APIError) {
	var errs []FieldError
	add := func(field, reason string) {
		errs = append(errs, FieldError{Field: field, Reason: reason})
	}
	need := func(p *float64, field string) (float64, bool) {
		if p == nil {
			add(field, "字段缺失或为 null,必须提供有限数值")
			return 0, false
		}
		if math.IsNaN(*p) || math.IsInf(*p, 0) {
			add(field, "必须是有限数值")
			return 0, false
		}
		return *p, true
	}
	positive := func(v float64, field, name string) bool {
		if v <= 0 {
			add(field, name+"必须为正数")
			return false
		}
		return true
	}

	if c == nil {
		return nil, fieldErr(CodeMissingField, "", "请求体必须是一个核算工况对象")
	}
	if c.Hot == nil {
		add("hot", "热侧参数整体缺失")
	}
	if c.Cold == nil {
		add("cold", "冷侧参数整体缺失")
	}
	if len(errs) > 0 {
		return nil, fieldErrs(CodeMissingField, "输入参数不合法,未执行核算", errs)
	}

	// 流型
	flow, ok := flowAliases[c.FlowType]
	if !ok {
		add("flow_type", "不支持的流型,当前仅支持 counter(逆流) 与 parallel(顺流)")
	}

	// 两侧物性
	mh, okMh := need(c.Hot.M, "hot.mass_flow")
	cph, okCph := need(c.Hot.Cp, "hot.cp")
	thi, okThi := need(c.Hot.TIn, "hot.t_in")
	mc, okMc := need(c.Cold.M, "cold.mass_flow")
	cpc, okCpc := need(c.Cold.Cp, "cold.cp")
	tci, okTci := need(c.Cold.TIn, "cold.t_in")
	if okMh {
		positive(mh, "hot.mass_flow", "热侧质量流量")
	}
	if okCph {
		positive(cph, "hot.cp", "热侧定压比热")
	}
	if okMc {
		positive(mc, "cold.mass_flow", "冷侧质量流量")
	}
	if okCpc {
		positive(cpc, "cold.cp", "冷侧定压比热")
	}
	if okThi && okTci && thi <= tci {
		add("hot.t_in/cold.t_in", "矛盾工况:热侧进口温度必须高于冷侧进口温度(本服务核算的是热侧加热冷侧)")
	}

	// 热导规格:UA 直给,或 area+u;两者不得混用/互相覆盖。
	var area, uClean, uaGiven, rf float64
	uaProvided := c.UA != nil
	areaProvided := c.Area != nil
	uProvided := c.U != nil
	rfProvided := c.Rf != nil

	if rfProvided {
		rf = *c.Rf
		if math.IsNaN(rf) || math.IsInf(rf, 0) {
			add("fouling_r", "污垢热阻必须是有限数值")
		} else if rf < 0 {
			add("fouling_r", "污垢热阻不得为负")
		}
	}
	switch {
	case uaProvided && (areaProvided || uProvided):
		add("ua/area/u", "热导规格互相覆盖:ua 与 area、u 只能二选一(直给 UA,或同时给 area 与 u)")
	case uaProvided:
		uaGiven, _ = need(c.UA, "ua")
		positive(uaGiven, "ua", "热导 UA")
		if rfProvided {
			add("fouling_r", "直给 ua 时已代表服务(含污垢)热导,不能再附加污垢热阻;请改用 area+u 规格")
		}
	case areaProvided || uProvided:
		if !areaProvided || !uProvided {
			add("area/u", "area 与 u 必须成对提供")
		} else {
			area, _ = need(c.Area, "area")
			uClean, _ = need(c.U, "u")
			positive(area, "area", "传热面积")
			positive(uClean, "u", "总传热系数")
		}
	default:
		add("ua", "必须提供热导:直接给 ua,或同时给 area 与 u")
	}

	// 目标出口温度:可双侧给也可不给;给一侧即按能量平衡推出另一侧所需热量。
	var hotGoal, coldGoal float64
	var hotGoalSet, coldGoalSet bool
	if c.Hot.TOutGoal != nil {
		hotGoal = *c.Hot.TOutGoal
		if math.IsNaN(hotGoal) || math.IsInf(hotGoal, 0) {
			add("hot.t_out_target", "目标出口温度必须是有限数值")
		} else {
			hotGoalSet = true
			if okThi && hotGoal >= thi {
				add("hot.t_out_target", "热侧目标出口温度必须低于热侧进口温度")
			}
			// hotGoal <= cold.t_in(要求达到/超过热力学极限)不在此拦截,
			// 由 evaluateMargin 按 Q>=Qmax 返回"目标不可行"。
		}
	}
	if c.Cold.TOutGoal != nil {
		coldGoal = *c.Cold.TOutGoal
		if math.IsNaN(coldGoal) || math.IsInf(coldGoal, 0) {
			add("cold.t_out_target", "目标出口温度必须是有限数值")
		} else {
			coldGoalSet = true
			if okTci && coldGoal <= tci {
				add("cold.t_out_target", "冷侧目标出口温度必须高于冷侧进口温度")
			}
			// coldGoal >= hot.t_in(热力学极限)留给 evaluateMargin 返回不可行。
		}
	}
	// 顺流目标温度交叉、目标超过物理上限等物理不可行情形,
	// 统一在 evaluateMargin 中按 TARGET_INFEASIBLE(422) 判定,
	// 这里只做纯静态的字段/类型/正数校验。

	if len(errs) > 0 {
		return nil, fieldErrs(CodeInvalidValue, "输入参数不合法,未执行核算", errs)
	}

	// 组装解算包
	s := &solvedCase{
		flow:   flow,
		ch:     mh * cph,
		cc:     mc * cpc,
		thi:    thi,
		tci:    tci,
		dTin:   thi - tci,
		area:   area,
		uClean: uClean,
		rf:     rf,
	}
	s.cMin, s.cMax = s.ch, s.cc
	if s.cc < s.ch {
		s.cMin, s.cMax = s.cc, s.ch
	}
	oneMinus := math.Abs(1 - s.cMin/s.cMax)
	if oneMinus <= tol.BalancedCr {
		s.cr = 1 // 热容流率平衡,后续走 Cr=1 闭式分支
	} else {
		s.cr = s.cMin / s.cMax
	}
	if uaProvided {
		s.ua = uaGiven
		s.uaClean = uaGiven
	} else {
		uService := 1 / (1/uClean + rf) // 1/U_f = 1/U + Rf
		s.ua = uService * area
		s.uaClean = uClean * area
	}
	s.ntu = s.ua / s.cMin

	// 单个字段虽为有限正数,乘积仍可能溢出为 Inf/NaN(天文数字输入),
	// 在此统一拦截,避免后续闭式解/迭代产出非物理结果。
	if math.IsInf(s.ch, 0) || math.IsNaN(s.ch) {
		return nil, fieldErr(CodeInvalidValue, "hot.mass_flow/hot.cp",
			"热侧热容流率 mass_flow·cp 超出数值范围")
	}
	if math.IsInf(s.cc, 0) || math.IsNaN(s.cc) {
		return nil, fieldErr(CodeInvalidValue, "cold.mass_flow/cold.cp",
			"冷侧热容流率 mass_flow·cp 超出数值范围")
	}
	if math.IsInf(s.ua, 0) || math.IsNaN(s.ua) {
		return nil, fieldErr(CodeInvalidValue, "ua/area/u",
			"热导(UA 或 U·A)超出数值范围")
	}
	if math.IsInf(s.ntu, 0) || math.IsNaN(s.ntu) {
		return nil, fieldErr(CodeInvalidValue, "ua/area/u",
			"传热单元数 NTU=UA/Cmin 超出数值范围")
	}

	// 目标热量(两侧若都给则在 Calculate 中做闭合一致性校验)
	if hotGoalSet {
		s.thGoal = hotGoal
		s.hotGoalSet = true
	}
	if coldGoalSet {
		s.tcGoal = coldGoal
		s.coldGoalSet = true
	}
	return s, nil
}
