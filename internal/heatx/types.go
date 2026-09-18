// Package heatx 实现间壁式换热器(surface heat exchanger)的工况核算。
//
// 两条相互独立的核算路径:
//  1. 效能-传热单元数法(ε-NTU),按流型使用闭式公式;
//  2. 对数平均温差法(LMTD),用二分法迭代求解实际换热量。
//
// 两条路径共用同一套进出口温度定义与能量平衡,结果必须在配置容差内一致,
// 否则返回方法互证错误而不是给出看似正常的结果。
package heatx

// 流型取值(规范化后的内部表示)。
const (
	FlowCounter  = "counter"  // 逆流
	FlowParallel = "parallel" // 顺流
)

// flowAliases 允许的流型写法 -> 规范写法。
var flowAliases = map[string]string{
	"counter":         FlowCounter,
	"counterflow":     FlowCounter,
	"counter-current": FlowCounter,
	"parallel":        FlowParallel,
	"parallelflow":    FlowParallel,
	"concurrent":      FlowParallel,
	"co-current":      FlowParallel,
	"co_current":      FlowParallel,
	"cocurrent":       FlowParallel,
	"逆流":              FlowCounter,
	"顺流":              FlowParallel,
}

// Tolerances 核算容差与数值开关,可由调用方覆盖。
type Tolerances struct {
	// EnergyRel 能量闭合的相对容差:|Qh-Qc|/Qmax 必须不大于该值。
	EnergyRel float64 `json:"energy_closure_rel"`
	// MethodRel 两种方法互证的相对容差:|Q_ntu-Q_lmtd|/Q_ntu。
	MethodRel float64 `json:"method_agreement_rel"`
	// TargetEnergyRel 用户目标换热量两侧一致性相对容差。
	TargetEnergyRel float64 `json:"target_energy_rel"`
	// BalancedCr 判定热容流率平衡(Cr→1)的阈值,低于该偏差时走平衡解析分支。
	BalancedCr float64 `json:"balanced_cr_threshold"`
	// CrossRel 温度交叉判定的相对死区(按进口温差缩放),避免把浮点误差判成交叉。
	CrossRel float64 `json:"cross_rel_tol"`
}

// DefaultTolerances 返回默认容差配置(查询接口原样回显)。
func DefaultTolerances() Tolerances {
	return Tolerances{
		EnergyRel:       1e-9,
		MethodRel:       1e-9,
		TargetEnergyRel: 1e-6,
		BalancedCr:      1e-12,
		CrossRel:        1e-9,
	}
}

// SideInput 单侧流体输入。指针类型用于区分"未提供"与"显式给零值"。
type SideInput struct {
	M        *float64 `json:"mass_flow"`              // 质量流量 kg/s
	Cp       *float64 `json:"cp"`                     // 定压比热 J/(kg·K)
	TIn      *float64 `json:"t_in"`                   // 进口温度 ℃
	TOutGoal *float64 `json:"t_out_target,omitempty"` // 目标出口温度 ℃(可选)
}

// Case 单台换热器核算输入。
type Case struct {
	Name     string     `json:"name,omitempty"`      // 可选,仅用于批量结果辨识
	FlowType string     `json:"flow_type"`           // 流型:counter/parallel(见 flowAliases)
	Hot      *SideInput `json:"hot"`                 // 热侧
	Cold     *SideInput `json:"cold"`                // 冷侧
	UA       *float64   `json:"ua,omitempty"`        // 服务(含污垢)热导 W/K;与 area/u 二选一
	Area     *float64   `json:"area,omitempty"`      // 传热面积 m²
	U        *float64   `json:"u,omitempty"`         // 洁净总传热系数 W/(m²·K)
	Rf       *float64   `json:"fouling_r,omitempty"` // 附加总污垢热阻 m²·K/W(1/U_f = 1/U + Rf)
}

// Margin 运行裕度信息。
type Margin struct {
	ActualQ   float64 `json:"actual_q_w"`
	RequiredQ float64 `json:"required_q_w"`
	// DutyMargin 换热裕度 = Q_actual/Q_required - 1;<0 表示达不到目标。
	DutyMargin float64 `json:"duty_margin"`
	ActualUA   float64 `json:"actual_ua_w_per_k"`
	RequiredUA float64 `json:"required_ua_w_per_k"`
	// UAMargin 面积/热导裕度 = UA_actual/UA_required - 1。
	UAMargin float64 `json:"ua_margin"`
	// FoulingMargin 可承受的进一步结垢裕度 = UA_clean/UA_actual - 1。
	FoulingMargin float64 `json:"fouling_margin"`
}

// MethodCheck 两条独立路径的互证与能量闭合指标。
type MethodCheck struct {
	QNTU            float64 `json:"q_ntu_w"`              // ε-NTU 法换热量
	QLMTD           float64 `json:"q_lmtd_w"`             // LMTD 迭代法换热量(UA·LMTD)
	MethodDiffRel   float64 `json:"method_diff_rel"`      // 两法相对偏差
	MethodTolerance float64 `json:"method_tolerance_rel"` // 互证容差
	EnergyDiffAbs   float64 `json:"energy_diff_w"`        // |Qh-Qc|
	EnergyDiffRel   float64 `json:"energy_diff_rel"`      // |Qh-Qc|/Qmax
	EnergyTolerance float64 `json:"energy_tolerance_rel"` // 能量闭合容差
}

// Result 单台核算的完整结果。
type Result struct {
	Name             string      `json:"name,omitempty"`
	FlowType         string      `json:"flow_type"`
	HotTOut          float64     `json:"hot_t_out_c"`
	ColdTOut         float64     `json:"cold_t_out_c"`
	Q                float64     `json:"q_w"`
	QMax             float64     `json:"q_max_w"`
	Effectiveness    float64     `json:"effectiveness"`
	NTU              float64     `json:"ntu"`
	CR               float64     `json:"capacity_ratio"`
	LMTD             float64     `json:"lmtd_k"`
	UAService        float64     `json:"ua_w_per_k"`
	UAClean          *float64    `json:"ua_clean_w_per_k,omitempty"`
	Area             *float64    `json:"area_m2,omitempty"`
	UClean           *float64    `json:"u_clean_w_per_m2k,omitempty"`
	UService         *float64    `json:"u_service_w_per_m2k,omitempty"`
	FoulingR         *float64    `json:"fouling_r_m2k_per_w,omitempty"`
	TemperatureCross bool        `json:"temperature_cross"`
	Feasible         bool        `json:"feasible"`
	Margin           *Margin     `json:"margin"`
	Checks           MethodCheck `json:"checks"`
	Warnings         []string    `json:"warnings"`
}

// 解算后的内部参数包。
type solvedCase struct {
	flow     string
	ch, cc   float64 // 两侧热容流率 W/K
	cMin     float64
	cMax     float64
	cr       float64 // Cmin/Cmax
	thi, tci float64 // 进口温度 ℃
	dTin     float64 // Thi - Tci
	ua       float64 // 服务热导 W/K
	uaClean  float64 // 洁净热导(无面积直给 UA 时等于 ua)
	area     float64 // 0 表示直给 UA
	uClean   float64 // 0 表示直给 UA
	rf       float64
	ntu      float64

	// 目标出口温度(可选)
	thGoal, tcGoal float64
	hotGoalSet     bool
	coldGoalSet    bool
}
