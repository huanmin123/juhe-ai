package gatewayproto

import (
	"fmt"
	"strings"
)

// Registry mirrors the Node gateway protocol registry
// (protocols/registry.ts): an ordered driver list with profile- and
// request-based selection. Order matters — the first matching driver wins.
type Registry struct {
	drivers []ProtocolDriver
}

// NewRegistry builds a registry from the given drivers.
func NewRegistry(drivers ...ProtocolDriver) *Registry {
	return &Registry{drivers: append([]ProtocolDriver(nil), drivers...)}
}

// Drivers returns the registered drivers in selection order.
func (r *Registry) Drivers() []ProtocolDriver {
	return append([]ProtocolDriver(nil), r.drivers...)
}

// DriverForProfile mirrors gatewayProtocolDriverForProfile.
func (r *Registry) DriverForProfile(profile ProtocolProfile) (ProtocolDriver, bool) {
	for _, driver := range r.drivers {
		if driver.SupportsProfile(profile) {
			return driver, true
		}
	}
	return nil, false
}

// RequireDriverForProfile mirrors requireGatewayProtocolDriverForProfile.
func (r *Registry) RequireDriverForProfile(profile ProtocolProfile) (ProtocolDriver, error) {
	driver, ok := r.DriverForProfile(profile)
	if !ok {
		id := profile.ID
		if id == "" {
			id = "missing_profile"
		}
		return nil, fmt.Errorf("未配置网关协议驱动：%s", id)
	}
	return driver, nil
}

// DriverForRequest mirrors gatewayProtocolDriverForRequest: path-based
// matching first, driver order deciding ties.
func (r *Registry) DriverForRequest(shape RequestShape) (ProtocolDriver, bool) {
	for _, driver := range r.drivers {
		if driver.MatchPath(shape) {
			return driver, true
		}
	}
	return nil, false
}

// DriverForRequestOrProfile mirrors gatewayProtocolDriverForRequestOrProfile.
func (r *Registry) DriverForRequestOrProfile(shape RequestShape, profile ProtocolProfile) (ProtocolDriver, error) {
	if driver, ok := r.DriverForRequest(shape); ok {
		return driver, nil
	}
	return r.RequireDriverForProfile(profile)
}

// DriverForResponseProtocol mirrors requireGatewayProtocolDriverForResponseProtocol.
func (r *Registry) DriverForResponseProtocol(responseProtocol string) (ProtocolDriver, error) {
	for _, driver := range r.drivers {
		if driver.ResponseProtocol() == responseProtocol {
			return driver, nil
		}
	}
	if responseProtocol == "" {
		responseProtocol = "missing_response_protocol"
	}
	return nil, fmt.Errorf("未配置响应协议驱动：%s", responseProtocol)
}

// EndpointModeForRequest resolves the endpoint mode of a request shape via
// its driver (mirrors gatewayProtocolDriverForRequest + endpointModeForRequestShape).
func (r *Registry) EndpointModeForRequest(shape RequestShape) (EndpointMode, bool) {
	driver, ok := r.DriverForRequest(shape)
	if !ok {
		return "", false
	}
	return driver.EndpointModeForRequestShape(shape)
}

// IsProtocolRequestPath mirrors isGatewayProtocolRequest on the path level:
// any registered driver recognizes the path.
func (r *Registry) IsProtocolRequestPath(shape RequestShape) bool {
	_, ok := r.DriverForRequest(shape)
	return ok
}

// NormalizeProtocolToken mirrors normalizeProviderToken: trimmed lowercase
// non-empty token.
func NormalizeProtocolToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	return normalized
}
