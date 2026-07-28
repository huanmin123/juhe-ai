package accountprobe

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"juhe-ai/backend-go/internal/platform/upstreamtransport"
)

type RevocationProtector interface {
	ProtectExternal(context.Context, func(context.Context) error, func(context.Context) error) error
}

// RevocationGuardTransport holds the PostgreSQL revocation gate across the
// exact final reload and the request-write boundary. The wrapped transport must
// not receive the reload fence again, otherwise the check would run outside the
// gate lifecycle.
type RevocationGuardTransport struct {
	Next  AttemptTransport
	Guard RevocationProtector
}

func (t RevocationGuardTransport) ExecuteWithFence(
	ctx context.Context,
	request *http.Request,
	fence func(context.Context) error,
) (upstreamtransport.Result, error) {
	if t.Next == nil || t.Guard == nil {
		return upstreamtransport.Result{}, fmt.Errorf("account probe revocation guard transport dependencies are required")
	}
	if fence == nil {
		return upstreamtransport.Result{}, fmt.Errorf("account probe revocation final reload is required")
	}

	var result upstreamtransport.Result
	var executeErr error
	guardErr := t.Guard.ProtectExternal(ctx, fence, func(sendCtx context.Context) error {
		result, executeErr = t.Next.ExecuteWithFence(sendCtx, request, nil)
		return executeErr
	})
	if guardErr != nil {
		return result, errors.Join(executeErr, guardErr)
	}
	return result, executeErr
}

var _ AttemptTransport = RevocationGuardTransport{}
