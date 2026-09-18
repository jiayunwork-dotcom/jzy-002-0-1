package heatx

import (
	"errors"
	"math"
)

// effectiveness 返回给定流型的闭式效能 ε。
//
// 逆流:
//
//	Cr<1: ε = (1-exp(-NTU(1-Cr))) / (1-Cr·exp(-NTU(1-Cr)))
//	Cr=1: ε = NTU/(1+NTU)
//
// 顺流:
//
//	ε = (1-exp(-NTU(1+Cr))) / (1+Cr)
//
// 对 Cr→1 的逆流分支使用 x/(1-x 重写)避免相消发散。
func effectiveness(flow string, ntu, cr float64) float64 {
	switch flow {
	case FlowCounter:
		if cr >= 1 {
			return ntu / (1 + ntu)
		}
		x := ntu * (1 - cr)
		if x < 1e-4 {
			// x 很小时用级数: (1-e^-x)/(1-cr·e^-x)
			// 分子分母同时乘 e^x -> (e^x-1)/(e^x-cr)
			ex := math.Expm1(x) // e^x-1
			return ex / (1 + ex - cr)
		}
		e := math.Exp(-x)
		return (1 - e) / (1 - cr*e)
	case FlowParallel:
		return -math.Expm1(-ntu*(1+cr)) / (1 + cr)
	default:
		// Validate 已拦截,这里属于编程错误。
		return math.NaN()
	}
}

// requiredNTU 由目标效能反算所需 NTU(逆问题)。
// 返回 ErrInfeasible 表示该流型/热容比下目标效能物理上达不到。
var errInfeasible = errors.New("target effectiveness unreachable")

func requiredNTU(flow string, eps, cr float64) (float64, error) {
	if eps <= 0 {
		return 0, nil
	}
	if eps >= 1 {
		return 0, errInfeasible
	}
	switch flow {
	case FlowCounter:
		if cr >= 1 { // 平衡: NTU = ε/(1-ε)
			return eps / (1 - eps), nil
		}
		// 不平衡: NTU = ln((1-ε·Cr)/(1-ε)) / (1-Cr)
		num := math.Log((1 - eps*cr) / (1 - eps))
		return num / (1 - cr), nil
	case FlowParallel:
		// 顺流上限: ε_max = 1/(1+Cr)
		eMax := 1 / (1 + cr)
		if eps >= eMax {
			return 0, errInfeasible
		}
		// NTU = -ln(1-ε(1+Cr)) / (1+Cr)
		return -math.Log1p(-eps*(1+cr)) / (1 + cr), nil
	default:
		return 0, errors.New("unsupported flow type")
	}
}

// lmtd 对数平均温差。d1、d2 为两端温差(取 abs 后按大端 d1、小端 d2 传入)。
// d2<=0 表示该端已发生温度交叉,LMTD 无定义,返回 (0,false)。
func lmtd(d1, d2 float64) (float64, bool) {
	if d1 < 0 || d2 < 0 || math.IsNaN(d1) || math.IsNaN(d2) {
		return 0, false
	}
	if d2 == 0 {
		// 端点温差为零是极限可行点(如顺流恰好不交叉),LMTD→0。
		return 0, true
	}
	if d1 == d2 { // 两端温差相等,LMTD 就是算术平均
		return d1, true
	}
	// 用 x=Δ/d2(d2>0)与 log1p 求值: LMTD = Δ/ln(1+x),全程不先算比值
	// r=d2/d1,避免 x 很小时比值相减把有效数字吃光。
	delta := d1 - d2
	x := delta / d2
	if x < 1e-8 {
		// x→0 时 LMTD→算术平均,修正项为 O(x²)·d(相对误差 < x²/12≈1e-17),
		// 直接返回均值,数值上严格位于 [d2,d1] 内。
		return (d1 + d2) / 2, true
	}
	return delta / math.Log1p(x), true
}

// endDeltas 返回给定换热量 q 下换热器两端温差(大端、小端)。
// 两侧出口温度由同一套能量平衡定义:
//
//	Tho = Thi - q/Ch,  Tco = Tci + q/Cc
func endDeltas(s *solvedCase, q float64) (big, small float64, ok bool) {
	tho := s.thi - q/s.ch
	tco := s.tci + q/s.cc
	var dA, dB float64
	if s.flow == FlowCounter {
		// 端1: 热进-冷出;端2: 热出-冷进
		dA, dB = s.thi-tco, tho-s.tci
	} else {
		// 顺流端1: 热进-冷进;端2: 热出-冷出
		dA, dB = s.thi-s.tci, tho-tco
	}
	if dA < 0 || dB < 0 {
		return 0, 0, false
	}
	if dA >= dB {
		return dA, dB, true
	}
	return dB, dA, true
}

