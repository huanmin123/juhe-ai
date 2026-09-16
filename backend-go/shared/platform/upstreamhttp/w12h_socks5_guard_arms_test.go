package upstreamhttp

// w12h 补充 arms：SOCKS5 握手矩阵的剩余错误/成功形状、上下文竞态臂、
// DialGuard 解析-校验-拨号回路、限长读取的剩余分支。
// 本文件补充登记的不可达语句（无法在本机确定性构造）：
//   - socks5.go NewSOCKS5DialContext 中 stopCancellation 返回 false 的分支
//     （socks5.go:47-53）：要求 ctx 在握手术语与 stop 调用之间的并发窗口内
//     完成，且 AfterFunc 关闭连接不得破坏握手，单线程测试无法确定性触发；
//   - socks5.go socks5ConnectRequest “resolution did not return an IP”：
//     LookupIPAddr 的非空结果必为 4/16 字节 IP，两个转换分支之外不可达；
//   - transport.go NewTransport scheme switch 的 default 分支：ParseProxyURL
//     已把 scheme 收敛到四种，且该守卫带保留注释（防未来 scheme 静默回落）；
//   - urlguard.go DialContext 中 dialErr == nil 的成功返回（419-422）：需要
//     对已校验的公网 IP 完成真实拨号，本地无法提供可拨通的公网地址。

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

var w12hConnErr = errors.New("w12h 注入连接错误")

// w12hFakeConn 脚本化 net.Conn：按预定字节回放读取，按序号注入写失败。
type w12hFakeConn struct {
	script         []byte
	writes         []byte
	writeCalls     int
	failWriteAt    int // 1 基（按写调用次数）；命中即返回错误
	zeroWriteAt    int // 1 基（按写调用次数）；命中即返回 (0, nil)
	setDeadlineErr error
	closed         bool
}

func (c *w12hFakeConn) Read(p []byte) (int, error) {
	if len(c.script) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.script)
	c.script = c.script[n:]
	return n, nil
}

func (c *w12hFakeConn) Write(p []byte) (int, error) {
	c.writeCalls++
	if c.failWriteAt == c.writeCalls {
		return 0, w12hConnErr
	}
	if c.zeroWriteAt == c.writeCalls {
		c.writes = append(c.writes, p...)
		return 0, nil
	}
	c.writes = append(c.writes, p...)
	return len(p), nil
}

func (c *w12hFakeConn) Close() error                       { c.closed = true; return nil }
func (c *w12hFakeConn) LocalAddr() net.Addr                { return nil }
func (c *w12hFakeConn) RemoteAddr() net.Addr               { return nil }
func (c *w12hFakeConn) SetDeadline(time.Time) error        { return c.setDeadlineErr }
func (c *w12hFakeConn) SetReadDeadline(time.Time) error    { return c.setDeadlineErr }
func (c *w12hFakeConn) SetWriteDeadline(time.Time) error   { return c.setDeadlineErr }

// w12hFakeCtx 白盒上下文：Deadline/Err/Done 允许不一致组合，用于确定性
// 命中拨号器在握手成功后检查 ctx.Err() 的分支。
type w12hFakeCtx struct {
	deadline time.Time
	err      error
	done     chan struct{}
}

func (c *w12hFakeCtx) Deadline() (time.Time, bool)    { return c.deadline, !c.deadline.IsZero() }
func (c *w12hFakeCtx) Done() <-chan struct{}          { return c.done }
func (c *w12hFakeCtx) Err() error                     { return c.err }
func (c *w12hFakeCtx) Value(any) any                  { return nil }

// 握手成功形状（无凭据 + ATYP=0x04 绑定地址）。
func w12hSuccessScript(atyp byte) []byte {
	script := []byte{0x05, 0x00}
	script = append(script, 0x05, 0x00, 0x00, atyp)
	if atyp == 0x04 {
		script = append(script, make([]byte, 16)...)
	} else if atyp == 0x01 {
		script = append(script, 10, 0, 0, 1)
	}
	script = append(script, 0x1f, 0x90)
	return script
}

