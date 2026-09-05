package helpweb

import (
	"net/http"
	"strings"
)

type kernelStub struct {
	handlers map[string]http.Handler
	order    []string
}

func (k *kernelStub) Register(pattern string, handler http.Handler) {
	if k.handlers == nil {
		k.handlers = map[string]http.Handler{}
	}
	k.handlers[pattern] = handler
	k.order = append(k.order, pattern)
}

// handlerFor resolves the registered handler for a request path: the mount
// uses method-less patterns, so exact then longest-prefix subtree lookups.
func (k *kernelStub) handlerFor(_ string, path string) http.Handler {
	if handler, ok := k.handlers[path]; ok {
		return handler
	}
	best := ""
	for pattern, handler := range k.handlers {
		if !strings.HasSuffix(pattern, "/") {
			continue
		}
		if strings.HasPrefix(path, pattern) && len(pattern) > len(best) {
			best = pattern
			_ = handler
		}
	}
	if best != "" {
		return k.handlers[best]
	}
	return nil
}

func newKernelStub() *kernelStub { return &kernelStub{} }
