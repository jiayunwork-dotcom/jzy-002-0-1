package heatx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeStrict 将 JSON 严格解码到 dst:
//   - 拒绝未知字段(DisallowUnknownFields);
//   - 检测同一对象内重复出现的键(防止两侧参数互相覆盖);
//   - 捕获类型不匹配并定位到字段;
//   - 拒绝语法错误与尾随内容。
//
// prefix 用于批量场景标注组号(如 "cases[2].")。
func decodeStrict(data []byte, dst any, prefix string) *APIError {
	if dup, path := findDuplicateKey(data, prefix); dup {
		return fieldErr(CodeDuplicateField, path, "JSON 中出现重复字段,后值会覆盖前值,已拒绝以防参数被悄悄改写")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return mapDecodeError(err, prefix)
	}
	// 顶层之后只允许空白
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return malformed("请求体在主 JSON 对象之后还存在多余内容")
	}
	return nil
}

// dupScanner 用 token 扫描检查对象内重复键,并维护点路径。
type dupScanner struct {
	dec *json.Decoder
	// 每层路径段:对象层为当前键,数组层为 "[i]"。
	seg     []string
	objKeys []map[string]struct{} // 对象层非 nil;数组层为 nil
	arrIdx  []int                 // 数组层的下一元素下标;对象层为 -1
	// awaitingValue: 栈深度 -> 该键的值是否为下一个 token。
	awaitingValue bool
}

// findDuplicateKey 检查对象内是否有重复键,返回重复键的完整路径。
func findDuplicateKey(data []byte, prefix string) (bool, string) {
	s := &dupScanner{dec: json.NewDecoder(bytes.NewReader(data))}
	s.dec.UseNumber()

	for {
		tok, err := s.dec.Token()
		if err == io.EOF {
			return false, ""
		}
		if err != nil {
			return false, "" // 语法错误交给标准解码报告
		}

		// 该 token 是上一键的值:容器开界符在下面的分支里处理栈,
		// 标量则在此消费后清除等待态。
		isValueToken := s.awaitingValue
		startContainer := false

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				s.seg = append(s.seg, "")
				s.objKeys = append(s.objKeys, map[string]struct{}{})
				s.arrIdx = append(s.arrIdx, -1)
				startContainer = true
			case '[':
				s.seg = append(s.seg, "[0]")
				s.objKeys = append(s.objKeys, nil)
				s.arrIdx = append(s.arrIdx, 0)
				startContainer = true
			case '}', ']':
				s.pop()
				s.awaitingValue = false
			}
		case string:
			depth := len(s.seg)
			if depth > 0 && s.objKeys[depth-1] != nil && !s.awaitingValue {
				// 对象层的键
				key := t
				keys := s.objKeys[depth-1]
				if _, seen := keys[key]; seen {
					return true, prefix + s.pathTo(key)
				}
				keys[key] = struct{}{}
				s.seg[depth-1] = key
				s.awaitingValue = true
			} else {
				// 字符串值
				s.awaitingValue = false
				s.afterValue()
			}
		default:
			// number / bool / null 标量值
			s.awaitingValue = false
			s.afterValue()
		}

		// 容器值开启后,它整体作为父键的值已"开始",清除等待态;
		// 闭合时的 pop() 内部会推进父数组下标。
		if startContainer && isValueToken {
			s.awaitingValue = false
		}
	}
}

func (s *dupScanner) pop() {
	n := len(s.seg) - 1
	s.seg = s.seg[:n]
	s.objKeys = s.objKeys[:n]
	s.arrIdx = s.arrIdx[:n]
	s.afterValue()
}

// afterValue 在消费完一个值后推进最近未关闭数组的元素下标。
func (s *dupScanner) afterValue() {
	for i := len(s.seg) - 1; i >= 0; i-- {
		if s.objKeys[i] == nil {
			s.arrIdx[i]++
			s.seg[i] = fmt.Sprintf("[%d]", s.arrIdx[i])
			return
		}
	}
}

// pathTo 用当前各层段拼出 key 的完整路径(最末对象层段尚未更新为 key)。
func (s *dupScanner) pathTo(key string) string {
	var parts []string
	for i, seg := range s.seg {
		if s.objKeys[i] == nil {
			// 数组层:直接附加 [i],不加分隔
			parts = append(parts, seg)
			continue
		}
		if i < len(s.seg)-1 && seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) > 0 {
		return joinFields(parts) + "." + key
	}
	return key
}

func joinFields(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 && p[0] != '[' {
			out += "."
		}
		out += p
	}
	return out
}

// mapDecodeError 把 encoding/json 的错误转成带字段定位的可读错误。
func mapDecodeError(err error, prefix string) *APIError {
	if e, ok := err.(*json.UnmarshalTypeError); ok {
		return fieldErr(CodeInvalidValue, prefix+e.Field,
			fmt.Sprintf("字段类型不正确:应为 %s,实际拿到的是 %s 值", e.Type.String(), e.Value))
	}
	if e, ok := err.(*json.SyntaxError); ok {
		return malformed(fmt.Sprintf("JSON 语法错误(偏移 %d):%s", e.Offset, e.Error()))
	}
	msg := err.Error()
	// DisallowUnknownFields 的错误形如: json: unknown field "xxx"
	if len(msg) > 19 && msg[:19] == "json: unknown field" {
		field := msg[21 : len(msg)-1]
		return fieldErr(CodeUnknownField, prefix+field, "未知字段,服务不接受该参数,请核对字段名")
	}
	return malformed("无法解析 JSON:" + msg)
}
