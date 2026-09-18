package heatx

import "fmt"

// 错误码(随响应返回,便于调用方程序化处理)。
const (
	CodeMalformed      = "MALFORMED_JSON"
	CodeUnknownField   = "UNKNOWN_FIELD"
	CodeDuplicateField = "DUPLICATE_FIELD"
	CodeMissingField   = "MISSING_FIELD"
	CodeInvalidValue   = "INVALID_VALUE"
	CodeContradiction  = "CONTRADICTORY_INPUT"
	CodeInfeasible     = "TARGET_INFEASIBLE"
	CodeMethodMismatch = "METHOD_MISMATCH"
	CodeBatchTooLarge  = "BATCH_TOO_LARGE"
)

// FieldError 指向具体字段的可读错误。
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// APIError 核算服务的统一错误结构。
type APIError struct {
	Code   string       `json:"code"`
	Msg    string       `json:"message"`
	Fields []FieldError `json:"fields,omitempty"`
}

func (e *APIError) Error() string {
	if len(e.Fields) == 0 {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Msg, e.Fields[0].Field)
}

func malformed(msg string) *APIError {
	return &APIError{Code: CodeMalformed, Msg: msg}
}

func fieldErr(code, field, reason string) *APIError {
	return &APIError{
		Code:   code,
		Msg:    "输入参数不合法,未执行核算",
		Fields: []FieldError{{Field: field, Reason: reason}},
	}
}

func fieldErrs(code, msg string, errs []FieldError) *APIError {
	return &APIError{Code: code, Msg: msg, Fields: errs}
}
