package gatewaydispatch

import (
	"context"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Shared shims and adapters for the dispatch engine.

// gatewayprotoLaneType is the lane type used by the engine signatures.
type gatewayprotoLaneType = gatewayproto.RequestLane

func gatewayhotqualityEffectiveImageLaneConcurrencyLimit(concurrencyLimit int, policy *gatewayruntimecache.GroupSchedulingPolicy) int {
	var dereferenced gatewayruntimecache.GroupSchedulingPolicy
	if policy != nil {
		dereferenced = *policy
	}
	return gatewayhotquality.EffectiveImageLaneConcurrencyLimit(concurrencyLimit, dereferenced)
}

// headerToStringMap mirrors headersToObject for the audit capture input.
func headerToStringMap(headers map[string][]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) > 0 {
			out[name] = strings.Join(values, ", ")
		}
	}
	return out
}

var _ = gatewayrouting.DefaultGatewayFinalResponseReserveMs
var _ = context.Background
var _ = errors.As