// solveByLMTD 用对数平均温差法独立迭代求解实际换热量 q,
// 求解 f(q) = q - UA·LMTD(q) = 0(二分法,保证全局收敛、不依赖初值)。
//
// f 关于 q 严格单调(LMTD 随 q 单调下降),只需夹住符号相反的两端:
//   - f(0) = -UA·ΔTin < 0;
//   - 逆流可行域到 Qmax=Cmin·ΔTin(小热容端温差归零,L=0,f>0);
//   - 顺流出口端温差 Tho-Tco = ΔTin-q(1/Ch+1/Cc) 先归零,
//     可行域上界 qFeas=ΔTin/(1/Ch+1/Cc) < Qmax,必须用它作右端,
//     否则 q>qFeas 时端温差为负,f 被人为置负会把二分引向错误一侧。
func solveByLMTD(s *solvedCase, tol Tolerances) (q float64, lmtdAtQ float64) {
	hi := feasibleLimit(s)

	f := func(q float64) float64 {
		d1, d2, ok := endDeltas(s, q)
		if !ok {
			// 理论上不会落在可行域外;防御性外推为大负值
			return -s.cMin * s.dTin
		}
		l, _ := lmtd(d1, d2)
		return q - s.ua*l
	}

	// 可行域边界处端温差为零,直接相减可能被舍成微小负值;同时 UA 极大时
	// 根无限贴近边界。自适应地取 hi=qFeas·(1-δ):δ 从机器精度量级起倍增,
	// 直到两端温差严格为正且 f(hi)>0(根必在该点内侧)。
	qFeas := hi
	lo := 0.0
	bracketFound := false
	for delta := 1e-13; delta <= 1e-3; delta *= 2 {
		hi = qFeas * (1 - delta)
		d1, d2, ok := endDeltas(s, hi)
		if ok && d1 > 0 && d2 > 0 && f(hi) > 0 {
			bracketFound = true
			break
		}
	}
	if !bracketFound {
		// 根缩进端温差归零的边界层(NTU 极大,效能已饱和):
		// 此时 d2≪d1,LMTD≈d2/ln(d1/d2),d1≈ΔTin 为常数,
		// 方程 q = UA·d2(q)/ln(d1/d2(q)) 是关于 d2 的简单单调式,
		// 对 d2 做二分即可拿到与 ε-NTU 一致的极限解。
		return solveBoundaryLayer(s)
	}

	// 固定 150 次二分:区间按 2^-150 收缩到机器精度以下,中点自然停滞,
	// 这样 q 极小时也能拿到满机器精度,避免"按 Qmax 缩放的绝对容差"
	// 在小换热量下造成相对误差超界。
	for range 150 {
		mid := (lo + hi) / 2
		if mid == lo || mid == hi {
			break // 浮点区间已坍缩
		}
		if f(mid) < 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	q = (lo + hi) / 2
	d1, d2, _ := endDeltas(s, q)
	lmtdAtQ, _ = lmtd(d1, d2)
	return q, lmtdAtQ
}

// feasibleLimit 返回该流型的换热量可行域上界 qFeas:
//   - 逆流: Qmax=Cmin·ΔTin(小热容端温差归零);
//   - 顺流: ΔTin/(1/Ch+1/Cc)(两侧出口相遇,端温差归零)。
func feasibleLimit(s *solvedCase) float64 {
	if s.flow == FlowCounter {
		return s.cMin * s.dTin
	}
	return s.dTin / (1/s.ch + 1/s.cc)
}

// solveBoundaryLayer 处理 NTU 极大、根缩进端温差归零边界层的饱和工况。
//
// 此时小端温差 d2 小到 dq=d2/k 已无法在双精度中改变 q(q 恒等于 qFeas),
// 从两个几乎相等/归零的端温差去反算 LMTD 必然不稳定(对 d2 极度敏感)。
// 改用换热定义式的恒等变形 LMTD=q/UA:它与 q=UA·LMTD 严格等价,
// 在饱和极限是数值最稳的求值路径,且给出的 LMTD 与端温差解析值一致
// (例:UA=1e6,qFeas=126000 → LMTD=0.126,与 d2~1e-205 时的
// ΔTin/ln(ΔTin/d2) 相符)。返回 (qFeas, qFeas/UA),与 ε-NTU 饱和解一致。
func solveBoundaryLayer(s *solvedCase) (q, lmtdAtQ float64) {
	qFeas := feasibleLimit(s)
	return qFeas, qFeas / s.ua
}

// isSaturated 判断效能是否已抵达该流型的理论极限(双精度饱和带)。
// 顺流极限 εmax=1/(1+Cr);逆流极限为 1。
func isSaturated(flow string, eps, cr float64) bool {
	var epsMax float64 = 1
	if flow == FlowParallel {
		epsMax = 1 / (1 + cr)
	}
	// 与极限的相对差距小于 1e-10 即视为饱和(此时端温差 gap 已小到
	// 在双精度出口温度上不可分辨)。
	return eps >= epsMax*(1-1e-10)
}
