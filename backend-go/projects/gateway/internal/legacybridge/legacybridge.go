// Package legacybridge proxies requests that belong to slices not yet
// migrated from the Node process to the Go gateway. Each flipped slice
// removes its proxy rule (registered routes always win over the bridge);
// at P8 the bridge is deleted entirely. It exists ONLY during the migration
// window so the Go gateway can be the single HTTP entry while Node still
// serves unmigrated families.
package legacybridge

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Bridge forwards unmatched prefixes to the Node origin.
type Bridge struct {
	target        *url.URL
	proxy         *httputil.ReverseProxy
	prefixes      []string
	flushInterval time.Duration
}

// New builds a bridge to the Node origin (e.g. http://127.0.0.1:3000).
func New(target string) (*Bridge, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	return &Bridge{
		target: u,
		proxy: &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(u)
				pr.Out.Host = u.Host
				// Keep the original client IP chain intact for Node's
				// trust-proxy resolution; strip hop-by-hop noise.
				pr.Out.Header.Set("X-Juhe-Ai-Served-By", "legacy-bridge")
			},
			FlushInterval: 1 * time.Millisecond, // SSE-friendly immediate flush
		},
	}, nil
}

// RegisterPrefix marks a prefix as still Node-owned. Matched requests are
// proxied; flipped prefixes must be removed from this list (RemovePrefix).
func (b *Bridge) RegisterPrefix(prefix string) {
	b.prefixes = append(b.prefixes, prefix)
}

// RemovePrefix drops a prefix from the bridge (slice flip). Returns whether
// a rule was removed.
func (b *Bridge) RemovePrefix(prefix string) bool {
	for i, p := range b.prefixes {
		if p == prefix {
			b.prefixes = append(b.prefixes[:i], b.prefixes[i+1:]...)
			return true
		}
	}
	return false
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, p := range b.prefixes {
		if strings.HasPrefix(r.URL.Path, p) {
			b.proxy.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
}
