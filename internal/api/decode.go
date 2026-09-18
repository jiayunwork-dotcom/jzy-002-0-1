package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// strictDecode unmarshals JSON into dst with three guarantees beyond the
// standard decoder:
//
//  1. unknown fields are rejected (no silently-ignored / shadowed parameters),
//  2. duplicate keys at any depth are rejected (two sides overwriting each
//     other or a parameter appearing twice),
//  3. the message must be exactly one JSON value (no trailing garbage).
//
// The returned error is already translated into a readable, parameter-aware
// Chinese message.
func strictDecode(data []byte, dst any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return translateJSONError(err)
	}
	var tail json.RawMessage
	if err := dec.Decode(&tail); err != io.EOF {
		return fmt.Errorf("请求体包含多个 JSON 值或在主 JSON 之后有多余内容,只允许一个 JSON 值")
	}
	return nil
}

// frame tracks one open object/array while scanning tokens.
type frame struct {
	isObj     bool
	expectKey bool // for objects: the next string token is a key
	keys      []string
}

// rejectDuplicateKeys scans the raw token stream and rejects any repeated key
// within the same object, reporting the dotted JSON path (e.g. hot.cp).
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []frame
	var path []string // ancestor object keys describing the current value slot

	// valueComplete marks the end of the value currently expected by the top
	// frame: the parent object becomes ready for its next key and the key path
	// segment is popped.
	valueComplete := func() {
		if len(stack) == 0 {
			return
		}
		top := &stack[len(stack)-1]
		if top.isObj && !top.expectKey {
			top.expectKey = true
			if len(path) > 0 {
				path = path[:len(path)-1]
			}
		}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return translateJSONError(err)
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, frame{isObj: true, expectKey: true})
			case '[':
				stack = append(stack, frame{isObj: false})
			case '}':
				// Inner key paths are already balanced by valueComplete; the
				// object's own key is released through its parent below.
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				valueComplete()
			case ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				valueComplete()
			}
		case string:
			// A string is an object key only when the top frame expects one;
			// otherwise it is a scalar value.
			if len(stack) > 0 && stack[len(stack)-1].isObj && stack[len(stack)-1].expectKey {
				top := &stack[len(stack)-1]
				key := t
				for _, k := range top.keys {
					if k == key {
						full := key
						if len(path) > 0 {
							full = strings.Join(path, ".") + "." + key
						}
						return fmt.Errorf("参数 %s 重复出现,同一字段不得给出两次(防止参数互相覆盖)", full)
					}
				}
				top.keys = append(top.keys, key)
				top.expectKey = false
				path = append(path, key)
			} else {
				valueComplete()
			}
		default: // number, bool, null
			valueComplete()
		}
	}
}

// translateJSONError turns encoding/json errors into readable Chinese messages,
// extracting the offending field path from UnmarshalTypeError.
func translateJSONError(err error) error {
	switch e := err.(type) {
	case *json.UnmarshalTypeError:
		field := e.Field
		if field == "" {
			field = fmt.Sprintf("(应为 %s)", e.Type.String())
		}
		return fmt.Errorf("参数 %s 类型错误:应为 %s,实际收到 %s", field, e.Type.String(), e.Value)
	case *json.SyntaxError:
		return fmt.Errorf("JSON 语法错误(位置 %d): %v", e.Offset, err)
	default:
		msg := err.Error()
		if strings.HasPrefix(msg, "json: unknown field ") {
			name := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
			return fmt.Errorf("存在不认识的参数 %q,请检查字段名或是否放错了 hot/cold 分组", name)
		}
		if strings.Contains(msg, "cannot unmarshal") {
			return fmt.Errorf("参数类型不合法:%s", msg)
		}
		if strings.Contains(msg, "unexpected end of JSON input") {
			return fmt.Errorf("请求体不是完整的 JSON,可能缺少字段或括号")
		}
		return fmt.Errorf("JSON 解析失败:%v", err)
	}
}
