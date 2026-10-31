package openaicompatbridge

import (
	"strings"
	"testing"
)

// 深挖复审修复批（DD-C1）钉住测试：桥泵 SSE 空行边界的 CR 形态补齐与既有
// LF / CRLF 语义回归。CRLF 空行此前经 \n\r\n 后缀模式实际可切分（回归钉住），
// 纯 CR 空行（\r\r）此前不识别、整流粘连到 EOF（修复目标）。

func TestDDC1IndexBridgeSseBoundaryForms(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		index  int
		length int
	}{
		{"LF 既有语义", "data: a\n\ntail", 7, 2},
		{"CRLF 空行经后缀命中（回归 cov_infra 口径）", "a\r\n\r\nb", 2, 3},
		{"LF+CRLF 混合", "data: a\n\r\nb", 7, 3},
		{"CR 空行（修复目标）", "data: a\r\rtail", 7, 2},
		{"无边界", "data: a\r\ndata: b\r\n", -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index, length := IndexBridgeSseBoundary(tc.input)
			if index != tc.index || length != tc.length {
				t.Fatalf("IndexBridgeSseBoundary(%q) = (%d, %d), want (%d, %d)", tc.input, index, length, tc.index, tc.length)
			}
		})
	}
}

func TestDDC1PumpBridgeSseTransformBoundaryForms(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		events []string
	}{
		{"CRLF 多事件（回归：逐事件即时切分）", "data: 1\r\n\r\ndata: 2\r\n\r\n", []string{"data: 1\r\n\r\n", "data: 2\r\n\r\n"}},
		{"CR 空行多事件（修复目标）", "data: 1\r\rdata: 2\r\r", []string{"data: 1\r\r", "data: 2\r\r"}},
		{"LF 多事件（逐字节回归）", "data: 1\n\ndata: 2\n\n", []string{"data: 1\n\n", "data: 2\n\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			err := PumpBridgeSseTransform(strings.NewReader(tc.stream), &strings.Builder{},
				func(event string) []string {
					events = append(events, event)
					return nil
				}, nil)
			if err != nil {
				t.Fatalf("pump err = %v", err)
			}
			if len(events) != len(tc.events) {
				t.Fatalf("events = %q, want %q", events, tc.events)
			}
			for index := range events {
				if events[index] != tc.events[index] {
					t.Fatalf("event[%d] = %q, want %q", index, events[index], tc.events[index])
				}
			}
		})
	}
}