func w12hHandshakeProxyURL(credentials string) *url.URL {
	raw := "socks5://" + credentials + "proxy.internal:1080"
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestW12HSOCKS5HandshakeArms(t *testing.T) {
	ctx := context.Background()

	t.Run("greeting 写失败", func(t *testing.T) {
		conn := &w12hFakeConn{failWriteAt: 1}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); !errors.Is(err, w12hConnErr) {
			t.Fatalf("greeting 写失败必须传播: %v", err)
		}
	})

	t.Run("要求凭据但客户端没有", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x02}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); err == nil || !strings.Contains(err.Error(), "requires credentials") {
			t.Fatalf("代理要求凭据必须失败: %v", err)
		}
	})

	t.Run("凭据超长", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x02}}
		long := strings.Repeat("u", 300)
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(long+":p@"), "example.invalid:80", true); err == nil || !strings.Contains(err.Error(), "exceed protocol limit") {
			t.Fatalf("超长凭据必须失败: %v", err)
		}
	})

	t.Run("认证子协商写失败", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x02}, failWriteAt: 2}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL("u:p@"), "example.invalid:80", true); !errors.Is(err, w12hConnErr) {
			t.Fatalf("认证写失败必须传播: %v", err)
		}
	})

	t.Run("认证应答不完整", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x02, 0x05}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL("u:p@"), "example.invalid:80", true); err == nil {
			t.Fatal("认证应答不完整必须失败")
		}
	})

	t.Run("未知认证方法", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x09}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); err == nil || !strings.Contains(err.Error(), "unknown authentication method") {
			t.Fatalf("未知方法必须失败: %v", err)
		}
	})

	t.Run("目标缺少端口", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x00}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "no-port", true); err == nil || !strings.Contains(err.Error(), "parse SOCKS5 target") {
			t.Fatalf("缺端口目标必须失败: %v", err)
		}
	})

	t.Run("请求写失败", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x00}, failWriteAt: 2}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); !errors.Is(err, w12hConnErr) {
			t.Fatalf("请求写失败必须传播: %v", err)
		}
	})

	t.Run("应答头不完整", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x00, 0x05, 0x00}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); err == nil {
			t.Fatal("应答头不完整必须失败")
		}
	})

	t.Run("ATYP=0x03 域名长度读取中断", func(t *testing.T) {
		conn := &w12hFakeConn{script: []byte{0x05, 0x00, 0x05, 0x00, 0x00, 0x03}}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); err == nil {
			t.Fatal("域名长度读取中断必须失败")
		}
	})

	t.Run("ATYP=0x04 IPv6 绑定地址成功", func(t *testing.T) {
		conn := &w12hFakeConn{script: w12hSuccessScript(0x04)}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); err != nil {
			t.Fatalf("IPv6 绑定地址必须成功: %v", err)
		}
	})

	t.Run("写返回零字节", func(t *testing.T) {
		conn := &w12hFakeConn{zeroWriteAt: 1}
		if err := socks5Handshake(ctx, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("零字节写必须报短写: %v", err)
		}
	})

	t.Run("SetDeadline 失败", func(t *testing.T) {
		conn := &w12hFakeConn{setDeadlineErr: w12hConnErr}
		fake := &w12hFakeCtx{deadline: time.Now().Add(time.Hour)}
		if err := socks5Handshake(fake, conn, w12hHandshakeProxyURL(""), "example.invalid:80", true); !errors.Is(err, w12hConnErr) {
			t.Fatalf("SetDeadline 失败必须传播: %v", err)
		}
	})
}

// 握手成功后 ctx 已过期：拨号器必须放弃连接并返回上下文错误。
func TestW12HSOCKS5DialContextExpiredAfterHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			// 回应无凭据握手：问候 → 选择 → 请求头 → 立即回成功应答
			// （客户端写完请求即阻塞等应答，不能先等请求尾部）。
			greeting := make([]byte, 2)
			if _, err := io.ReadFull(conn, greeting); err != nil {
				_ = conn.Close()
				return
			}
			if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
				_ = conn.Close()
				return
			}
			request := make([]byte, 4)
			if _, err := io.ReadFull(conn, request); err != nil {
				_ = conn.Close()
				return
			}
			_, _ = conn.Write(w12hSuccessScript(0x01))
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}()
	}()
	proxy, _ := url.Parse("socks5://" + listener.Addr().String())
	dial := NewSOCKS5DialContext(proxy, true)
	// Done()==nil 保证 AfterFunc 永不关闭连接，握手可以完整跑完；
	// Err() 恒为 DeadlineExceeded，命中握手成功后的 ctx 检查分支。
	fake := &w12hFakeCtx{deadline: time.Now().Add(time.Hour), err: context.DeadlineExceeded}
	conn, dialErr := dial(fake, "tcp", "example.invalid:8080")
	if dialErr != context.DeadlineExceeded {
		t.Fatalf("握手后过期的 ctx 必须返回上下文错误: %v", dialErr)
	}
	if conn != nil {
		t.Fatal("不得返回连接")
	}
}

