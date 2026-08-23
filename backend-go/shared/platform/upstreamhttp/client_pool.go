package upstreamhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ClientPool owns long-lived transports for direct upstream callers.  A
// caller borrows the returned client for requests and must close response
// bodies, but must not close idle connections after every request; doing so
// would defeat the transport's keep-alive and HTTP/2 connection pools.
type ClientPool struct {
	mu         sync.Mutex
	clients    map[string]clientPoolEntry
	maxEntries int
	clock      uint64
}

type clientPoolEntry struct {
	client *http.Client
	used   uint64
}

const defaultClientPoolEntries = 256

// NewClientPool creates an independently closable pool, primarily useful for
// a worker lifetime or tests that need explicit cleanup.
func NewClientPool() *ClientPool {
	return NewClientPoolWithLimit(defaultClientPoolEntries)
}

// NewClientPoolWithLimit creates a bounded pool. A non-positive limit is
// coerced to one so callers never accidentally create an unbounded cache.
func NewClientPoolWithLimit(limit int) *ClientPool {
	if limit < 1 {
		limit = 1
	}
	return &ClientPool{clients: make(map[string]clientPoolEntry), maxEntries: limit}
}

// Client returns a shared no-redirect client for the exact transport policy.
// Proxy credentials are represented only by a digest in the in-memory key;
// the raw URL is still passed to the transport constructor and never logged.
func (p *ClientPool) Client(rawProxyURL string, options TransportOptions) (*http.Client, error) {
	if p == nil {
		return nil, ErrClientPoolNil
	}
	key := transportKey(rawProxyURL, options)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = make(map[string]clientPoolEntry)
	}
	if p.maxEntries < 1 {
		p.maxEntries = defaultClientPoolEntries
	}
	p.clock++
	if entry, ok := p.clients[key]; ok {
		entry.used = p.clock
		p.clients[key] = entry
		return entry.client, nil
	}
	transport, err := NewTransport(rawProxyURL, options)
	if err != nil {
		return nil, err
	}
	client := NewClientWithTransport(transport)
	if len(p.clients) >= p.maxEntries {
		p.evictLeastRecentlyUsed()
	}
	p.clients[key] = clientPoolEntry{client: client, used: p.clock}
	return client, nil
}

func (p *ClientPool) evictLeastRecentlyUsed() {
	var oldestKey string
	var oldestUse uint64
	for key, entry := range p.clients {
		if oldestKey == "" || entry.used < oldestUse {
			oldestKey = key
			oldestUse = entry.used
		}
	}
	if oldestKey == "" {
		return
	}
	p.clients[oldestKey].client.CloseIdleConnections()
	delete(p.clients, oldestKey)
}

// CloseIdleConnections releases idle sockets while keeping the pool usable;
// a later Client call will reuse the same transport and establish connections
// again as needed.
func (p *ClientPool) CloseIdleConnections() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.clients {
		entry.client.CloseIdleConnections()
	}
}

var (
	ErrClientPoolNil  = &clientPoolError{"upstream client pool is nil"}
	defaultClientPool = NewClientPool()
)

type clientPoolError struct{ message string }

func (e *clientPoolError) Error() string { return e.message }

// SharedClient returns a process-wide client pool entry for jobs that run
// many short-lived account probes or balance queries.
func SharedClient(rawProxyURL string, options TransportOptions) (*http.Client, error) {
	return defaultClientPool.Client(rawProxyURL, options)
}

func transportKey(rawProxyURL string, options TransportOptions) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(rawProxyURL))
	builder.WriteByte(0)
	builder.WriteString(strconv.FormatInt(int64(options.ResponseHeaderTimeout), 10))
	builder.WriteByte(0)
	builder.WriteString(strconv.FormatInt(options.MaxResponseHeaderBytes, 10))
	builder.WriteByte(0)
	if options.DisableCompression {
		builder.WriteByte('1')
	} else {
		builder.WriteByte('0')
	}
	builder.WriteByte(0)
	if options.ForceRemoteSOCKS5 {
		builder.WriteByte('1')
	} else {
		builder.WriteByte('0')
	}
	for _, key := range sortedHeaderKeys(options.ProxyConnectHeader) {
		builder.WriteByte(0)
		builder.WriteString(key)
		for _, value := range options.ProxyConnectHeader.Values(key) {
			builder.WriteByte(0)
			builder.WriteString(value)
		}
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}

func sortedHeaderKeys(header http.Header) []string {
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
