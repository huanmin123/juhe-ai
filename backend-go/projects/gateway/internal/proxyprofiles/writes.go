package proxyprofiles

// Write half of the proxy family: create, management patch (updated_at CAS +
// connection-change test reset) and guarded delete (Node createProxyAsync /
// patchProxyForManagementAsync / deleteProxyForManagementAsync).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// proxyInput carries the parsed create/patch body (strict field set).
type proxyInput struct {
	Name              *string
	Description       *string // nil = absent; pointer-to-"" cleared to NULL upstream
	HasDescription    bool
	Type              *string
	Host              *string
	Port              *int
	Username          *string
	HasUsername       bool
	Password          *string
	HasPassword       bool
	Enabled           *bool
	ExpectedUpdatedAt string
}

// normalize validates the shared field normalizers (Node zod schema plus the
// repository normalizers). The route layer maps typed failures onto 400.
func (input *proxyInput) normalize() error {
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return ErrNameRequired
		}
		input.Name = &name
	}
	if input.HasDescription {
		if input.Description != nil {
			text := strings.TrimSpace(*input.Description)
			if len([]rune(text)) > 200 {
				// Node zod: max(200) -> schema 400 代理参数无效 (route renders
				// a generic message); surfaced here as a typed error.
				return ErrDescriptionInvalid
			}
			input.Description = &text
		}
	}
	if input.Type != nil {
		switch *input.Type {
		case "http", "https", "socks5", "socks5h":
		default:
			return ErrTypeInvalid
		}
	}
	if input.Host != nil {
		host := strings.TrimSpace(*input.Host)
		if host == "" {
			return ErrHostRequired
		}
		input.Host = &host
	}
	if input.Port != nil && (*input.Port < 1 || *input.Port > 65535) {
		return ErrPortInvalid
	}
	if input.HasUsername && input.Username != nil {
		text := strings.TrimSpace(*input.Username)
		input.Username = &text
	}
	if input.HasPassword {
		if input.Password == nil {
			return ErrPasswordString
		}
		if strings.TrimSpace(*input.Password) == "" {
			return ErrPasswordRequired
		}
	}
	return nil
}

// encryptPassword seals the password with the Node-compatible AES-GCM
// envelope (proxy.repository.ts encryptJson({password})).
func (s *Store) encryptPassword(password string) (string, error) {
	return apikeys.EncryptJSON(s.secret, map[string]string{"password": password})
}

