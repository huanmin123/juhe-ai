package upstreamhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadBoundedRejectsOverflow(t *testing.T) {
	if _, err := ReadBounded(strings.NewReader("12345"), 4); !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestReadAndDrainBoundedRetainsPrefixAndConsumesSuffix(t *testing.T) {
	reader := &trackingReader{reader: strings.NewReader("123456")}
	body, err := ReadAndDrainBounded(reader, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "1234" || reader.readBytes != 6 {
		t.Fatalf("body=%q readBytes=%d", body, reader.readBytes)
	}
}

type trackingReader struct {
	reader    io.Reader
	readBytes int
}

func (reader *trackingReader) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	reader.readBytes += read
	return read, err
}

func TestNewTransportDisablesEnvironmentProxyAndEnablesHTTP2(t *testing.T) {
	transport, err := NewTransport("", TransportOptions{ResponseHeaderTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil {
		t.Fatal("direct transport must not consult environment proxy settings")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("shared upstream transport must enable HTTP/2")
	}
	if transport.ResponseHeaderTimeout != time.Second || transport.MaxResponseHeaderBytes != DefaultMaxResponseHeaderBytes {
		t.Fatalf("transport limits=%s/%d", transport.ResponseHeaderTimeout, transport.MaxResponseHeaderBytes)
	}
}

func TestNewTransportRejectsUnsupportedProxyInsteadOfFallingBack(t *testing.T) {
	for _, raw := range []string{"", "ftp://proxy.example:21", "http:///missing-host", "http://proxy.example:8080#fragment"} {
		if raw == "" {
			continue
		}
		if _, err := NewTransport(raw, TransportOptions{}); err == nil {
			t.Fatalf("proxy %q unexpectedly accepted", raw)
		}
	}
}

func TestNewClientDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient("", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status=%d, want=%d", response.StatusCode, http.StatusFound)
	}
}

func TestClientPoolReusesKeepAliveTransport(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	defer server.Close()

	pool := NewClientPoolWithLimit(2)
	client, err := pool.Client("", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connection count=%d, want one reused keep-alive connection", got)
	}
}

func TestZeroValueClientPoolIsUsable(t *testing.T) {
	var pool ClientPool
	client, err := pool.Client("", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("zero-value client pool returned nil client")
	}
}

func TestNewTransportUsesProxyConnectHeaders(t *testing.T) {
	transport, err := NewTransport("http://proxy.example:8080", TransportOptions{ProxyConnectHeader: http.Header{"Proxy-Connection": []string{"close"}}})
	if err != nil {
		t.Fatal(err)
	}
	if transport.ProxyConnectHeader.Get("Proxy-Connection") != "close" {
		t.Fatalf("proxy connect headers=%v", transport.ProxyConnectHeader)
	}
}

func TestSOCKS5DialPreservesLocalAndRemoteResolutionModes(t *testing.T) {
	for _, test := range []struct {
		name, scheme, target, wantHost string
		wantType                       byte
		remote                         bool
	}{
		{name: "remote DNS", scheme: "socks5h", target: "relay.example:443", wantHost: "relay.example", wantType: 0x03, remote: true},
		{name: "local IP", scheme: "socks5", target: "127.0.0.1:443", wantHost: "127.0.0.1", wantType: 0x01},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, observed := startSOCKS5TestServer(t)
			proxyURL, err := url.Parse(test.scheme + "://" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := NewSOCKS5DialContext(proxyURL, test.remote)(ctx, "tcp", test.target)
			if err != nil {
				t.Fatal(err)
			}
			_ = connection.Close()
			actual := <-observed
			if actual.typ != test.wantType || actual.host != test.wantHost || actual.port != 443 {
				t.Fatalf("SOCKS request=%#v", actual)
			}
		})
	}
}

func TestSOCKS5DialAuthenticates(t *testing.T) {
	listener, observed := startAuthenticatedSOCKS5TestServer(t)
	proxyURL, err := url.Parse("socks5://user:pass@" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := NewSOCKS5DialContext(proxyURL, true)(ctx, "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if got := <-observed; got != "user:pass" {
		t.Fatalf("credentials=%q", got)
	}
}

func TestSOCKS5DialContextCancellationInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	proxyURL, err := url.Parse("socks5://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		connection, dialErr := NewSOCKS5DialContext(proxyURL, false)(ctx, "tcp", "example.test:443")
		if connection != nil {
			_ = connection.Close()
		}
		result <- dialErr
	}()

	var serverConnection net.Conn
	select {
	case serverConnection = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 proxy did not accept the connection")
	}
	cancel()
	select {
	case dialErr := <-result:
		if !errors.Is(dialErr, context.Canceled) {
			t.Fatalf("dial error=%v, want context cancellation", dialErr)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 handshake did not stop after context cancellation")
	}
	_ = serverConnection.Close()
}

type socksRequest struct {
	typ  byte
	host string
	port int
}

func startSOCKS5TestServer(t *testing.T) (net.Listener, <-chan socksRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	observed := make(chan socksRequest, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var greeting [2]byte
		if _, err := io.ReadFull(connection, greeting[:]); err != nil {
			return
		}
		methods := make([]byte, greeting[1])
		if _, err := io.ReadFull(connection, methods); err != nil {
			return
		}
		if _, err := connection.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
		request, ok := readSOCKS5Request(connection)
		if !ok {
			return
		}
		observed <- request
		_, _ = connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
	}()
	return listener, observed
}

func startAuthenticatedSOCKS5TestServer(t *testing.T) (net.Listener, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	observed := make(chan string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var greeting [2]byte
		if _, err := io.ReadFull(connection, greeting[:]); err != nil {
			return
		}
		methods := make([]byte, greeting[1])
		if _, err := io.ReadFull(connection, methods); err != nil {
			return
		}
		if _, err := connection.Write([]byte{0x05, 0x02}); err != nil {
			return
		}
		var authHeader [2]byte
		if _, err := io.ReadFull(connection, authHeader[:]); err != nil {
			return
		}
		username := make([]byte, authHeader[1])
		if _, err := io.ReadFull(connection, username); err != nil {
			return
		}
		var passwordLength [1]byte
		if _, err := io.ReadFull(connection, passwordLength[:]); err != nil {
			return
		}
		password := make([]byte, passwordLength[0])
		if _, err := io.ReadFull(connection, password); err != nil {
			return
		}
		if _, err := connection.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
		if _, ok := readSOCKS5Request(connection); !ok {
			return
		}
		observed <- string(username) + ":" + string(password)
		_, _ = connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
	}()
	return listener, observed
}

func readSOCKS5Request(connection net.Conn) (socksRequest, bool) {
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return socksRequest{}, false
	}
	request := socksRequest{typ: header[3]}
	switch header[3] {
	case 0x01:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(connection, address); err != nil {
			return socksRequest{}, false
		}
		request.host = net.IP(address).String()
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return socksRequest{}, false
		}
		address := make([]byte, length[0])
		if _, err := io.ReadFull(connection, address); err != nil {
			return socksRequest{}, false
		}
		request.host = string(address)
	default:
		return socksRequest{}, false
	}
	var port [2]byte
	if _, err := io.ReadFull(connection, port[:]); err != nil {
		return socksRequest{}, false
	}
	request.port = int(port[0])<<8 | int(port[1])
	return request, true
}
