package openaicompatbridge

import (
	"io"
	"strings"
)

// BridgeSsePump pumps an upstream SSE body through a per-event transform and
// writes the rendered events to dst (Node's per-bridge
// `for await (const chunk of upstreamBody) { takeCompleteSseEvents(...); for
// (const output of process(event)) yield ... }` generators).

// IndexBridgeSseBoundary finds the first SSE empty-line boundary and returns
// the index of the boundary's leading byte together with the full boundary
// length. 词表：\n\n、\n\r\n、\r\n\r\n（经 \n\r\n 后缀命中，index 指向前一
// 终止符的 \n）与 \r\r（DD-C1 补齐，此前纯 CR 空行不识别、整流粘连到 EOF）；
// 边界判定只认空行序列，事件内容行尾 \r\n 不触发。
func IndexBridgeSseBoundary(value string) (int, int) {
	for i := 0; i < len(value); i++ {
		if value[i] == '\r' {
			if i+1 < len(value) && value[i+1] == '\r' {
				return i, 2
			}
			continue
		}
		if value[i] != '\n' {
			continue
		}
		length := 1
		j := i + 1
		if j < len(value) && value[j] == '\r' {
			j++
			length++
		}
		if j >= len(value) || value[j] != '\n' {
			continue
		}
		return i, length + 1
	}
	return -1, 0
}

// PumpBridgeSseTransform consumes src until EOF, splits complete SSE events
// (keeping the boundary inside the event text, mirroring
// takeCompleteSseEvents), feeds every event through process, writes the
// outputs to dst, then flushes the trailing buffer through process and the
// finish() outputs. process/finish may be nil (pass-through).
func PumpBridgeSseTransform(src io.Reader, dst io.Writer, process func(eventText string) []string, finish func() []string) error {
	if process == nil {
		process = func(string) []string { return nil }
	}
	var pending strings.Builder
	buf := make([]byte, 32*1024)
	writeOutputs := func(outputs []string) error {
		for _, output := range outputs {
			if _, err := io.WriteString(dst, output); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			pending.Write(buf[:n])
			for {
				text := pending.String()
				index, length := IndexBridgeSseBoundary(text)
				if index < 0 {
					break
				}
				event := text[:index+length]
				pending.Reset()
				pending.WriteString(text[index+length:])
				if err := writeOutputs(process(event)); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	if rest := strings.TrimSpace(pending.String()); rest != "" {
		if err := writeOutputs(process(rest)); err != nil {
			return err
		}
	}
	if finish != nil {
		if err := writeOutputs(finish()); err != nil {
			return err
		}
	}
	return nil
}
