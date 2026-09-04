// Package gatewaypreauth is the Go port of the gateway request pre-auth and
// preflight orchestration slice (work package G05):
//
//   - backend/src/modules/gateway/request/pre-auth.ts,
//   - backend/src/modules/gateway/request/preflight.ts (core orchestration),
//   - backend/src/modules/gateway/request/authorization-preflight.ts,
//   - backend/src/modules/gateway/request/metadata.ts,
//   - backend/src/modules/gateway/request/error-response.ts,
//   - backend/src/modules/gateway/request/local-request-errors.ts,
//   - backend/src/modules/gateway/request/validation-error.ts,
//   - plus the small request/-folder helpers the orchestration calls inline
//     (image-permission.ts, models-response-protocol.ts) and the inline pure
//     helpers of those files.
//
// The orchestration calls routing / quota / runtime-cache / body via the
// existing Go packages (gatewayrouting, gatewayquota, gatewayruntimecache,
// gatewaybody) and the protocol packages (gatewayopenai, gatewayanthropic,
// gatewaygemini). Collaborators owned by later slices (client-ip guard
// runtime G13, session identity/affinity G14, dispatch pipeline G15, response
// senders G16, usage/audit G17, client profiles + codex bridge G18) are
// declared as port interfaces in ports.go so the orchestration order, error
// copy and status codes are frozen now and mock-testable.
//
// Every gateway-facing Chinese error message is byte-identical with the Node
// source; HTTP status codes and the rejection matrix mirror Node field by
// field.
package gatewaypreauth
