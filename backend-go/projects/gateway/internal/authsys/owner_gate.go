package authsys

import (
	"context"
	"errors"
	"fmt"
)

// OwnerGate is the external handoff evidence for the authsys system-account
// writes, isomorphic to internal/business/system_accounts OwnerGate and the
// businessauth gate: a partial handoff never permits a mutation even when the
// database and all relations are reachable (BUG-0169.3).
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

// Ready reports whether every handoff precondition is proven.
func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// ErrOwnerGate is returned by every AccountStore operation while the owner
// gate is not satisfied. It is a sentinel so callers can branch on it.
var ErrOwnerGate = errors.New("authsys system-account owner handoff gate is not satisfied")

// errContract mirrors the business system_accounts contract failure shape.
var errContract = errors.New("authsys system-account contract is not satisfied")

// checkContract verifies that the relations and columns this store writes
// already exist. It only reads pre-existing relations (LIMIT 0); runtime DDL
// is forbidden and a missing relation/column is a deployment contract failure.
func (s *AccountStore) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	relations := map[string]string{
		"system_accounts":       `id,username,display_name,description,role,status,password_hash,must_change_password,image_generation_enabled,ai_account_limit,request_limits_json,last_login_at,created_at,updated_at`,
		"system_sessions":       `id,system_account_id,token_hash,expires_at,created_at,last_seen_at`,
		"groups":                `id,system_account_id,name,provider_code,description,enabled,is_default,group_type,created_at,updated_at`,
		"route_strategies":      `id,system_account_id,name,description,mode,status,is_default,created_at,updated_at`,
		"route_strategy_groups": `id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at,updated_at`,
		"api_keys":              `id,system_account_id,route_strategy_id,name,description,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at`,
	}
	for name, columns := range relations {
		query := "SELECT " + columns + " FROM " + s.table(name) + " LIMIT 0"
		if _, err := s.db.ExecContext(ctx, s.bind(query)); err != nil {
			return fmt.Errorf("%w: verify %s: %v", errContract, name, err)
		}
	}
	return nil
}

// requireOwner is the fail-closed guard every AccountStore operation passes
// through: without the proven handoff gate the store never touches the
// business database, so a Go entry started while the Node writer is still
// live cannot corrupt the single-owner contract.
func (s *AccountStore) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}
