package accounthealth

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This is deliberately opt-in: CI/unit runs must not open a business
// database. L3 supplies an isolated database plus one Node-created fixture.
func TestPostgresDirectInputReaderSmoke(t *testing.T) {
	postgresURL := os.Getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL")
	secret := os.Getenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET")
	wantAccountID := os.Getenv("JUHE_AI_J1_DIRECT_INPUT_SMOKE_ACCOUNT_ID")
	wantAuthorizedAccountID := os.Getenv("JUHE_AI_J1_DIRECT_INPUT_SMOKE_AUTHORIZED_ACCOUNT_ID")
	wantQuotaExceededAccountID := os.Getenv("JUHE_AI_J1_DIRECT_INPUT_SMOKE_QUOTA_EXCEEDED_ACCOUNT_ID")
	wantCooldownAccountID := os.Getenv("JUHE_AI_J1_DIRECT_INPUT_SMOKE_COOLDOWN_ACCOUNT_ID")
	if postgresURL == "" || secret == "" || wantAccountID == "" {
		t.Skip("requires isolated J1 PostgreSQL direct-input smoke environment")
	}
	database, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatalf("open direct input database: %v", err)
	}
	defer database.Close()
	context, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := database.PingContext(context); err != nil {
		t.Fatalf("ping direct input database: %v", err)
	}
	reader, err := NewPostgresDirectInputReader(database, secret, time.Hour, time.Now)
	if err != nil {
		t.Fatalf("new direct input reader: %v", err)
	}
	inputs, err := reader.LoadDue(context, 32)
	if err != nil {
		t.Fatalf("load direct input candidates: %v", err)
	}
	foundOwner := false
	for _, input := range inputs {
		if input.AccountID != wantAccountID {
			continue
		}
		if !inputEligible(input) || input.InputVersion < 1 || input.ConfigRevision < 1 || input.DispatchRevision < 1 {
			t.Fatalf("fixture input is not a valid J1 candidate: %#v", input)
		}
		if len(input.APIKeys) != 1 || input.Type != "api_key" || input.Provider != "openai" {
			t.Fatalf("fixture input was not decoded as the expected OpenAI API-key candidate")
		}
		if input.AccountID == wantAccountID {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Fatalf("isolated owner fixture account %q was absent from direct-input candidates", wantAccountID)
	}
	if wantAuthorizedAccountID == "" {
		return
	}
	authorizedInputs, err := reader.LoadAccount(context, wantAuthorizedAccountID)
	if err != nil {
		t.Fatalf("load authorized explicit input: %v", err)
	}
	if len(authorizedInputs) != 1 || authorizedInputs[0].AccountID != wantAuthorizedAccountID {
		t.Fatalf("isolated authorized fixture account %q was absent from explicit direct input", wantAuthorizedAccountID)
	}
	authorized := authorizedInputs[0]
	if !inputEligible(authorized) || authorized.Eligibility.SourceConfigRevision == nil {
		t.Fatalf("authorized explicit input is missing eligibility/source fence: %#v", authorized)
	}
	if authorized.Proxy == nil {
		t.Fatal("authorized explicit input is missing source proxy envelope")
	}
	if wantQuotaExceededAccountID == "" {
		return
	}
	quotaInputs, err := reader.LoadAccount(context, wantQuotaExceededAccountID)
	if err != nil {
		t.Fatalf("load quota-exceeded explicit input: %v", err)
	}
	if len(quotaInputs) != 0 {
		t.Fatalf("quota-exceeded fixture account %q must be omitted, got %#v", wantQuotaExceededAccountID, quotaInputs)
	}
	if wantCooldownAccountID == "" {
		return
	}
	cooldownInputs, err := reader.LoadAccount(context, wantCooldownAccountID)
	if err != nil {
		t.Fatalf("load cooldown explicit input: %v", err)
	}
	if len(cooldownInputs) != 1 || cooldownInputs[0].Cooldown == nil || cooldownInputs[0].Cooldown.SourceConfigRevision == nil {
		t.Fatalf("cooldown fixture account %q is missing five-field fence: %#v", wantCooldownAccountID, cooldownInputs)
	}
}
