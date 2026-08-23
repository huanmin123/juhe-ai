package proxylatency

import (
	"context"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func TestSOCKS5DialPreservesResolutionMode(t *testing.T) {
	for _, test := range []struct {
		name, scheme, target, wantHost string
		wantType                       byte
	}{
		{name: "remote DNS", scheme: "socks5h", target: "relay.example:443", wantHost: "relay.example", wantType: 0x03},
		{name: "local IP", scheme: "socks5", target: "127.0.0.1:443", wantHost: "127.0.0.1", wantType: 0x01},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, observed := startSOCKS5Server(t)
			proxyURL, err := url.Parse(test.scheme + "://" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := upstreamhttp.NewSOCKS5DialContext(proxyURL, test.scheme == "socks5h")(ctx, "tcp", test.target)
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

func TestSOCKS5TransportMapsStoredSocks5ToRemoteDNS(t *testing.T) {
	listener, observed := startSOCKS5Server(t)
	proxyURL, err := url.Parse("socks5://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newProxyTransport(proxyURL.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := transport.DialContext(ctx, "tcp", "relay.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	actual := <-observed
	if actual.typ != 0x03 || actual.host != "relay.example" || actual.port != 443 {
		t.Fatalf("stored socks5 must use remote DNS, request=%#v", actual)
	}
}

type socksRequest struct {
	typ  byte
	host string
	port int
}

func startSOCKS5Server(t *testing.T) (net.Listener, <-chan socksRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	observed := make(chan socksRequest, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
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
		var header [4]byte
		if _, err := io.ReadFull(connection, header[:]); err != nil {
			return
		}
		request := socksRequest{typ: header[3]}
		switch header[3] {
		case 0x01:
			address := make([]byte, net.IPv4len)
			if _, err := io.ReadFull(connection, address); err != nil {
				return
			}
			request.host = net.IP(address).String()
		case 0x03:
			var length [1]byte
			if _, err := io.ReadFull(connection, length[:]); err != nil {
				return
			}
			address := make([]byte, length[0])
			if _, err := io.ReadFull(connection, address); err != nil {
				return
			}
			request.host = string(address)
		default:
			return
		}
		var port [2]byte
		if _, err := io.ReadFull(connection, port[:]); err != nil {
			return
		}
		request.port = int(port[0])<<8 | int(port[1])
		observed <- request
		_, _ = connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0})
	}()
	return listener, observed
}
