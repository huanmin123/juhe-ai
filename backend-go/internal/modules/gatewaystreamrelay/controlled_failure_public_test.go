package gatewaystreamrelay_test

import (
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
)

func TestHandBuiltDispositionCannotAuthorizeControlledFailure(t *testing.T) {
	t.Parallel()
	disposition := gatewaystreamrelay.TerminalDisposition{
		TerminalKind:        gatewaystreamrelay.TerminalKindReadFailure,
		EmitControlledEvent: true,
	}
	got, err := gatewaystreamrelay.EncodeControlledFailureEvent(disposition, gatewaystreamrelay.ControlledFailureProtocolResponses)
	if len(got) != 0 || !errors.Is(err, gatewaystreamrelay.ErrControlledFailureEventNotPermitted) {
		t.Fatalf("EncodeControlledFailureEvent() = %q, %v; want no bytes and %v", got, err, gatewaystreamrelay.ErrControlledFailureEventNotPermitted)
	}
}
