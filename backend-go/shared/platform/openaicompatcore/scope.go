package openaicompatcore

import "net/http"

// GatewayScope mirrors the apiKey subset of the Node gateway runtime that the
// five modules consume: scope binding for every store query.
type GatewayScope struct {
	SystemAccountID string
	APIKeyID        string
}

// ScopeResolver supplies the gateway runtime scope for a request (the Go
// equivalent of req.gatewayRuntime set by preResolveGatewayRuntime). Returning
// nil mirrors a missing/invalid runtime and renders the 401 contract.
type ScopeResolver func(*http.Request) *GatewayScope
