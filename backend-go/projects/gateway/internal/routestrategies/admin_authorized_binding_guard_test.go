package routestrategies

// Recipient-consent boundary: a scoped administrator may manage the receiver's
// own groups but must not bind a group authorized to the receiver. Only the
// receiver's personal surface may create that binding.

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func TestAdminScopedCreateRejectsAuthorizedGroupBinding(t *testing.T) {
	env := newTestEnv(t)
	receiver, err := env.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		MustChangePassword: &mustChangeFalse,
		Username:           "receiver",
		DisplayName:        "receiver_name",
		Password:           "receiver-pass",
		Role:               "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	receiverID := receiver.ID
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	groupID := env.createGroup(t, ownerID, "admin-authz", true)

	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, effective_source_type, expires_at, created_at, updated_at)
		VALUES ('auth-admin-guard', 'group', ?, ?, 'active', 'manual', NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, groupID, receiverID)
	env.exec(t, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('auth-admin-guard-manual', 'auth-admin-guard', 'manual', 'active', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, ownerID)
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, snapshot_revision, created_at, updated_at)
		VALUES ('auth-admin-guard', ?, ?, 1, 'snap-admin-guard', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, receiverID, groupID)

	body := `{"name":"admin-proxy","groupBindings":[{"groupId":"` + groupID + `"}]}`
	code, payload := env.createStrategy(t, "/__aisys__/api/route-strategies?systemAccountId="+receiverID, body)
	if code != http.StatusBadRequest || payload["code"] != "authorization_binding_forbidden" {
		t.Fatalf("admin scoped authorized binding: %d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM route_strategies WHERE system_account_id = ? AND name = 'admin-proxy'`, receiverID); got != 0 {
		t.Fatalf("rejected admin binding must not create a strategy, got %d rows", got)
	}

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"receiver","password":"receiver-pass"}`)
	if code != http.StatusOK {
		t.Fatalf("receiver login: %d %v", code, payload)
	}
	if code, payload := env.createStrategy(t, "/__aisys__/api/my-route-strategies", body); code != http.StatusCreated {
		t.Fatalf("receiver self binding must stay usable: %d %v", code, payload)
	}
}