// Create mirrors createProxyAsync: insert with unknown test status; duplicate
// names surface as 409.
func (s *Store) Create(ctx context.Context, input proxyInput, systemAccountID string) (ProfileSummary, error) {
	profile := ProfileSummary{
		ID:         s.newID("proxy"),
		Name:       derefOrEmpty(input.Name),
		Type:       derefOrEmpty(input.Type),
		Host:       derefOrEmpty(input.Host),
		Port:       derefInt(input.Port),
		Username:   input.Username,
		Enabled:    input.Enabled == nil || *input.Enabled,
		TestStatus: testStatusUnknown,
	}
	if input.HasDescription && input.Description != nil {
		description := *input.Description
		profile.Description = &description
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	profile.UpdatedAt = now
	var passwordEncrypted any
	if input.HasPassword && input.Password != nil {
		sealed, err := s.encryptPassword(*input.Password)
		if err != nil {
			return ProfileSummary{}, err
		}
		passwordEncrypted = sealed
	}
	query := `
		INSERT INTO ` + s.table("proxy_profiles") + ` (id, system_account_id, name, description, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	var enabledValue any = s.enabledDBValue(profile.Enabled)
	_, err := s.db.ExecContext(ctx, s.bind(query),
		profile.ID, systemAccountID, profile.Name, nullIfEmpty(profile.Description), profile.Type, profile.Host,
		profile.Port, nullIfEmpty(profile.Username), passwordEncrypted, enabledValue, profile.TestStatus, now, now)
	if err != nil {
		if isProxyNameDuplicate(err) {
			return ProfileSummary{}, &DuplicateNameError{Name: profile.Name}
		}
		return ProfileSummary{}, err
	}
	return profile, nil
}

// patchOutcome mirrors ProxyProfileManagementPatchOutcome.
type patchOutcome struct {
	Mutation        ProfileMutationResult
	Name            string
	Before          map[string]any
	After           map[string]any
	PasswordChanged bool
	// RuntimeChanged mirrors plan.runtimeChanged: any change that affects the
	// gateway runtime proxy cache (connection fields, password, enabled).
	RuntimeChanged bool
}

// ProfileMutationResult mirrors ProxyProfileMutationResult.
type ProfileMutationResult struct {
	ID        string         `json:"id"`
	UpdatedAt string         `json:"updatedAt"`
	Changed   bool           `json:"changed"`
	Values    map[string]any `json:"values"`
}

// Patch mirrors patchProxyForManagement: CAS on updated_at, empty patches are
// no-ops (changed:false), connection changes reset the test state.
func (s *Store) Patch(ctx context.Context, id string, input proxyInput) (*patchOutcome, error) {
	current, err := s.loadProfileRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	currentUpdatedAt := normalizeProxyRevision(current.updatedAt)
	if currentUpdatedAt != normalizeProxyRevision(input.ExpectedUpdatedAt) {
		return nil, ErrConflict
	}
	before := map[string]any{}
	after := map[string]any{}
	values := map[string]any{}
	assignments := []struct {
		column string
		value  any
	}{}
	connectionChanged := false
	passwordChanged := false
	name := current.name
	equals := func(currentValue, nextValue any) bool { return currentValue == nextValue }

	runtimeChanged := false
	add := func(key, column string, currentValue, nextValue any, affectsConnection bool) {
		before[key] = currentValue
		after[key] = nextValue
		if equals(currentValue, nextValue) {
			return
		}
		assignments = append(assignments, struct {
			column string
			value  any
		}{column, nextValue})
		values[key] = nextValue
		if affectsConnection {
			connectionChanged = true
			runtimeChanged = true
		}
	}
	if input.Name != nil {
		name = *input.Name
		add("name", "name", current.name, *input.Name, false)
	}
	if input.HasDescription {
		var currentDesc any
		if current.description != nil {
			currentDesc = *current.description
		}
		var nextDesc any
		if input.Description != nil {
			nextDesc = *input.Description
		}
		add("description", "description", currentDesc, nextDesc, false)
	}
	if input.Type != nil {
		add("type", "type", current.typeCode, *input.Type, true)
	}
	if input.Host != nil {
		add("host", "host", current.host, *input.Host, true)
	}
	if input.Port != nil {
		add("port", "port", current.port, *input.Port, true)
	}
	if input.HasUsername {
		var currentUsername any
		if current.username != nil {
			currentUsername = *current.username
		}
		var nextUsername any
		if input.Username != nil {
			nextUsername = *input.Username
		}
		add("username", "username", currentUsername, nextUsername, true)
	}
	if input.HasPassword {
		nextPassword := *input.Password
		currentPassword := ""
		if current.passwordEncrypted != "" {
			var decrypted struct {
				Password any `json:"password"`
			}
			if err := apikeys.DecryptJSON(s.secret, current.passwordEncrypted, &decrypted); err == nil {
				if text, ok := decrypted.Password.(string); ok {
					currentPassword = text
				}
			}
		}
		if currentPassword != "" {
			before["password"] = "[已设置]"
		} else {
			before["password"] = nil
		}
		after["password"] = "[已设置]"
		if currentPassword != nextPassword {
			sealed, err := s.encryptPassword(nextPassword)
			if err != nil {
				return nil, err
			}
			assignments = append(assignments, struct {
				column string
				value  any
			}{"password_encrypted", sealed})
			connectionChanged = true
			passwordChanged = true
		}
	}
	if input.Enabled != nil {
		add("enabled", "enabled", current.enabled, *input.Enabled, false)
		if current.enabled != *input.Enabled {
			// The assignment just appended carries the JSON value (bool); the
			// bound database value is dialect-shaped (Node
			// buildProxyManagementPatchPlan proxy.repository.ts:978).
			assignments[len(assignments)-1].value = s.enabledDBValue(*input.Enabled)
			// Node: enabled flips set runtimeChanged only (no test reset).
			runtimeChanged = true
		}
	}
	if len(assignments) == 0 {
		return &patchOutcome{
			Mutation: ProfileMutationResult{ID: id, UpdatedAt: currentUpdatedAt, Changed: false, Values: map[string]any{}},
			Name:     name,
			Before:   before,
			After:    after,
		}, nil
	}
	if connectionChanged {
		assignments = append(assignments,
			struct {
				column string
				value  any
			}{"test_status", testStatusUnknown},
			struct {
				column string
				value  any
			}{"latency_ms", nil},
			struct {
				column string
				value  any
			}{"outbound_ip", nil},
			struct {
				column string
				value  any
			}{"outbound_region", nil},
			struct {
				column string
				value  any
			}{"last_test_message", nil},
			struct {
				column string
				value  any
			}{"last_tested_at", nil},
		)
		values["testStatus"] = testStatusUnknown
		values["latencyMs"] = nil
		values["outboundIp"] = nil
		values["outboundRegion"] = nil
		values["lastTestMessage"] = nil
		values["lastTestedAt"] = nil
	}
	updatedAtCandidate := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	setClauses := make([]string, 0, len(assignments)+1)
	params := make([]any, 0, len(assignments)+3)
	for _, assignment := range assignments {
		setClauses = append(setClauses, assignment.column+" = ?")
		params = append(params, assignment.value)
	}
	// Node bumps the timestamp when the same-millisecond collision occurs;
	// the SQLite CASE expression mirrors that exactly.
	if s.pg {
		setClauses = append(setClauses, "updated_at = GREATEST(updated_at + INTERVAL '1 millisecond', CAST(? AS timestamptz))")
		params = append(params, updatedAtCandidate)
	} else {
		setClauses = append(setClauses, "updated_at = CASE WHEN updated_at >= ? THEN strftime('%Y-%m-%dT%H:%M:%fZ', updated_at, '+0.001 seconds') ELSE ? END")
		params = append(params, updatedAtCandidate, updatedAtCandidate)
	}
	params = append(params, id, currentUpdatedAt)
	// Node bumps the timestamp when the same-millisecond collision occurs;
	// the SQLite CASE expression mirrors that exactly. The PG CAS predicate
	// casts the revision text explicitly (patchProxyForManagementAsync
	// proxy.repository.ts:856).
	updateQuery := `
		UPDATE ` + s.table("proxy_profiles") + `
		SET ` + strings.Join(setClauses, ", ") + `
		WHERE id = ? AND updated_at = ` + s.revisionCastPlaceholder()
	result, err := s.db.ExecContext(ctx, s.bind(updateQuery), params...)
	if err != nil {
		if isProxyNameDuplicate(err) {
			return nil, &DuplicateNameError{Name: name}
		}
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrConflict
	}
	updated, err := s.loadProfileRow(ctx, id)
	if err != nil || updated == nil {
		return nil, err
	}
	return &patchOutcome{
		Mutation: ProfileMutationResult{
			ID:        id,
			UpdatedAt: normalizeProxyRevision(updated.updatedAt),
			Changed:   true,
			Values:    values,
		},
		Name:            name,
		Before:          before,
		After:           after,
		PasswordChanged: passwordChanged,
		RuntimeChanged:  runtimeChanged,
	}, nil
}

// Delete mirrors deleteProxyForManagementAsync: usage guard inside the same
// critical section, then the delete.
func (s *Store) Delete(ctx context.Context, id string) (string, error) {
	current, err := s.loadProfileRow(ctx, id)
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", nil
	}
	usageRows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT id, name
		FROM `+s.table("accounts")+`
		WHERE proxy_profile_id = ? AND deleted_at IS NULL
		ORDER BY id ASC
		LIMIT ?
	`), id, proxyUsageWindowLimit)
	if err != nil {
		return "", err
	}
	type usageRow struct {
		id   string
		name sql.NullString
	}
	usage := []usageRow{}
	for usageRows.Next() {
		var row usageRow
		if err := usageRows.Scan(&row.id, &row.name); err != nil {
			usageRows.Close()
			return "", err
		}
		usage = append(usage, row)
	}
	if err := usageRows.Err(); err != nil {
		usageRows.Close()
		return "", err
	}
	usageRows.Close()
	if len(usage) > 0 {
		names := []string{}
		for index, row := range usage {
			if index >= proxyUsagePreviewLimit {
				break
			}
			if row.name.Valid && row.name.String != "" {
				names = append(names, row.name.String)
			}
		}
		return "", &InUseError{
			AccountCount:             len(usage),
			AccountNames:             names,
			AccountCountIsLowerBound: len(usage) >= proxyUsageWindowLimit,
		}
	}
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("proxy_profiles")+` WHERE id = ?`), id)
	if err != nil {
		return "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if affected != 1 {
		return "", nil
	}
	return current.name, nil
}