// 拨号阶段连接失败必须原样传播。
func TestW12HSOCKS5DialProxyRefused(t *testing.T) {
	proxy, _ := url.Parse("socks5://127.0.0.1:1")
	dial := NewSOCKS5DialContext(proxy, true)
	if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil {
		t.Fatal("代理拒连必须失败")
	}
}

// 本地解析路径：域名解析成功、解析失败与 IPv6 字面量。
func TestW12HSOCKS5ConnectRequestLocalResolution(t *testing.T) {
	if _, err := socks5ConnectRequest(context.Background(), "localhost:80", false); err != nil {
		t.Fatalf("localhost 本地解析必须成功: %v", err)
	}
	if _, err := socks5ConnectRequest(context.Background(), "w12h-nonexistent.invalid:80", false); err == nil || !strings.Contains(err.Error(), "resolution failed") {
		t.Fatalf("不可解析域名必须失败: %v", err)
	}
	request, err := socks5ConnectRequest(context.Background(), "[::1]:80", false)
	if err != nil {
		t.Fatalf("IPv6 字面量必须成功: %v", err)
	}
	if request[3] != 0x04 {
		t.Fatalf("IPv6 必须使用 ATYP=0x04: % x", request[:4])
	}
}

// 限长读取剩余分支。
func TestW12HBoundedReaderArms(t *testing.T) {
	if _, err := ReadBounded(w7cErrReader{}, 8); err == nil || strings.Contains(err.Error(), "limit") {
		t.Fatalf("读取错误必须原样传播: %v", err)
	}
	body, err := ReadBoundedPartial(strings.NewReader("ok"), 10)
	if err != nil || string(body) != "ok" {
		t.Fatalf("限额内的部分读取必须成功: %q %v", body, err)
	}
	// ReadAll 命中 EOF 后，Drain 再次读取时失败。
	drainFail := &w12hMultiErrReader{steps: []step{{data: "ab"}, {err: io.EOF}, {err: w12hConnErr}}}
	if _, err := ReadAndDrainBounded(drainFail, 10); !errors.Is(err, w12hConnErr) {
		t.Fatalf("Drain 失败必须传播: %v", err)
	}
}

type step struct {
	data string
	err  error
}

type w12hMultiErrReader struct {
	steps []step
	index int
}

func (r *w12hMultiErrReader) Read(p []byte) (int, error) {
	if r.index >= len(r.steps) {
		return 0, io.EOF
	}
	current := r.steps[r.index]
	r.index++
	if current.err != nil {
		return 0, current.err
	}
	return copy(p, current.data), nil
}

// 客户端池：传输构造错误传播。
func TestW12HClientPoolTransportError(t *testing.T) {
	pool := NewClientPoolWithLimit(2)
	if _, err := pool.Client("ftp://proxy.invalid", TransportOptions{}); !errors.Is(err, ErrProxySchemeUnsupported) {
		t.Fatalf("非法 scheme 必须失败: %v", err)
	}
}

