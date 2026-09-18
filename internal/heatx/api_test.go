package heatx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doPOST(t *testing.T, h http.Handler, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是合法 JSON: %v\nbody=%s", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

func doGET(h http.Handler, path string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestAPI_SingleCalculate 正常单台核算返回全部指标。
func TestAPI_SingleCalculate(t *testing.T) {
	h := Handler(DefaultServerConfig())
	body := `{
	  "flow_type": "counter",
	  "hot":  {"mass_flow": 2, "cp": 2100, "t_in": 80},
	  "cold": {"mass_flow": 1, "cp": 2100, "t_in": 20},
	  "ua": 2100
	}`
	status, out := doPOST(t, h, "/api/v1/calculate", body)
	if status != http.StatusOK {
		t.Fatalf("状态码=%d body=%v", status, out)
	}
	res := out["result"].(map[string]any)
	for _, k := range []string{
		"hot_t_out_c", "cold_t_out_c", "q_w", "q_max_w", "effectiveness",
		"ntu", "capacity_ratio", "lmtd_k", "temperature_cross", "feasible", "checks",
	} {
		if _, ok := res[k]; !ok {
			t.Errorf("正常结果缺少字段 %s", k)
		}
	}
	if res["feasible"] != true {
		t.Error("feasible 应为 true")
	}
}

// TestAPI_Config 配置查询回显流型与容差。
func TestAPI_Config(t *testing.T) {
	h := Handler(DefaultServerConfig())
	status, out := doGET(h, "/api/v1/config")
	if status != http.StatusOK {
		t.Fatalf("状态码=%d", status)
	}
	flows := out["flow_types"].([]any)
	vals := map[string]bool{}
	for _, f := range flows {
		vals[f.(map[string]any)["value"].(string)] = true
	}
	if !vals["counter"] || !vals["parallel"] {
		t.Fatalf("流型回显不全: %v", vals)
	}
	defs := out["defaults"].(map[string]any)
	if _, ok := defs["tolerances"]; !ok {
		t.Fatal("默认容差未回显")
	}
}

func TestAPI_Health(t *testing.T) {
	h := Handler(DefaultServerConfig())
	if status, _ := doGET(h, "/healthz"); status != http.StatusOK {
		t.Fatal("healthz 应 200")
	}
}

func TestAPI_ErrorShapes(t *testing.T) {
	h := Handler(DefaultServerConfig())

	type tc struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantField  string
	}
	cases := []tc{
		{
			name:       "空请求体",
			body:       "",
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeMalformed,
		},
		{
			name:       "JSON语法错误",
			body:       `{"flow_type": "counter", }`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeMalformed,
		},
		{
			name: "缺字段",
			body: `{"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},
			        "cold":{"mass_flow":1,"cp":2100,"t_in":20}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeInvalidValue,
			wantField:  "ua",
		},
		{
			name:       "非数值",
			body:       `{"flow_type":"counter","hot":{"mass_flow":"big","cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeInvalidValue,
			wantField:  "hot.mass_flow",
		},
		{
			name:       "未知字段",
			body:       `{"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100,"color":"red"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeUnknownField,
			wantField:  "color",
		},
		{
			name:       "重复键",
			body:       `{"flow_type":"counter","flow_type":"parallel","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeDuplicateField,
			wantField:  "flow_type",
		},
		{
			name:       "矛盾温度",
			body:       `{"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":10},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   CodeInvalidValue,
			wantField:  "t_in",
		},
		{
			name: "目标超物理上限",
			body: `{"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},
			        "cold":{"mass_flow":1,"cp":2100,"t_in":20,"t_out_target":85},"ua":2100}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   CodeInfeasible,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out := doPOST(t, h, "/api/v1/calculate", c.body)
			if status != c.wantStatus {
				t.Fatalf("状态码=%d 期望 %d, body=%v", status, c.wantStatus, out)
			}
			if _, ok := out["result"]; ok {
				t.Fatal("异常响应不得包含 result")
			}
			e := out["error"].(map[string]any)
			if e["code"] != c.wantCode {
				t.Fatalf("错误码=%v 期望 %s", e["code"], c.wantCode)
			}
			if c.wantField != "" {
				fields := e["fields"].([]any)
				got := fields[0].(map[string]any)["field"].(string)
				if !strings.Contains(got, c.wantField) {
					t.Fatalf("错误字段=%q 应包含 %q", got, c.wantField)
				}
			}
		})
	}
}

// TestAPI_BatchOrderAndIsolation 批量按提交顺序成表,单组非法不影响其余。
func TestAPI_BatchOrderAndIsolation(t *testing.T) {
	h := Handler(DefaultServerConfig())
	body := `{"cases":[
	  {"name":"a","flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100},
	  {"name":"bad","flow_type":"counter","hot":{"mass_flow":-5,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100},
	  {"name":"c","flow_type":"parallel","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":2,"cp":2100,"t_in":20},"ua":4200}
	]}`
	status, out := doPOST(t, h, "/api/v1/calculate/batch", body)
	if status != http.StatusOK {
		t.Fatalf("批量请求整体应 200(逐项报错),实际 %d body=%v", status, out)
	}
	items := out["results"].([]any)
	if len(items) != 3 {
		t.Fatalf("应返回 3 项,实际 %d", len(items))
	}
	if items[0].(map[string]any)["name"] != "a" || items[2].(map[string]any)["name"] != "c" {
		t.Fatal("结果顺序必须与提交顺序一致")
	}
	if items[0].(map[string]any)["status"] != "ok" || items[2].(map[string]any)["status"] != "ok" {
		t.Fatal("第 0、2 组应成功")
	}
	mid := items[1].(map[string]any)
	if mid["status"] != "error" {
		t.Fatal("第 1 组应失败")
	}
	e := mid["error"].(map[string]any)
	field := e["fields"].([]any)[0].(map[string]any)["field"].(string)
	if !strings.Contains(field, "cases[1]") || !strings.Contains(field, "mass_flow") {
		t.Fatalf("错误应指出第 2 组(index=1)与具体字段,实际 %q", field)
	}
}

// TestAPI_BatchMalformed 批量请求外层结构错误应整体 400。
func TestAPI_BatchMalformed(t *testing.T) {
	h := Handler(DefaultServerConfig())
	status, out := doPOST(t, h, "/api/v1/calculate/batch", `{"cases":[]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("空批量应 400,实际 %d", status)
	}
	if _, ok := out["error"]; !ok {
		t.Fatal("应返回错误说明")
	}

	// 某组 JSON 本身坏掉:组级错误,带组号
	body := `{"cases":[
	  {"flow_type":"counter","hot":{"mass_flow":2,"cp":2100,"t_in":80},"cold":{"mass_flow":1,"cp":2100,"t_in":20},"ua":2100},
	  {"flow_type":"counter","hot":{"mass_flow":,"cp":2100,"t_in":80}}
	]}`
	status, out = doPOST(t, h, "/api/v1/calculate/batch", body)
	if status != http.StatusOK {
		t.Fatalf("组级 JSON 错误应逐项返回、整体 200,实际 %d", status)
	}
	items := out["results"].([]any)
	if items[1].(map[string]any)["status"] != "error" {
		t.Fatal("第 2 组应为 error")
	}
	e := items[1].(map[string]any)["error"].(map[string]any)
	if e["code"] != CodeMalformed {
		t.Fatalf("坏 JSON 错误码=%v", e["code"])
	}
}

func TestAPI_404and405(t *testing.T) {
	h := Handler(DefaultServerConfig())
	status, out := doGET(h, "/nope")
	if status != http.StatusNotFound || out["error"] == nil {
		t.Fatalf("未知路径应 JSON 404,实际 %d %v", status, out)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/calculate", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE 应 405,实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatal("405 也应为 JSON")
	}
}
