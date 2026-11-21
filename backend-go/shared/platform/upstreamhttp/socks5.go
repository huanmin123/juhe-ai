package upstreamhttp

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

// NewSOCKS5DialContext returns the historical unguarded dialer. It behaves
// exactly like NewSOCKS5DialContextWithGuard(proxyURL, remoteResolve, nil):
// when remoteResolve is true the target hostname is sent as a domain (SOCKS5H);
// otherwise a hostname is resolved locally before the CONNECT request and the
// first resolved address is used.
func NewSOCKS5DialContext(proxyURL *url.URL, remoteResolve bool) func(context.Context, string, string) (net.Conn, error) {
	return NewSOCKS5DialContextWithGuard(proxyURL, remoteResolve, nil)
}

// NewSOCKS5DialContextWithGuard returns a context-aware SOCKS5 dialer with the
// deployment URL guard applied to local resolution. When remoteResolve is true
// the target hostname is sent as a domain (SOCKS5H) and guard is ignored: the
// proxy resolves, so the local prepare-time check remains best-effort. When
// remoteResolve is false, the CONNECT address is selected through
// guard.resolveDialable; in strict mode only addresses that clear the
// blocked-range tables are sent to the proxy, and an all-blocked resolution
// fails with UnsafeResolvedUpstreamURLError instead of dialing an unvalidated
// address. A nil guard keeps the historical net.DefaultResolver first-address
// behavior; a permissive guard (AllowPrivateBaseUrls / allowlist) returns the
// historical first resolvable address.
func NewSOCKS5DialContextWithGuard(proxyURL *url.URL, remoteResolve bool, guard *DialGuard) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if proxyURL == nil || proxyURL.Host == "" {
			return nil, ErrProxyURLInvalid
		}
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("SOCKS5 unsupported network %q", network)
		}
		connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyURL.Host)
		if err != nil {
			return nil, err
		}
		stopCancellation := context.AfterFunc(ctx, func() {
			_ = connection.Close()
		})
		if err := socks5HandshakeWithGuard(ctx, connection, proxyURL, address, remoteResolve, guard); err != nil {
			stopCancellation()
			_ = connection.Close()
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
		if contextErr := ctx.Err(); contextErr != nil {
			stopCancellation()
			_ = connection.Close()
			return nil, contextErr
		}
		if !stopCancellation() {
			_ = connection.Close()
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, context.Canceled
		}
		return connection, nil
	}
}

func socks5Handshake(ctx context.Context, connection net.Conn, proxyURL *url.URL, target string, remoteResolve bool) error {
	return socks5HandshakeWithGuard(ctx, connection, proxyURL, target, remoteResolve, nil)
}

func socks5HandshakeWithGuard(ctx context.Context, connection net.Conn, proxyURL *url.URL, target string, remoteResolve bool, guard *DialGuard) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return err
		}
	}
	defer connection.SetDeadline(time.Time{})

	methods := []byte{0x00}
	username, password, hasCredentials := socksCredentials(proxyURL)
	if hasCredentials {
		methods = append(methods, 0x02)
	}
	if err := writeFull(connection, append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	var selection [2]byte
	if _, err := io.ReadFull(connection, selection[:]); err != nil {
		return err
	}
	if selection[0] != 0x05 || selection[1] == 0xff {
		return errors.New("SOCKS5 authentication method rejected")
	}
	if selection[1] == 0x02 {
		if !hasCredentials {
			return errors.New("SOCKS5 proxy requires credentials")
		}
		if len(username) > 255 || len(password) > 255 {
			return errors.New("SOCKS5 credentials exceed protocol limit")
		}
		auth := append([]byte{0x01, byte(len(username))}, []byte(username)...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, []byte(password)...)
		if err := writeFull(connection, auth); err != nil {
			return err
		}
		if _, err := io.ReadFull(connection, selection[:]); err != nil {
			return err
		}
		if selection[1] != 0x00 {
			return errors.New("SOCKS5 username/password authentication failed")
		}
	} else if selection[1] != 0x00 {
		return errors.New("SOCKS5 proxy returned an unknown authentication method")
	}

	request, err := socks5ConnectRequestWithGuard(ctx, target, remoteResolve, guard)
	if err != nil {
		return err
	}
	if err := writeFull(connection, request); err != nil {
		return err
	}
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return err
	}
	if header[0] != 0x05 || header[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT failed: reply=%d", header[1])
	}
	addressLength, err := socks5BoundAddressLength(connection, header[3])
	if err != nil {
		return err
	}
	_, err = io.CopyN(io.Discard, connection, int64(addressLength+2))
	return err
}

func socks5BoundAddressLength(connection net.Conn, addressType byte) (int, error) {
	switch addressType {
	case 0x01:
		return net.IPv4len, nil
	case 0x04:
		return net.IPv6len, nil
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return 0, err
		}
		return int(length[0]), nil
	default:
		return 0, errors.New("SOCKS5 proxy returned an unknown bound address type")
	}
}

func socksCredentials(proxyURL *url.URL) (string, string, bool) {
	if proxyURL == nil || proxyURL.User == nil {
		return "", "", false
	}
	username := proxyURL.User.Username()
	password, hasPassword := proxyURL.User.Password()
	return username, password, username != "" || hasPassword
}

func socks5ConnectRequest(ctx context.Context, target string, remoteResolve bool) ([]byte, error) {
	return socks5ConnectRequestWithGuard(ctx, target, remoteResolve, nil)
}

func socks5ConnectRequestWithGuard(ctx context.Context, target string, remoteResolve bool, guard *DialGuard) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("parse SOCKS5 target: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("SOCKS5 target port invalid")
	}
	request := []byte{0x05, 0x01, 0x00}
	if remoteResolve {
		if strings.TrimSpace(host) == "" || len(host) > 255 {
			return nil, errors.New("SOCKS5H target host invalid")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	} else {
		var ip net.IP
		if guard != nil {
			// Local resolution is address-selecting: only dialable (non-blocked)
			// addresses may be sent to the proxy. An all-blocked answer is a
			// policy rejection, never a reason to fall back to the first address.
			dialable, resolveErr := guard.resolveDialable(ctx, host, portText)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if len(dialable) == 0 {
				return nil, &net.DNSError{Err: "no such host", Name: NormalizeHostToken(host), IsNotFound: true}
			}
			ip = dialable[0]
		} else {
			ip = net.ParseIP(host)
			if ip == nil {
				resolved, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
				if lookupErr != nil || len(resolved) == 0 {
					return nil, fmt.Errorf("local SOCKS5 target resolution failed: %w", lookupErr)
				}
				ip = resolved[0].IP
			}
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else if ipv6 := ip.To16(); ipv6 != nil {
			request = append(request, 0x04)
			request = append(request, ipv6...)
		} else {
			return nil, errors.New("local SOCKS5 resolution did not return an IP")
		}
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	return append(request, portBytes...), nil
}

func writeFull(connection net.Conn, payload []byte) error {
	for len(payload) > 0 {
		written, err := connection.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}
