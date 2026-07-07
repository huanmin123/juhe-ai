package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/config"
)

type clientIPResolver struct {
	trustProxy config.TrustProxyConfig
}

func newClientIPResolver(cfg config.Config) clientIPResolver {
	trustProxy, err := cfg.TrustProxyConfig()
	if err != nil {
		panic(err)
	}
	return clientIPResolver{trustProxy: trustProxy}
}

func (r clientIPResolver) FromRequest(req *http.Request) string {
	remoteIP := normalizeClientIPAddress(req.RemoteAddr)
	if !r.trustProxy.Enabled {
		return clientIPOrUnknown(remoteIP)
	}

	forwarded := forwardedForIPs(req.Header)
	if len(forwarded) == 0 {
		return clientIPOrUnknown(remoteIP)
	}

	if r.trustProxy.TrustAll {
		return clientIPOrUnknown(forwarded[0])
	}

	chain := append(forwarded, remoteIP)
	chain = compactClientIPChain(chain)
	if len(chain) == 0 {
		return "unknown"
	}
	clientIndex := len(chain) - 1 - r.trustProxy.Hops
	if clientIndex < 0 {
		clientIndex = 0
	}
	return chain[clientIndex]
}

func forwardedForIPs(header http.Header) []string {
	values := header.Values("X-Forwarded-For")
	ips := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if ip := normalizeClientIPAddress(part); ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func normalizeClientIPAddress(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}

	if host, port, err := net.SplitHostPort(text); err == nil {
		if !isNumericPort(port) {
			return ""
		}
		text = host
	} else if strings.HasPrefix(text, "[") {
		if end := strings.Index(text, "]"); end > 0 {
			text = text[1:end]
		}
	} else if strings.Count(text, ":") == 1 {
		if host, port, ok := strings.Cut(text, ":"); ok && isNumericPort(port) {
			if _, err := netip.ParseAddr(host); err == nil {
				text = host
			}
		}
	}

	addr, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil {
		return ""
	}
	if addr.Is4In6() {
		return netip.AddrFrom4(addr.As4()).String()
	}
	return addr.String()
}

func isNumericPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port >= 0 && port <= 65535
}

func compactClientIPChain(values []string) []string {
	clean := values[:0]
	for _, value := range values {
		if value != "" {
			clean = append(clean, value)
		}
	}
	return clean
}

func clientIPOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