// ---------------------------------------------------------------------------
// Profile row plumbing.
// ---------------------------------------------------------------------------

type profileRow struct {
	id                string
	name              string
	description       *string
	typeCode          string
	host              string
	port              int
	username          *string
	passwordEncrypted string
	enabled           bool
	testStatus        string
	updatedAt         string
}

func (s *Store) loadProfileRow(ctx context.Context, id string) (*profileRow, error) {
	var row profileRow
	var description, username sql.NullString
	var updatedAt any
	var passwordEncrypted sql.NullString
	var enabled any
	var port any
	query := `
		SELECT id, name, description, type, host, port, username, password_encrypted, enabled, test_status, ` +
		s.revisionSelectExpression() + `
		FROM ` + s.table("proxy_profiles") + `
		WHERE id = ?`
	err := s.db.QueryRowContext(ctx, s.bind(query), id).Scan(
		&row.id, &row.name, &description, &row.typeCode, &row.host, &port, &username,
		&passwordEncrypted, &enabled, &row.testStatus, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.description = nullStringPtr(description)
	row.username = nullStringPtr(username)
	row.passwordEncrypted = passwordEncrypted.String
	row.enabled = boolFromValue(enabled)
	row.port = int(fromAnyInt(port))
	row.updatedAt = toTextValue(updatedAt)
	return &row, nil
}

func (s *Store) scanProfile(rows rowScanner) (ProfileSummary, error) {
	var (
		profile                           ProfileSummary
		description, username, outboundIP sql.NullString
		outboundRegion, lastTestMessage   sql.NullString
		lastTestedAt                      sql.NullString
		latencyMs                         sql.NullInt64
		enabled                           any
	)
	err := rows.Scan(&profile.ID, &profile.Name, &description, &profile.Type, &profile.Host, &profile.Port,
		&username, &enabled, &profile.TestStatus, &latencyMs, &outboundIP, &outboundRegion,
		&lastTestMessage, &lastTestedAt, &profile.UpdatedAt)
	if err != nil {
		return ProfileSummary{}, err
	}
	profile.Description = nullStringPtr(description)
	profile.Username = nullStringPtr(username)
	profile.Enabled = boolFromValue(enabled)
	profile.TestStatus = normalizeTestStatus(profile.TestStatus)
	if latencyMs.Valid {
		value := latencyMs.Int64
		profile.LatencyMs = &value
	}
	profile.OutboundIp = nullStringPtr(outboundIP)
	profile.OutboundRegion = nullStringPtr(outboundRegion)
	profile.LastTestMessage = nullStringPtr(lastTestMessage)
	if lastTestedAt.Valid {
		value := lastTestedAt.String
		profile.LastTestedAt = &value
	}
	profile.UpdatedAt = normalizeProxyRevision(profile.UpdatedAt)
	return profile, nil
}

func normalizeProxyRevision(value string) string {
	return strings.TrimSpace(value)
}

func nullIfEmpty(value *string) any {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return nil
	}
	return *value
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func boolFromValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed == 1
	case float64:
		return typed != 0
	case []byte:
		return string(typed) == "1"
	default:
		return false
	}
}

