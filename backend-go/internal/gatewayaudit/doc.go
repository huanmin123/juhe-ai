// Package gatewayaudit defines the HTTP-independent gateway audit capture contract.
//
// The Go contract intentionally differs from the Node capture context in three ways:
// terminal success is derived from the resolved outcome so contradictory pairs cannot
// escape, and a protocol-required streaming terminal event is required before a stream
// can be finalized as successful. It also does not copy Node's header and URL sanitizer
// drift: raw headers, query values, and bodies stay unchanged under the payload owner,
// while this package applies only explicit byte bounds and truncation facts. Generic
// parser-skipped passthrough streams may still finish on transport EOF. This package
// owns only bounded metadata DTOs. Raw payload, blob, queue, database, and HTTP
// integration remain separate migration owners.
package gatewayaudit
