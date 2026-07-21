package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementaccountstatussnapshot"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestManagementAccountStatusSnapshotRejectsInvalidIDs(t *testing.T) {
	request := httptest.NewRequest("GET", "/status-snapshot?accountIds=", nil)
	request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "admin", Role: "admin"}))
	recorder := httptest.NewRecorder()
	NewManagementAccountStatusSnapshotHandler(managementaccountstatussnapshot.NewService(nil)).ServeHTTP(recorder, request)
	if recorder.Code != 400 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestManagementMyAccountStatusSnapshotRejectsTooManyIDs(t *testing.T) {
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = fmt.Sprintf("a%d%s", i, strings.Repeat("x", i%3))
	}
	request := httptest.NewRequest("GET", "/my-status-snapshot?accountIds="+strings.Join(ids, ","), nil)
	request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user", Role: "user"}))
	recorder := httptest.NewRecorder()
	NewManagementMyAccountStatusSnapshotHandler(managementaccountstatussnapshot.NewService(nil)).ServeHTTP(recorder, request)
	if recorder.Code != 400 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
