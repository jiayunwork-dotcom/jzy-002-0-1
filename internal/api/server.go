package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"heatexchanger/internal/exchanger"
)

// maxBodyBytes bounds a single rating request; batch bodies are larger than
// single ones but the pure-computation payload stays small.
const (
	maxSingleBody = 1 << 20 // 1 MiB
	maxBatchBody  = 8 << 20 // 8 MiB
	maxBatchCases = 1000
)

// Server wires the rating core to HTTP.
type Server struct {
	log *slog.Logger
}

func NewServer(log *slog.Logger) *Server { return &Server{log: log} }

// Routes registers all HTTP endpoints.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v1/config", s.handleConfig)
	mux.HandleFunc("POST /api/v1/rate", s.handleRate)
	mux.HandleFunc("POST /api/v1/rate/batch", s.handleRateBatch)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, exchanger.Info())
}

// Single-case envelopes.
type rateResponse struct {
	OK     bool              `json:"ok"`
	Result *exchanger.Result `json:"result,omitempty"`
	Error  *errorBody        `json:"error,omitempty"`
}

type errorBody struct {
	Message  string                 `json:"message"`
	Problems []exchanger.FieldError `json:"problems,omitempty"`
}

type batchResponse struct {
	OK   bool        `json:"ok"`
	Rows []batchItem `json:"rows"`
}

type batchItem struct {
	Index  int               `json:"index"` // 1-based, in submission order
	OK     bool              `json:"ok"`
	Result *exchanger.Result `json:"result,omitempty"`
	Error  *errorBody        `json:"error,omitempty"`
}

func (s *Server) handleRate(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r, maxSingleBody)
	if err != nil {
		return
	}
	var in exchanger.CaseInput
	if err := strictDecode(body, &in); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := exchanger.Calculate(in)
	if err != nil {
		s.writeCalcError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rateResponse{OK: true, Result: res})
}

func (s *Server) handleRateBatch(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r, maxBatchBody)
	if err != nil {
		return
	}
	// Top level must be an array; validate it as JSON first so malformed
	// payloads produce one clear error instead of N nonsense rows.
	var raw []json.RawMessage
	if err := strictDecode(body, &raw); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if len(raw) == 0 {
		s.failMsg(w, http.StatusBadRequest, "批量核算至少需要一组参数")
		return
	}
	if len(raw) > maxBatchCases {
		s.failMsg(w, http.StatusBadRequest, "批量核算组数 %d 超过上限 %d", len(raw), maxBatchCases)
		return
	}
	rows := make([]batchItem, len(raw))
	for i, item := range raw {
		idx := i + 1
		var in exchanger.CaseInput
		if err := strictDecode(item, &in); err != nil {
			rows[i] = batchItem{Index: idx, OK: false, Error: problemBody(idx, err)}
			continue
		}
		res, err := exchanger.Calculate(in)
		if err != nil {
			rows[i] = batchItem{Index: idx, OK: false, Error: problemBody(idx, err)}
			continue
		}
		rows[i] = batchItem{Index: idx, OK: true, Result: res}
	}
	// Batch transport always succeeds once the top level is a valid array:
	// per-case legality is expressed per row and never aborts the other rows.
	writeJSON(w, http.StatusOK, batchResponse{OK: true, Rows: rows})
}

// writeCalcError separates bad input (4xx) from an internal invariant breach
// (5xx). An infeasible target is a normal result and never reaches here.
func (s *Server) writeCalcError(w http.ResponseWriter, err error) {
	var ve *exchanger.ValidationError
	if errors.As(err, &ve) {
		writeJSON(w, http.StatusBadRequest, rateResponse{
			OK:    false,
			Error: &errorBody{Message: ve.Error(), Problems: ve.Problems},
		})
		return
	}
	s.log.Error("rating invariant failure", "err", err.Error())
	s.fail(w, http.StatusInternalServerError, err)
}

// fail writes a transport-level error envelope (no result rows at all).
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": errorBody{Message: err.Error()}})
}

func (s *Server) failMsg(w http.ResponseWriter, status int, format string, args ...any) {
	s.fail(w, status, &plainError{msg: fmt.Sprintf(format, args...)})
}

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }

// problemBody adapts a per-case error (validation or decoding) to a row body.
// Decode errors carry only a message; validation errors carry field problems.
func problemBody(index int, err error) *errorBody {
	var ve *exchanger.ValidationError
	if errors.As(err, &ve) {
		ps := make([]exchanger.FieldError, len(ve.Problems))
		for i, p := range ve.Problems {
			p.Index = index
			ps[i] = p
		}
		return &errorBody{Message: ve.Error(), Problems: ps}
	}
	return &errorBody{
		Message:  fmt.Sprintf("第 %d 组: %s", index, err.Error()),
		Problems: []exchanger.FieldError{{Index: index, Field: "-", Issue: err.Error()}},
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"ok":    false,
				"error": errorBody{Message: "请求体超过大小限制"},
			})
			return nil, err
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": errorBody{Message: "读取请求体失败: " + err.Error()},
		})
		return nil, err
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": errorBody{Message: "请求体为空,需要 JSON 参数"},
		})
		return nil, &plainError{msg: "empty body"}
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
