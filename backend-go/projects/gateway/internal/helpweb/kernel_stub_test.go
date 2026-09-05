package helpweb

import "net/http"

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

func (k *kernelStub) RegisterFallback(handler http.Handler) {}

func (k *kernelStub) handlerFor(method, path string) http.Handler {
	if handler, ok := k.handlers[method+" "+path]; ok {
		return handler
	}
	return k.handlers[method+" "+path+"/"]
}

func newKernelStub() *kernelStub { return &kernelStub{} }
