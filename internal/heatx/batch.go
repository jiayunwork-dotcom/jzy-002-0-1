package heatx

import (
	"bytes"
	"encoding/json"
)

// parseBatchRequest 容错地解析批量请求外层结构 {"cases":[<raw>,<raw>,...]}。
//
// 关键取舍:标准库把元素解码进 json.RawMessage 时仍会深扫校验语法,
// 一个元素内的语法错误会让整批失败。这里按括号/字符串深度手工切分数组元素,
// 元素内部即使是坏 JSON 也原样保留,交给逐项 decodeStrict 报组级错误,
// 从而保证"某组非法指出第几组,其余各组照常返回"。
//
// 只容忍"元素内部"的语法错误;外层结构(键、冒号、括号配对、尾随内容)
// 一旦不合法仍返回整体错误。
func parseBatchRequest(data []byte) ([]json.RawMessage, *APIError) {
	p := &batchScanner{in: data}
	if err := p.scanOuter(); err != nil {
		return nil, err
	}
	return p.elements, nil
}

type batchScanner struct {
	in        []byte
	pos       int
	sawCases  bool
	elements  []json.RawMessage
	truncated bool // 输入在最后一个元素内部截断(容器/字符串未闭合)
}

func (p *batchScanner) skipWS() {
	for p.pos < len(p.in) {
		switch p.in[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *batchScanner) scanOuter() *APIError {
	p.skipWS()
	if p.pos >= len(p.in) || p.in[p.pos] != '{' {
		return malformed("批量请求体必须是 {\"cases\":[...]} 对象")
	}
	p.pos++
	p.skipWS()
	if p.pos < len(p.in) && p.in[p.pos] == '}' {
		p.pos++
		return malformed("批量核算缺少 cases 字段")
	}

	seen := map[string]bool{}
	for {
		p.skipWS()
		key, err := p.readKey()
		if err != nil {
			return err
		}
		if key != "cases" {
			return fieldErr(CodeUnknownField, key, "批量请求顶层仅接受 cases 字段")
		}
		if seen[key] {
			return fieldErr(CodeDuplicateField, key, "顶层 cases 字段重复出现")
		}
		seen[key] = true
		p.sawCases = true

		p.skipWS()
		if p.pos >= len(p.in) || p.in[p.pos] != ':' {
			return malformed("cases 键后缺少冒号")
		}
		p.pos++
		p.skipWS()
		if err := p.readCasesArray(); err != nil {
			return err
		}
		// readCasesArray 可能为容纳元素级坏 JSON 而把输入消费到末尾
		// (容器/字符串在最后一个元素内截断),此时不再要求外层闭合,
		// 坏元素交给逐项解码按组号报错。
		p.skipWS()
		if p.pos >= len(p.in) {
			if p.truncated {
				return nil // 末元素内截断:坏元素由逐项解码报组号
			}
			return malformed("批量请求 JSON 未闭合:缺少 ]}")
		}
		switch p.in[p.pos] {
		case '}':
			p.pos++
			p.skipWS()
			if p.pos != len(p.in) {
				return malformed("主 JSON 对象之后存在多余内容")
			}
			return nil
		case ',':
			p.pos++
			continue
		default:
			return malformed("cases 后应为逗号或闭合括号")
		}
	}
}

// readKey 读取并严格解码一个 JSON 对象键。
func (p *batchScanner) readKey() (string, *APIError) {
	if p.pos >= len(p.in) || p.in[p.pos] != '"' {
		return "", malformed("批量请求对象中应为字符串键")
	}
	start := p.pos
	p.pos++ // 越过开引号,从其后开始寻找闭引号
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if c == '\\' {
			p.pos += 2
			continue
		}
		if c == '"' {
			raw := p.in[start : p.pos+1]
			key, ok := unquoteKey(raw)
			if !ok {
				return "", malformed("对象键包含非法转义")
			}
			p.pos++
			return key, nil
		}
		p.pos++
	}
	return "", malformed("对象键的字符串未闭合")
}

// readCasesArray 按深度切分 cases 数组,不校验元素内部语法。
func (p *batchScanner) readCasesArray() *APIError {
	if p.pos >= len(p.in) || p.in[p.pos] != '[' {
		return malformed("cases 必须是数组")
	}
	p.pos++

	depth := 0 // 相对数组内部的嵌套深度
	elemStart := p.pos
	inStr := false

	flush := func(end int) {
		raw := bytes.TrimSpace(p.in[elemStart:end])
		if len(raw) > 0 {
			p.elements = append(p.elements, json.RawMessage(append([]byte(nil), raw...)))
		} else {
			// 空元素(如连续逗号)也保留一个空切片,让逐项解码报 MALFORMED
			p.elements = append(p.elements, json.RawMessage{})
		}
	}

	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if inStr {
			switch c {
			case '\\':
				p.pos++ // 跳过被转义字符
			case '"':
				inStr = false
			}
			p.pos++
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				if c == '}' {
					return malformed("cases 数组未闭合,遇到意外的 }")
				}
				// 数组结束:末元素若非空白则入账;若刚消费过逗号则是尾随逗号
				tail := bytes.TrimSpace(p.in[elemStart:p.pos])
				switch {
				case len(tail) > 0:
					flush(p.pos)
				case len(p.elements) > 0:
					p.elements = append(p.elements, json.RawMessage{}) // 尾随逗号 → 组级语法错误
				}
				p.pos++
				p.truncated = false // 数组已正常闭合,后续仍要求外层结构合法
				return nil
			}
			depth--
		case ',':
			if depth == 0 {
				flush(p.pos) // 空元素(连续逗号)也会作为空 raw 报组级语法错误
				elemStart = p.pos + 1
				p.pos++
				continue
			}
		default:
			// 其它字符(标量/空白),深度扫描无需处理
		}
		p.pos++
	}
	// 输入结束:
	//  - 若停在元素内部(容器未闭合或字符串未闭合),把最后一块作为元素保留,
	//    它本身是坏 JSON,会在逐项解码时报"第几组"的语法错误;
	//  - 若停在数组层且有已起头的标量元素,同样保留;
	//  - 数组层纯空白结束才算结构性未闭合。
	tail := bytes.TrimSpace(p.in[elemStart:])
	if inStr || depth > 0 {
		p.truncated = true
	}
	if len(tail) > 0 || p.truncated {
		flush(len(p.in))
	} else {
		return malformed("cases 数组未闭合:缺少 ]")
	}
	return nil
}

// unquoteKey 解码 JSON 字符串键;只在键上做严格转义校验。
func unquoteKey(raw []byte) (string, bool) {
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", false
	}
	return key, true
}
