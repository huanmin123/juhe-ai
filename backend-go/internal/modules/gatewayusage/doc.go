// Package gatewayusage defines the side-effect-free hand-off between gateway
// request/protocol adapters and future usage queue or storage adapters.
//
// The Go snapshot boundary intentionally improves on Node's approximate byte
// counter by enforcing the limit against the final encoded JSON. It preserves
// the current product contract for URLs, bodies, headers, and diagnostics: the
// captured values remain verbatim and this package only applies capacity and
// structural protection. This intentionally fixes the Node sanitizer drift
// from the authoritative usage snapshot policy instead of copying it into Go.
package gatewayusage
