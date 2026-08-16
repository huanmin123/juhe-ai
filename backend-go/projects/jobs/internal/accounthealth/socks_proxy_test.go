package accounthealth

import (
	"context"
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestSOCKS5HDialSendsDomainToProxy(t *testing.T) {
	listener, observed := startSOCKS5TestServer(t)
	proxyURL, err := url.Parse("socks5h://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := newSOCKS5DialContext(proxyURL, true)(ctx, "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	value := <-observed
	if value.atyp != 0x03 || value.host != "example.test" || value.port != 443 {
		t.Fatalf("unexpected SOCKS5H request: %#v", value)
	}
}

func TestSOCKS5DialSendsLocalIPToProxy(t *testing.T) {
	listener, observed := startSOCKS5TestServer(t)
	proxyURL, err := url.Parse("socks5://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := newSOCKS5DialContext(proxyURL, false)(ctx, "tcp", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	value := <-observed
	if value.atyp != 0x01 || value.host != "127.0.0.1" || value.port != 443 {
		t.Fatalf("unexpected SOCKS5 request: %#v", value)
	}
}

type socksRequest struct {
	atyp byte
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
		var header [4]byte
		if _, err := io.ReadFull(connection, header[:]); err != nil {
			return
		}
		request := socksRequest{atyp: header[3]}
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
