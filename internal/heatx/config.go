package heatx

// configResponse 是 GET /api/v1/config 的回显内容,
// 说明当前服务支持的流型、字段规格与默认容差。
func configResponse(cfg ServerConfig) map[string]any {
	return map[string]any{
		"service":     "heatx",
		"description": "间壁式换热器后端核算服务(ε-NTU 闭式解与 LMTD 迭代双路互证)",
		"flow_types": []map[string]string{
			{"value": FlowCounter, "label": "逆流", "aliases": "counterflow, counter-current, 逆流"},
			{"value": FlowParallel, "label": "顺流", "aliases": "parallelflow, concurrent, co-current, cocurrent, 顺流"},
		},
		"endpoints": []map[string]string{
			{"method": "POST", "path": "/api/v1/calculate", "description": "单台核算"},
			{"method": "POST", "path": "/api/v1/calculate/batch", "description": "批量核算,按提交顺序成表返回"},
			{"method": "GET", "path": "/api/v1/config", "description": "回显流型与默认容差配置"},
			{"method": "GET", "path": "/healthz", "description": "存活探针"},
		},
		"defaults": map[string]any{
			"tolerances":            cfg.Tolerances,
			"max_batch_cases":       MaxBatchCases,
			"max_single_body_bytes": maxSingleBody,
			"max_batch_body_bytes":  maxBatchBody,
		},
		"input_spec": map[string]any{
			"hot":       map[string]string{"mass_flow": "kg/s,>0", "cp": "J/(kg·K),>0", "t_in": "℃,必须高于 cold.t_in", "t_out_target": "℃,可选,目标出口温度"},
			"cold":      map[string]string{"mass_flow": "kg/s,>0", "cp": "J/(kg·K),>0", "t_in": "℃", "t_out_target": "℃,可选"},
			"ua":        "W/K,>0;与 area/u 二选一,直给时代表服务(含污垢)热导",
			"area":      "m²,>0;与 u 成对提供",
			"u":         "W/(m²·K),>0;洁净总传热系数",
			"fouling_r": "m²·K/W,>=0;1/U_service = 1/U + fouling_r,仅 area/u 规格可用",
			"flow_type": "counter(逆流) 或 parallel(顺流)",
		},
		"output_fields": []string{
			"hot_t_out_c", "cold_t_out_c", "q_w", "q_max_w", "effectiveness",
			"ntu", "capacity_ratio", "lmtd_k", "temperature_cross", "feasible",
			"margin", "checks", "warnings",
		},
	}
}
