package gatewaysecpol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// 有序 JSON 解析与序列化（设计 §5「确定性遍历」：内层 JSON 解析禁止使用无序
// map 直接遍历，否则叶子替换顺序随机会导致占位符编号在两轮相同历史间漂移，
// 破坏上游前缀缓存（§2 硬性契约）。实现参照 gatewayusage / usagewriter 的
// OrderedObject/jsonx 先例：token 流解析保序、json.Number 保留数字字面文本、
// 紧凑序列化不转义 HTML 字符。

// orderedObject 是插入序 JSON 对象：键序即文档序，遍历与序列化按同一次序进
// 行，保证同一 body 两次 Sanitize 逐字节一致（§12 验收 9）。
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: map[string]any{}}
}

func (o *orderedObject) get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

func (o *orderedObject) has(key string) bool {
	if o == nil {
		return false
	}
	_, exists := o.values[key]
	return exists
}

// set 插入或替换键值；替换保留首次插入位置（与 JS 对象语义一致）。
func (o *orderedObject) set(key string, value any) {
	if o.values == nil {
		o.values = map[string]any{}
	}
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// parseOrderedJSON 解析完整 JSON 文档为有序值树：对象 *orderedObject、数组
// []any、字符串 string、数字 json.Number、布尔、nil。数字保留字面文本
// （UseNumber），未命中的数字叶子按原字节回写（设计 §5）。尾随非空白内容
// 报错（与 JSON.parse 一致）。
func parseOrderedJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON 文档存在尾随内容")
		}
		return nil, err
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedFromToken(decoder, token)
}

func decodeOrderedFromToken(decoder *json.Decoder, token json.Token) (any, error) {
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := newOrderedObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyToken.(string)
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				object.set(key, value)
			}
			if _, err := decoder.Token(); err != nil { // consume '}'
				return nil, err
			}
			return object, nil
		case '[':
			items := []any{}
			for decoder.More() {
				value, err := decodeOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			if _, err := decoder.Token(); err != nil { // consume ']'
				return nil, err
			}
			return items, nil
		default:
			return nil, errors.New("意外的 JSON 分隔符")
		}
	case nil:
		return nil, nil
	default:
		// string / json.Number / bool 由 Token 直接给出。
		return typed, nil
	}
}

// serializeOrdered 将有序值树序列化为紧凑 JSON（无空白、不转义 HTML 字符）。
// json.Number 原样输出字面文本；字符串转义与 encoding/json（escapeHTML=false）
// 字节语义一致，与项目其他 JSON 序列化产物形态统一。
func serializeOrdered(root any) []byte {
	var buf bytes.Buffer
	writeOrderedValue(&buf, root)
	return buf.Bytes()
}

func writeOrderedValue(buf *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if typed {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(typed.String())
	case string:
		writeOrderedString(buf, typed)
	case []any:
		buf.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buf.WriteByte(',')
			}
			writeOrderedValue(buf, item)
		}
		buf.WriteByte(']')
	case *orderedObject:
		buf.WriteByte('{')
		for index, key := range typed.keys {
			if index > 0 {
				buf.WriteByte(',')
			}
			writeOrderedString(buf, key)
			buf.WriteByte(':')
			writeOrderedValue(buf, typed.values[key])
		}
		buf.WriteByte('}')
	default:
		// 解析器只产出上述类型；该分支是完备性兜底，不会在正常链路触发。
		buf.WriteString("null")
	}
}

const hexDigits = "0123456789abcdef"

// writeOrderedString 按 encoding/json（escapeHTML=false）语义转义字符串：仅
// 转义双引号、反斜杠与控制字符（\n \r \t 用短转义，其余 <0x20 用 \u00xx）；
// U+2028/U+2029 恒转义；非法 UTF-8 序列替换为 U+FFFD——与标准库字节语义一致。
func writeOrderedString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if safeJSONByte(b) {
				i++
				continue
			}
			if start < i {
				buf.WriteString(s[start:i])
			}
			buf.WriteByte('\\')
			switch b {
			case '\\', '"':
				buf.WriteByte(b)
			case '\n':
				buf.WriteByte('n')
			case '\r':
				buf.WriteByte('r')
			case '\t':
				buf.WriteByte('t')
			default:
				buf.WriteString("u00")
				buf.WriteByte(hexDigits[b>>4])
				buf.WriteByte(hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if start < i {
				buf.WriteString(s[start:i])
			}
			buf.WriteString(`\ufffd`)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			if start < i {
				buf.WriteString(s[start:i])
			}
			buf.WriteString(`\u202`)
			buf.WriteByte(hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		buf.WriteString(s[start:])
	}
	buf.WriteByte('"')
}

func safeJSONByte(b byte) bool {
	return b >= 0x20 && b != '"' && b != '\\'
}
