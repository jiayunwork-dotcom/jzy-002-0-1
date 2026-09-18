package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	NewServer(slog.New(slog.NewTextHandler(io.Discard, nil))).Routes(mux)
	return mux
}

func doJSON(t *testing.T, h http.Handler, method, path string, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	raw := rec.Body.Bytes()
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("response not JSON (%d): %s", rec.Code, raw)
		}
	}
	return rec.Code, out
}

func validBody() string {
	return `{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}`
}

func TestConfigEndpoint(t *testing.T) {
	h := testServer(t)
	code, body := doJSON(t, h, "GET", "/api/v1/config", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	flows, ok := body["supported_flows"].([]any)
	if !ok || len(flows) != 2 {
		t.Fatalf("config did not echo flows: %v", body["supported_flows"])
	}
	tol, ok := body["default_tolerances"].(map[string]any)
	if !ok || tol["eps_ntu_vs_lmtd_rel"] == nil {
		t.Fatalf("config did not echo tolerances")
	}
}

func TestHealth(t *testing.T) {
	h := testServer(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("health status=%d", rec.Code)
	}
}

func TestRateSingleOK(t *testing.T) {
	h := testServer(t)
	code, body := doJSON(t, h, "POST", "/api/v1/rate", validBody())
	if code != 200 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if body["ok"] != true {
		t.Fatalf("ok=false: %v", body)
	}
	res := body["result"].(map[string]any)
	if mathAbsFloat(res["q"].(float64)-50) > 1e-9 {
		t.Fatalf("q = %v", res["q"])
	}
	if res["feasible"] != true {
		t.Fatalf("should be feasible")
	}
}

func mathAbsFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestRateSingleErrorEnvelope(t *testing.T) {
	h := testServer(t)
	// Contradictory inlets => 400, no result, readable message.
	bad := `{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":20},"cold":{"m_dot":1,"cp":1,"t_in":80},"ua":1}`
	code, body := doJSON(t, h, "POST", "/api/v1/rate", bad)
	if code != 400 {
		t.Fatalf("want 400, got %d", code)
	}
	if body["ok"] != false {
		t.Fatalf("error envelope ok must be false")
	}
	if _, present := body["result"]; present {
		t.Fatalf("error response must not carry a result")
	}
	errObj := body["error"].(map[string]any)
	if !strings.Contains(errObj["message"].(string), "hot.t_in") {
		t.Fatalf("error message should name hot.t_in: %v", errObj["message"])
	}
}

func TestStrictJSONRejections(t *testing.T) {
	h := testServer(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"non-numeric", `{"flow":"counter","hot":{"m_dot":"x","cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}`, "hot.m_dot"},
		{"missing field", `{"flow":"counter","hot":{"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}`, "hot.m_dot"},
		{"unknown field", `{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1,"bogus":3}`, "bogus"},
		{"duplicate key", `{"flow":"counter","flow":"parallel","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}`, "flow"},
		{"duplicate nested key", `{"flow":"counter","hot":{"m_dot":1,"m_dot":2,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}`, "hot.m_dot"},
		{"malformed", `{"flow":counter}`, "JSON"},
		{"two values", validBody() + ` {"x":1}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := doJSON(t, h, "POST", "/api/v1/rate", tc.body)
			if code != 400 {
				t.Fatalf("want 400, got %d (%v)", code, body)
			}
			if body["ok"] != false {
				t.Fatalf("ok must be false")
			}
			if _, present := body["result"]; present {
				t.Fatalf("must not return result on bad input")
			}
			msg, _ := body["error"].(map[string]any)["message"].(string)
			if tc.want != "" && !strings.Contains(msg, tc.want) {
				t.Fatalf("message %q should mention %q", msg, tc.want)
			}
		})
	}
}

func TestEmptyBody(t *testing.T) {
	h := testServer(t)
	code, _ := doJSON(t, h, "POST", "/api/v1/rate", "")
	if code != 400 {
		t.Fatalf("empty body should be 400, got %d", code)
	}
}

func TestBatchOrderingAndPerCaseErrors(t *testing.T) {
	h := testServer(t)
	// Case 1 valid, case 2 bad (negative ua), case 3 valid parallel.
	payload := `[
		{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1},
		{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":-5},
		{"flow":"parallel","hot":{"m_dot":2,"cp":1,"t_in":120},"cold":{"m_dot":1,"cp":1,"t_in":20},"u":400,"area":2}
	]`
	code, body := doJSON(t, h, "POST", "/api/v1/rate/batch", payload)
	if code != 200 {
		t.Fatalf("batch transport should be 200, got %d %v", code, body)
	}
	rows := body["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	for i, r := range rows {
		row := r.(map[string]any)
		if int(row["index"].(float64)) != i+1 {
			t.Fatalf("row %d has index %v (must preserve submission order)", i, row["index"])
		}
	}
	r1 := rows[0].(map[string]any)
	if r1["ok"] != true {
		t.Fatalf("row1 should be ok")
	}
	r2 := rows[1].(map[string]any)
	if r2["ok"] != false {
		t.Fatalf("row2 should fail")
	}
	prob := r2["error"].(map[string]any)["problems"].([]any)[0].(map[string]any)
	if prob["field"] != "ua" || int(prob["index"].(float64)) != 2 {
		t.Fatalf("row2 error should name ua at index 2, got %v", prob)
	}
	if _, present := r2["result"]; present {
		t.Fatalf("failed row must not carry result")
	}
	r3 := rows[2].(map[string]any)
	if r3["ok"] != true {
		t.Fatalf("row3 should be ok: %v", r3)
	}
}

func TestBatchBadTopLevel(t *testing.T) {
	h := testServer(t)
	code, body := doJSON(t, h, "POST", "/api/v1/rate/batch", `{"flow":"counter"}`)
	if code != 400 {
		t.Fatalf("non-array batch should be 400, got %d", code)
	}
	if body["ok"] != false {
		t.Fatalf("ok false expected")
	}
}

func TestBatchEmptyArray(t *testing.T) {
	h := testServer(t)
	code, _ := doJSON(t, h, "POST", "/api/v1/rate/batch", `[]`)
	if code != 400 {
		t.Fatalf("empty batch should be 400, got %d", code)
	}
}

func TestBatchOneMalformedItem(t *testing.T) {
	h := testServer(t)
	payload := `[
		{"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1},
		{"flow":"counter","hot":{"m_dot":"BAD","cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1}
	]`
	code, body := doJSON(t, h, "POST", "/api/v1/rate/batch", payload)
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	rows := body["rows"].([]any)
	if rows[0].(map[string]any)["ok"] != true {
		t.Fatalf("valid row must still return")
	}
	bad := rows[1].(map[string]any)
	if bad["ok"] != false {
		t.Fatalf("malformed row must fail")
	}
	msg := bad["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "第 2 组") {
		t.Fatalf("error must identify 第 2 组: %q", msg)
	}
}
