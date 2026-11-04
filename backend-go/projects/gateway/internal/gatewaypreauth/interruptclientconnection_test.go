package gatewaypreauth

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// interruptFakeConn 记录 Close 次数，测试交接是否真的断开连接。
type interruptFakeConn struct {
	closeCalls int
}

func (c *interruptFakeConn) Read([]byte) (int, error)                  { return 0, errors.New("closed") }
func (c *interruptFakeConn) Write(p []byte) (int, error)               { return len(p), nil }
func (c *interruptFakeConn) Close() error                              { c.closeCalls++; return nil }
func (c *interruptFakeConn) LocalAddr() net.Addr                       { return fakeAddr{} }
func (c *interruptFakeConn) RemoteAddr() net.Addr                      { return fakeAddr{} }
func (c *interruptFakeConn) SetDeadline(deadline time.Time) error      { return nil }
func (c *interruptFakeConn) SetReadDeadline(deadline time.Time) error  { return nil }
func (c *interruptFakeConn) SetWriteDeadline(deadline time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }

// interruptFakeWriter 是可 Hijack 的底层 writer；hijackErr 非 nil 模拟
// 能力存在但运行期 Hijack 失败。
type interruptFakeWriter struct {
	header      http.Header
	conn        *interruptFakeConn
	hijackCalls int
	hijackErr   error
}

func (w *interruptFakeWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *interruptFakeWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *interruptFakeWriter) WriteHeader(int)             {}
func (w *interruptFakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijackCalls++
	if w.hijackErr != nil {
		return nil, nil, w.hijackErr
	}
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// 底层支持 Hijack：交接成功、连接被关闭、TrackingWriter 进入已结束/已销毁。
func TestInterruptClientConnectionHijacksAndCloses(t *testing.T) {
	conn := &interruptFakeConn{}
	fake := &interruptFakeWriter{conn: conn}
	res := NewTrackingWriter(fake)

	if !InterruptClientConnection(res) {
		t.Fatal("底层支持 Hijack 时必须断开成功")
	}
	if fake.hijackCalls != 1 || conn.closeCalls != 1 {
		t.Fatalf("hijackCalls=%d closeCalls=%d", fake.hijackCalls, conn.closeCalls)
	}
	if !res.WritableEnded() {
		t.Fatal("断开成功后必须置 WritableEnded，阻止后续补写")
	}
}

// 底层不支持 Hijack：不得走 TrackingWriter.Hijack（它会在失败前置
// wroteHeader/101），否则 503 回退会被 HeadersSent 守卫吞掉。
func TestInterruptClientConnectionWithoutHijackerKeepsHeadersUnsent(t *testing.T) {
	res := NewTrackingWriter(httptest.NewRecorder())

	if InterruptClientConnection(res) {
		t.Fatal("底层不支持 Hijack 时必须返回 false")
	}
	if res.HeadersSent() {
		t.Fatal("失败路径不得置 HeadersSent")
	}
	if res.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d，失败路径不得改写状态码", res.StatusCode())
	}
}

// Hijack 能力存在但运行期失败：同样不得污染 wroteHeader，回退 503 仍可写出。
func TestInterruptClientConnectionHijackErrorKeepsHeadersUnsent(t *testing.T) {
	fake := &interruptFakeWriter{conn: &interruptFakeConn{}, hijackErr: errors.New("hijack refused")}
	res := NewTrackingWriter(fake)

	if InterruptClientConnection(res) {
		t.Fatal("Hijack 失败时必须返回 false")
	}
	if res.HeadersSent() {
		t.Fatal("Hijack 失败不得置 HeadersSent")
	}
	if res.WritableEnded() {
		t.Fatal("Hijack 失败不得置 WritableEnded")
	}
}
