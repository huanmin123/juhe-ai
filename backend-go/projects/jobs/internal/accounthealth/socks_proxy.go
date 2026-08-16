package accounthealth

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// newSOCKS5DialContext is intentionally local to J1: it supplies an explicit
// bounded SOCKS5/SOCKS5H transport without importing Gateway code or adding a
// hidden proxy fallback. socks5 resolves the target locally; socks5h sends the
// target host to the proxy for resolution.
func newSOCKS5DialContext(proxyURL *url.URL, remoteResolve bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("SOCKS5 不支持网络 %q", network)
		}
		dialer := &net.Dialer{}
		connection, err := dialer.DialContext(ctx, "tcp", proxyURL.Host)
		if err != nil {
			return nil, err
		}
		if err := socks5Handshake(ctx, connection, proxyURL, address, remoteResolve); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return connection, nil
	}
}

func socks5Handshake(ctx context.Context, connection net.Conn, proxyURL *url.URL, target string, remoteResolve bool) error {
	if err := setHandshakeDeadline(ctx, connection); err != nil {
		return err
	}
	defer connection.SetDeadline(timeZero)
	methods := []byte{0x00}
	username, password, hasCredentials := socksCredentials(proxyURL)
	if hasCredentials {
		methods = append(methods, 0x02)
	}
	if _, err := connection.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	var selection [2]byte
	if _, err := io.ReadFull(connection, selection[:]); err != nil {
		return err
	}
	if selection[0] != 0x05 || selection[1] == 0xff {
		return errors.New("SOCKS5 代理拒绝认证方法")
	}
	if selection[1] == 0x02 {
		if !hasCredentials {
			return errors.New("SOCKS5 代理要求用户名密码")
		}
		if len(username) > 255 || len(password) > 255 {
			return errors.New("SOCKS5 用户名或密码超过长度限制")
		}
		request := append([]byte{0x01, byte(len(username))}, []byte(username)...)
		request = append(request, byte(len(password)))
		request = append(request, []byte(password)...)
		if _, err := connection.Write(request); err != nil {
			return err
		}
		if _, err := io.ReadFull(connection, selection[:]); err != nil {
			return err
		}
		if selection[1] != 0x00 {
			return errors.New("SOCKS5 用户名密码认证失败")
		}
	} else if selection[1] != 0x00 {
		return errors.New("SOCKS5 返回未知认证方法")
	}
	request, err := socks5ConnectRequest(ctx, target, remoteResolve)
	if err != nil {
		return err
	}
	if _, err := connection.Write(request); err != nil {
		return err
	}
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return err
	}
	if header[0] != 0x05 || header[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT 失败：reply=%d", header[1])
	}
	addressLength := 0
	switch header[3] {
	case 0x01:
		addressLength = net.IPv4len
	case 0x04:
		addressLength = net.IPv6len
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return err
		}
		addressLength = int(length[0])
	default:
		return errors.New("SOCKS5 返回未知绑定地址类型")
	}
	if _, err := io.CopyN(io.Discard, connection, int64(addressLength+2)); err != nil {
		return err
	}
	return nil
}

var timeZero = time.Time{}

func setHandshakeDeadline(ctx context.Context, connection net.Conn) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return connection.SetDeadline(deadline)
}

func socksCredentials(proxyURL *url.URL) (string, string, bool) {
	if proxyURL.User == nil {
		return "", "", false
	}
	username := proxyURL.User.Username()
	password, hasPassword := proxyURL.User.Password()
	return username, password, username != "" || hasPassword
}

func socks5ConnectRequest(ctx context.Context, target string, remoteResolve bool) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("解析 SOCKS5 目标失败: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("SOCKS5 目标端口无效")
	}
	request := []byte{0x05, 0x01, 0x00}
	if !remoteResolve {
		if ip := net.ParseIP(host); ip != nil {
			return appendSOCKSAddress(request, ip, uint16(port))
		}
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(resolved) == 0 {
			return nil, fmt.Errorf("SOCKS5 本地解析目标失败: %w", err)
		}
		return appendSOCKSAddress(request, resolved[0].IP, uint16(port))
	}
	if strings.TrimSpace(host) == "" || len(host) > 255 {
		return nil, errors.New("SOCKS5H 目标主机无效")
	}
	request = append(request, 0x03, byte(len(host)))
	request = append(request, []byte(host)...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	return append(request, portBytes...), nil
}

func appendSOCKSAddress(request []byte, ip net.IP, port uint16) ([]byte, error) {
	if ipv4 := ip.To4(); ipv4 != nil {
		request = append(request, 0x01)
		request = append(request, ipv4...)
	} else if ipv6 := ip.To16(); ipv6 != nil {
		request = append(request, 0x04)
		request = append(request, ipv6...)
	} else {
		return nil, errors.New("SOCKS5 本地解析结果不是 IP 地址")
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, port)
	return append(request, portBytes...), nil
}
