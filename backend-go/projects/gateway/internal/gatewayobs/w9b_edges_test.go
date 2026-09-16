package gatewayobs

// w9b UTF-8 解码器尾部、行终止符判定与 store 缺失臂。

import (
	"strings"
	"testing"
)

func TestW9BIncompleteUTF8SequenceLength(t *testing.T) {
	cases := []struct {
		input []byte
		want  int
	}{
		{nil, 0},
		{[]byte{0xC3}, 1},             // 2 字节序列缺 1 字节。
		{[]byte{0xE4, 0xB8}, 2},       // 3 字节序列缺 1 字节。
		{[]byte{0xF0, 0x9F, 0x98}, 3}, // 4 字节序列缺 1 字节。
		{[]byte{0xE4, 0xB8, 0xAD}, 0}, // 完整序列 → 0。
		{[]byte{0x41}, 0},             // ASCII 起始 → 0。
		{[]byte{0xE4, 0x41}, 0},       // 后续字节非法 → 0。
		{[]byte{0x80}, 0},             // 裸 continuation → 0。
	}
	for _, tc := range cases {
		if got := incompleteUTF8SequenceLength(tc.input); got != tc.want {
			t.Fatalf("incompleteUTF8SequenceLength(% x)=%d want %d", tc.input, got, tc.want)
		}
	}
}

func TestW9BUtf8StringDecoderEnd(t *testing.T) {
	decoder := &utf8StringDecoder{}
	if got := decoder.end(); got != "" {
		t.Fatalf("空 pending=%q", got)
	}
	decoder.pending = []byte{0xE4, 0xB8}
	if got := decoder.end(); got != "\uFFFD" {
		t.Fatalf("悬挂 pending=%q", got)
	}
	if decoder.pending != nil {
		t.Fatal("end 后 pending 必须清空")
	}
}

func TestW9BIsJSLineTerminatorAt(t *testing.T) {
	if !isJSLineTerminatorAt("a\nb", 1) {
		t.Fatal("LF 必须识别")
	}
	if !isJSLineTerminatorAt("a\rb", 1) {
		t.Fatal("CR 必须识别")
	}
	ls := "a b"
	ps := "a b"
	if !isJSLineTerminatorAt(ls, 1) {
		t.Fatal("U+2028 必须识别")
	}
	if !isJSLineTerminatorAt(ps, 1) {
		t.Fatal("U+2029 必须识别")
	}
	if isJSLineTerminatorAt("ab", 1) {
		t.Fatal("普通字符不应识别")
	}
	if isJSLineTerminatorAt("a\xe2\x80", 1) {
		t.Fatal("截断的 U+2028 序列不应识别")
	}
}

func TestW9BStoreUnavailableError(t *testing.T) {
	observer := &Observer{}
	err := observer.storeUnavailableError()
	if err == nil || !strings.Contains(err.Error(), "未注入") {
		t.Fatalf("storeUnavailableError=%v", err)
	}
}
