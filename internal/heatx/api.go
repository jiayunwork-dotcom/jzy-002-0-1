package heatx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var errEmptyBody = errors.New("empty request body")

// MaxBatchCases 单次批量核算的组数上限,防止超大请求拖垮纯计算服务。
const MaxBatchCases = 1000

const (
	maxSingleBody = 1 << 20  // 1 MiB
	maxBatchBody  = 10 << 20 // 10 MiB
)

// ServerConfig 服务运行配置。
type ServerConfig struct {
	Tolerances Tolerances
}

// DefaultServerConfig 默认服务配置。
func DefaultServerConfig() ServerConfig {
	return ServerConfig{Tolerances: DefaultTolerances()}
}

// batchItemOut 批量结果中的一项(成功或失败)。
type batchItemOut struct {
	Index  int       `json:"index"`
	Name   string    `json:"name,omitempty"`
	Status string    `json:"status"` // ok / error
	Result *Result   `json:"result,omitempty"`
	Error  *APIError `json:"error,omitempty"`
}

// Handler 注册核算服务路由,返回可测试的 http.Handler。
func Handler(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, configResponse(cfg))
	})

	mux.HandleFunc("POST /api/v1/calculate", func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(w, r, maxSingleBody)
		if err != nil {
			return
		}
		var c Case
		if aerr := decodeStrict(body, &c, ""); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		res, aerr := Calculate(&c, cfg.Tolerances)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res})
	})

	mux.HandleFunc("POST /api/v1/calculate/batch", func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(w, r, maxBatchBody)
		if err != nil {
			return
		}
		// 容错拆分:元素内部坏 JSON 归组级错误,不拖垮整批
		cases, aerr := parseBatchRequest(body)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		if len(cases) == 0 {
			writeAPIError(w, fieldErr(CodeMissingField, "cases", "批量核算至少需要一组工况"))
			return
		}
		if len(cases) > MaxBatchCases {
			writeAPIError(w, fieldErr(CodeBatchTooLarge, "cases",
				"批量组数超过上限,单次最多 "+itoa(MaxBatchCases)+" 组"))
			return
		}

		items := make([]batchItemOut, len(cases))
		for i, raw := range cases {
			var c Case
			prefix := "cases[" + itoa(i) + "]."
			if aerr := decodeStrict(raw, &c, prefix); aerr != nil {
				items[i] = batchItemOut{Index: i, Status: "error", Error: aerr}
				continue
			}
			res, aerr := Calculate(&c, cfg.Tolerances)
			if aerr != nil {
				// 字段错误统一补上组号前缀,便于定位"第几组、哪个参数"。
				for j := range aerr.Fields {
					aerr.Fields[j].Field = prefix + aerr.Fields[j].Field
				}
				items[i] = batchItemOut{Index: i, Name: c.Name, Status: "error", Error: aerr}
				continue
			}
			items[i] = batchItemOut{Index: i, Name: c.Name, Status: "ok", Result: res}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": items})
	})

	return withJSONErrors(mux)
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeAPIError(w, fieldErr(CodeBatchTooLarge, "body", "请求体超过大小限制"))
			return nil, err
		}
		writeAPIError(w, malformed("无法读取请求体:"+err.Error()))
		return nil, err
	}
	if len(body) == 0 {
		writeAPIError(w, malformed("请求体为空,需要 JSON 工况参数"))
		return nil, errEmptyBody
	}
	return body, nil
}

func writeAPIError(w http.ResponseWriter, e *APIError) {
	status := httpStatusFor(e.Code)
	writeJSON(w, status, map[string]any{"error": e})
}

func httpStatusFor(code string) int {
	switch code {
	case CodeInfeasible:
		return http.StatusUnprocessableEntity // 422:物理不可行
	case CodeMethodMismatch:
		return http.StatusInternalServerError // 500:两法互证失败属服务端数值异常
	default:
		return http.StatusBadRequest // 400:输入类错误
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// withJSONErrors 把 404/405 也统一成 JSON 错误响应:
// 拦截 mux 默认写入的纯文本状态行,改为输出 JSON。
func withJSONErrors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &interceptingWriter{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error": APIError{Code: "NOT_FOUND", Msg: "路径不存在,请查看 GET /api/v1/config 获取服务能力"},
			})
		case http.StatusMethodNotAllowed:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": APIError{Code: "METHOD_NOT_ALLOWED", Msg: "该路径不支持此 HTTP 方法"},
			})
		default:
			// 正常响应:被拦截的 Header/Body 此刻补写到真实 ResponseWriter
			copyHeader(w.Header(), rec.hdr)
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body)
		}
	})
}

// interceptingWriter 暂存所有输出,直到外层决定放行还是替换错误体。
type interceptingWriter struct {
	http.ResponseWriter
	status int
	hdr    http.Header
	body   []byte
}

func (r *interceptingWriter) Header() http.Header {
	if r.hdr == nil {
		r.hdr = http.Header{}
	}
	return r.hdr
}

func (r *interceptingWriter) WriteHeader(code int) {
	r.status = code
}

func (r *interceptingWriter) Write(b []byte) (int, error) {
	// net/http 在未显式 WriteHeader 时隐式 200
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