func fromAnyInt(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	case []byte:
		var parsed int64
		_ = json.Unmarshal(typed, &parsed)
		return parsed
	default:
		return 0
	}
}

func toTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format("2006-01-02T15:04:05.000000Z")
	default:
		return ""
	}
}

// enabledDBValue shapes the bound boolean for the dialect: PG columns are
// `enabled boolean` and take the bool directly (Node createProxyAsync
// proxy.repository.ts:558 passes proxy.enabled; buildProxyManagementPatchPlan
// line 978 binds `next` on PG); SQLite keeps the 0/1 integer (line 517 and the
// toSqliteValues mapping in database-client.ts:377).
func (s *Store) enabledDBValue(enabled bool) any {
	if s.pg {
		return enabled
	}
	if enabled {
		return int64(1)
	}
	return int64(0)
}

// revisionSelectExpression textifies updated_at on PG so the revision scans
// back as text (Node proxyManagementPatchSelectColumns proxy.repository.ts:887-889
// and proxySummarySelectColumns line 450 use the same to_char US pattern); the
// SQLite column is already RFC3339 text.
func (s *Store) revisionSelectExpression() string {
	if s.pg {
		return `to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at`
	}
	return "updated_at"
}

// revisionCastPlaceholder wraps the CAS parameter on PG (Node
// patchProxyForManagementAsync proxy.repository.ts:856:
// `updated_at = CAST(? AS timestamptz)`).
func (s *Store) revisionCastPlaceholder() string {
	if s.pg {
		return "CAST(? AS timestamptz)"
	}
	return "?"
}
