// Package exchanger implements the pure thermohydraulic rating model for a
// recuperative (wall-type / 间壁式) heat exchanger.
//
// The model is driven through two independent numerical paths that must agree:
//
//   - the Effectiveness–NTU (ε-NTU) closed-form path, with the formula chosen
//     by flow arrangement;
//   - the Log-Mean Temperature Difference (LMTD, 对数平均温差) path, solved by
//     bracketed bisection on the energy-balance variable.
//
// Both paths share exactly one definition of the four endpoint temperatures,
// so agreement between them is a genuine cross-check rather than an identity.
package exchanger

import (
	"fmt"
	"math"
)

// Numerical tolerances that are "nailed down" for the rating service. They are
// deliberately tight: the governing equations are smooth and the solvers use
// stable forms, so there is no reason to hide a large error in the tolerance.
const (
	// relEnergyTol bounds |Q_hot - Q_cold| / Q for the computed operating point.
	relEnergyTol = 1e-9
	// relMethodTol bounds the disagreement between the ε-NTU and LMTD paths.
	relMethodTol = 1e-8
	// absEps guards relative comparisons when a quantity is exactly zero.
	absEps = 1e-12
)

// Arrangement is the flow arrangement (流型).
type Arrangement string

const (
	Counter  Arrangement = "counter"  // 逆流
	Parallel Arrangement = "parallel" // 顺流
)

// parseArrangement accepts the canonical English tokens and their Chinese
// aliases. The echoed configuration only reports the canonical tokens.
func parseArrangement(s string) (Arrangement, error) {
	switch s {
	case "counter", "counterflow", "counter_flow", "逆流", "逆流式":
		return Counter, nil
	case "parallel", "parallelflow", "parallel_flow", "cocurrent", "顺流", "顺流式", "并流":
		return Parallel, nil
	default:
		return "", fmt.Errorf("流型 %q 不支持,仅支持 counter(逆流) 或 parallel(顺流)", s)
	}
}

// SideInput is one fluid side. All temperatures are °C, m_dot is kg/s and
// cp is J/(kg·K); the choice of absolute temperature scale cancels out because
// only differences are used.
type SideInput struct {
	MDot *float64 `json:"m_dot"` // 质量流量 kg/s,必须 > 0
	Cp   *float64 `json:"cp"`    // 定压比热 J/(kg·K),必须 > 0
	TIn  *float64 `json:"t_in"`  // 进口温度 °C
}

// TargetInput optionally carries the user's design target. Every field is
// optional; at most one target outlet temperature may be given.
type TargetInput struct {
	HotTOut  *float64 `json:"hot_t_out"`  // 目标热侧出口温度 °C
	ColdTOut *float64 `json:"cold_t_out"` // 目标冷侧出口温度 °C
	Q        *float64 `json:"q"`          // 目标换热量 W
}

// CaseInput is one exchanger rating case. UA [W/K] may be given directly, or
// the clean overall coefficient U [W/(m²·K)] plus area A [m²], optionally with
// a total fouling resistance R_f [(m²·K)/W].
type CaseInput struct {
	Flow   string       `json:"flow"` // "counter" | "parallel"
	Hot    SideInput    `json:"hot"`
	Cold   SideInput    `json:"cold"`
	UA     *float64     `json:"ua,omitempty"`   // 直接给定热导 UA [W/K]
	U      *float64     `json:"u,omitempty"`    // 总传热系数(洁净) [W/(m²·K)]
	Area   *float64     `json:"area,omitempty"` // 传热面积 [m²]
	Rf     *float64     `json:"rf,omitempty"`   // 污垢热阻 [(m²·K)/W],>= 0
	Target *TargetInput `json:"target,omitempty"`
}

// SideResult reports one fluid side at the solved operating point.
type SideResult struct {
	TOut  float64 `json:"t_out"`  // 出口温度 °C
	Q     float64 `json:"q"`      // 该侧热量 W(热侧放热、冷侧吸热,均取正值)
	CRate float64 `json:"c_rate"` // 热容流率 C = m_dot*cp [W/K]
}

// MethodCheck records the two independent verification paths.
type MethodCheck struct {
	QByLMTD        float64 `json:"q_by_lmtd"`       // LMTD 迭代路径换热量 W
	QByEPSNTU      float64 `json:"q_by_eps_ntu"`    // ε-NTU 路径换热量 W
	RelDiff        float64 `json:"rel_diff"`        // 两路径换热量相对偏差
	Tolerance      float64 `json:"tolerance"`       // 允许相对误差
	EnergyResidual float64 `json:"energy_residual"` // |Qh-Qc|/Q
	Iterations     int     `json:"lmtd_iterations"` // LMTD 二分迭代次数
}

// Result is the full rating answer for one case.
type Result struct {
	Flow             string      `json:"flow"`
	Hot              SideResult  `json:"hot"`
	Cold             SideResult  `json:"cold"`
	Q                float64     `json:"q"`        // 实际换热量 W
	UA               float64     `json:"ua"`       // 用于计算的(含污垢)热导 W/K
	UAClean          float64     `json:"ua_clean"` // 洁净状态热导 W/K
	Epsilon          float64     `json:"epsilon"`  // 效能 ε = Q/Q_max
	NTU              float64     `json:"ntu"`      // 传热单元数 NTU = UA/C_min
	CR               float64     `json:"c_ratio"`  // 热容比 C_r = C_min/C_max ∈ (0,1]
	CMin             float64     `json:"c_min"`
	CMax             float64     `json:"c_max"`
	QMax             float64     `json:"q_max"`             // 物理上限 C_min*(Th,in-Tc,in)
	LMTD             float64     `json:"lmtd"`              // 对数平均温差(含端点定义)
	TemperatureCross bool        `json:"temperature_cross"` // 冷侧出口 >= 热侧出口
	Feasible         bool        `json:"feasible"`
	Reason           string      `json:"reason,omitempty"` // 不可行/非法原因
	TargetQ          *float64    `json:"target_q,omitempty"`
	Margin           *float64    `json:"margin,omitempty"` // 运行裕度 Q实际/Q目标 - 1
	Check            MethodCheck `json:"method_check"`
}

// FieldError identifies one invalid parameter. Index is 1-based and is only
// populated for batch requests (0 means a top-level / single-case field).
type FieldError struct {
	Index int    `json:"index,omitempty"` // 批量中的第几组(从 1 起)
	Field string `json:"field"`
	Issue string `json:"issue"`
}

// ValidationError carries one or more readable parameter errors.
type ValidationError struct {
	Problems []FieldError `json:"problems"`
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "参数不合法"
	}
	p := e.Problems[0]
	if p.Index > 0 {
		return fmt.Sprintf("第 %d 组参数 %s: %s", p.Index, p.Field, p.Issue)
	}
	return fmt.Sprintf("参数 %s: %s", p.Field, p.Issue)
}

// fieldErr builds a single-field validation error.
func fieldErr(field, issue string) error {
	return &ValidationError{Problems: []FieldError{{Field: field, Issue: issue}}}
}

// isFinite rejects NaN / ±Inf coming from a JSON parser that would otherwise
// accept them via hand-crafted tokens.
func isFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
