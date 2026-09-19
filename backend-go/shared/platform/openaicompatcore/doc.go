// Package openaicompatcore holds the neutral vocabulary shared by the
// openaicompat domain packages (bridge protocol conversion and storage REST
// endpoints): shared error types, runtime Config, the GatewayScope resolver
// vocabulary, endpoint-family constants and the pure JS-compatibility
// parsing / time helpers. It mirrors the neutral core identified by
// docs/refactors/重构-0008-剩余god包拆分判定与设计.md (阶段 A-1).
//
// The package must stay a pure standard-library leaf: it must not import any
// sibling or business package (asserted via go list -deps).
package openaicompatcore