// 传输构造剩余分支。
func TestW12HTransportArms(t *testing.T) {
	if _, err := ParseProxyURL("   "); !errors.Is(err, ErrProxyURLInvalid) {
		t.Fatalf("空白代理 URL 必须拒绝: %v", err)
	}
	transport, err := NewTransport("", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transport.MaxResponseHeaderBytes != DefaultMaxResponseHeaderBytes {
		t.Fatalf("缺省响应头上限不符: %d", transport.MaxResponseHeaderBytes)
	}
	transport, err = NewTransport("", TransportOptions{ProxyConnectHeader: http.Header{"X-W12h": {"v"}}})
	if err != nil || transport.ProxyConnectHeader == nil {
		t.Fatalf("ProxyConnectHeader 必须克隆: %v", err)
	}
	if _, err := NewClient("ftp://proxy.invalid", TransportOptions{}); !errors.Is(err, ErrProxySchemeUnsupported) {
		t.Fatalf("NewClient 错误必须传播: %v", err)
	}
	client := NewClientWithTransport(nil)
	if client.Transport != http.DefaultTransport {
		t.Fatal("nil 传输必须回退默认传输")
	}
}

// URL 安全原语剩余分支。
func TestW12HURLGuardArms(t *testing.T) {
	if got := (&UnsafeUpstreamURLError{Message: "m"}).Error(); got != "m" {
		t.Fatalf("静态错误消息不符: %q", got)
	}
	if got := (&UnsafeResolvedUpstreamURLError{Message: "r"}).Error(); got != "r" {
		t.Fatalf("解析错误消息不符: %q", got)
	}
	// 恶意区间表输入触发 panic。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("非法 IPv4 区间必须 panic")
			}
		}()
		mustBlockedIpv4Ranges([]blockedRangeInput{{address: "not-an-ip", prefixLength: 8}})
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("非法 IPv6 区间必须 panic")
			}
		}()
		mustBlockedIpv6Ranges([]blockedRangeInput{{address: "not-an-ip", prefixLength: 8}})
	}()
	// 前缀完整匹配的提前返回。
	if !ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{1, 2, 3, 4}, 32) {
		t.Fatal("完整前缀必须匹配")
	}
	// https 无端口 origin 补默认 443。
	if got := UpstreamOriginKey(&url.URL{Scheme: "https", Host: "example.com"}); got != "https://example.com:443" {
		t.Fatalf("https origin 不符: %q", got)
	}
	// allowlist 里的非法 origin 被跳过。
	guard := NewDialGuard(URLSecurityConfig{PrivateOriginAllowlist: map[string]bool{"not-a-origin": true}}, nil, nil)
	if guard.allowedHosts["not-a-origin"] {
		t.Fatal("非法 origin 不得进入 allowlist")
	}
	// 端口级 allowlist 命中。
	guard = NewDialGuard(URLSecurityConfig{PrivateOriginAllowlist: map[string]bool{"http://10.0.0.1:8443": true}}, nil, nil)
	if err := guard.ValidateHost(context.Background(), "10.0.0.1", "8443"); err != nil {
		t.Fatalf("端口级 allowlist 必须放行: %v", err)
	}
}

type w12hMapResolver map[string][]net.IPAddr

func (r w12hMapResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if addrs, ok := r[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// DialGuard 回路：字面量直拨、nil IP 跳过、拨号失败聚合与无可拨地址。
func TestW12HDialGuardLoopArms(t *testing.T) {
	ctx := context.Background()
	dialer := &net.Dialer{Timeout: 100 * time.Millisecond}

	// 非封锁公网字面量：校验通过后直接拨（拨号失败原样返回）。
	guard := NewDialGuard(URLSecurityConfig{}, nil, dialer)
	if _, err := guard.DialContext(ctx, "tcp", "1.2.3.4:1"); err == nil {
		t.Fatal("1.2.3.4:1 拨号必须失败")
	}

	// 解析结果全为 nil IP：跳过后无可拨地址，命中兜底错误。
	guard = NewDialGuard(URLSecurityConfig{}, w12hMapResolver{"w12h-nil.example": {{IP: nil}, {IP: nil}}}, dialer)
	_, err := guard.DialContext(ctx, "tcp", "w12h-nil.example:80")
	if err == nil || !strings.Contains(err.Error(), "no dialable validated address") {
		t.Fatalf("全 nil IP 必须报无可拨地址: %v", err)
	}
	if err := guard.ValidateHost(ctx, "w12h-nil.example", "80"); err != nil {
		t.Fatalf("ValidateHost 必须跳过 nil IP: %v", err)
	}

	// 多个公网候选全部拨号失败：返回最后一次错误。
	guard = NewDialGuard(URLSecurityConfig{}, w12hMapResolver{"w12h-fail.example": {{IP: net.ParseIP("1.2.3.4")}, {IP: net.ParseIP("1.2.3.5")}}}, dialer)
	if _, err = guard.DialContext(ctx, "tcp", "w12h-fail.example:1"); err == nil {
		t.Fatal("全部候选拨号失败必须返回错误")
	}
	if strings.Contains(err.Error(), "no dialable") {
		t.Fatalf("应返回最后一次拨号错误而非兜底: %v", err)
	}

	// nil IP 混入候选时被跳过，剩余候选全部失败。
	guard = NewDialGuard(URLSecurityConfig{}, w12hMapResolver{"w12h-mixed.example": {{IP: nil}, {IP: net.ParseIP("1.2.3.4")}}}, dialer)
	if _, err = guard.DialContext(ctx, "tcp", "w12h-mixed.example:1"); err == nil {
		t.Fatal("混合候选拨号失败必须返回错误")
	}
}
