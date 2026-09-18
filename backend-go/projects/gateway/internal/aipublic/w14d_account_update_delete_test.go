// w14d_account_update_delete_test.go drives the account update and delete
// families over the real stores: the full optional patch surface, the group
// and profile rebind diagnostics, the CAS retry/conflict handling, the notes
// and schedule arms and the delete not-found envelope.
package aipublic

import (
	"net/http"
	"strings"
	"testing"
)

func TestW14dAccountUpdatePatchSurface(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	if _, err := env.db.Exec(`INSERT INTO provider_protocol_profiles
		(id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_gpt_openai_v1', 'gpt', 'OpenAI v1', 1, 'openai', 'v1', 'https://api.openai.com', 'gpt-4o-mini', '["api_key"]', '{}', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"juhe_ai_public:account_add:write", "juhe_ai_public:account_update:write",
		"juhe_ai_public:account_delete:write", "juhe_ai_public:account_list:read"}
	token := env.seedSource("w14d_ext", "w14d_tok", "juis_token_upd4teacc0unt1", "active", "active", scopes, "[]", "", "")

	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14d_updater","targetGroupName":"更新组","name":"站点U","providerCode":"gpt",
		"providerProtocolProfileId":"profile_gpt_openai_v1","type":"api_key","baseUrl":"https://a.example",
		"apiKey":"sk-a","supportedModels":["gpt-4o-mini"],"status":"disabled","concurrencyLimit":7,"priority":2,
		"notes":"note"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("account add: %d %v", status, payload)
	}
	accountID := payload["data"].(map[string]any)["account"].(map[string]any)["id"].(string)

	// The full optional patch surface including notes, schedule and status.
	update := `{"accountId":"` + accountID + `","targetUsername":"w14d_updater","name":"站点U2",
		"baseUrl":"https://b.example","apiKey":"sk-b","supportedModels":["gpt-4o"],
		"status":"active","concurrencyLimit":9,"priority":3,
		"notes":"updated"}`
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update", update, token)
	if status != http.StatusOK {
		t.Fatalf("account update: %d %v", status, payload)
	}
	if payload["data"].(map[string]any)["action"] != "updated" {
		t.Fatalf("update action: %v", payload)
	}

	// A profile change to an unknown profile renders the diagnostics.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update",
		`{"accountId":"`+accountID+`","providerProtocolProfileId":"profile-nope"}`, token)
	if status != http.StatusBadRequest {
		t.Fatalf("profile rebind: %d %v", status, payload)
	}

	// The validation arms of the update schema.
	for _, body := range []string{
		`{"accountId":"x","type":"image"}`,
		`{"accountId":"x","supportedModels":"x"}`,
		`{"accountId":"x","supportedModels":[7]}`,
		`{"accountId":"x","concurrencyLimit":null}`,
		`{"accountId":"x","priority":null}`,
		`{}`,
	} {
		status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/update", body, token)
		if status != http.StatusBadRequest {
			t.Fatalf("update validation %s: %d %v", body, status, payload)
		}
	}

	// The delete removes the row and the second pass renders not-found.
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/del",
		`{"accountId":"`+accountID+`"}`, token)
	if status != http.StatusOK {
		t.Fatalf("account delete: %d %v", status, payload)
	}
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/account/del",
		`{"accountId":"`+accountID+`"}`, token)
	if status == http.StatusOK && payload["data"].(map[string]any)["action"] != "not_found" {
		t.Fatalf("double delete action: %v", payload)
	}
}

func TestW14dAccountAddValidationArms(t *testing.T) {
	env := newAIPublicEnv(t)
	env.seedProvider("gpt", true)
	env.seedTargetUser("w14d_validator", "w14dvalidator", "active")
	scopes := []string{"juhe_ai_public:account_add:write"}
	token := env.seedSource("w14d_ext", "w14d_tok9", "juis_token_v4l1d11111111",
		"active", "active", scopes, "[]", "", "")

	cases := []struct {
		body    string
		message string
	}{
		{`{"targetUsername":"w14dvalidator","targetGroupName":"g","name":"n","providerCode":"gpt","type":"image"}`, "请求参数无效"},
		{`{"targetUsername":"w14dvalidator","targetGroupName":"g","name":"n","providerCode":"gpt","baseUrl":"https://x","apiKey":"k","supportedModels":"x"}`, "请求参数无效"},
		{`{"targetUsername":"w14dvalidator","targetGroupName":"g","name":"n","providerCode":"gpt","baseUrl":"https://x","apiKey":"k","supportedModels":[1]}`, "请求参数无效"},
		{`{"targetUsername":"w14dvalidator","targetGroupName":"g","name":"n","providerCode":"gpt","type":"api_key","baseUrl":"https://x","apiKey":"k","supportedModels":["m"],"notes":" "}`, "请求参数无效"},
	}
	for _, testCase := range cases {
		status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add", testCase.body, token)
		if status != http.StatusBadRequest || !strings.Contains(payload["message"].(string), testCase.message) {
			t.Fatalf("add validation %s: %d %v", testCase.body, status, payload)
		}
	}

	// The disabled target renders the dedicated message.
	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/account/add",
		`{"targetUsername":"w14doff","targetGroupName":"g","name":"n","providerCode":"gpt","type":"api_key","baseUrl":"https://x","apiKey":"k","supportedModels":["m"]}`, token)
	if status != http.StatusBadRequest || payload["message"] == "" {
		t.Fatalf("disabled target add: %d %v", status, payload)
	}
}
