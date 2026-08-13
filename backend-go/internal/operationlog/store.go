package operationlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

var ErrOwnerLeaseLost = errors.New("F4 operation-log owner lease lost")

const (
	maxListWindowRows = 1001
	maxPageSize       = 50
)

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}
type Store interface {
	EnsureSchema(context.Context) error
	AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error)
	RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error)
	ReleaseOwnerLease(context.Context, OwnerLease) error
	Persist(context.Context, OwnerLease, Input) (bool, error)
	List(context.Context, ListOptions) (ListResult, error)
	Detail(context.Context, string, string) (DetailSupplement, bool, error)
	CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error)
	RetentionDays(context.Context, int) (int, error)
	Close() error
}
type sqlStore struct {
	db          *sql.DB
	businessDB  *sql.DB
	mode        Mode
	writeMu     sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
}

func OpenStore(cfg Config) (Store, error) {
	if cfg.Mode == ModeSQLite {
		if err := ensureDistinctSQLitePaths(cfg.DatabasePath, append([]string{cfg.BusinessSettingsPath}, cfg.SQLiteIsolationPaths...)...); err != nil {
			return nil, err
		}
		dsn, err := sqliteDSN(cfg.DatabasePath)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := configureSQLite(db); err != nil {
			_ = db.Close()
			return nil, err
		}
		businessDB, err := openSQLiteReadOnly(cfg.BusinessSettingsPath)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return &sqlStore{db: db, businessDB: businessDB, mode: cfg.Mode}, nil
	}
	db, err := sql.Open("pgx", cfg.PostgresURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return &sqlStore{db: db, mode: cfg.Mode}, nil
}

func ensureDistinctSQLitePaths(operationPath string, otherPaths ...string) error {
	operationAbs, err := filepath.Abs(operationPath)
	if err != nil {
		return err
	}
	canonical := func(path string) string {
		abs, _ := filepath.Abs(path)
		if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(abs)
	}
	operationCanonical := canonical(operationAbs)
	operationInfo, operationStatErr := os.Stat(operationAbs)
	for _, otherPath := range otherPaths {
		if strings.TrimSpace(otherPath) == "" {
			continue
		}
		otherAbs, absErr := filepath.Abs(otherPath)
		if absErr != nil {
			return absErr
		}
		if operationCanonical == canonical(otherAbs) {
			return fmt.Errorf("F4 operation log SQLite database must be physically distinct from %s", otherPath)
		}
		if operationStatErr == nil {
			if otherInfo, statErr := os.Stat(otherAbs); statErr == nil && os.SameFile(operationInfo, otherInfo) {
				return fmt.Errorf("F4 operation log SQLite database must not share an inode with %s", otherPath)
			}
		}
	}
	return nil
}
func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: "_pragma=busy_timeout(5000)"}).String(), nil
}
func configureSQLite(db *sql.DB) error {
	ctx := context.Background()
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var timeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		return fmt.Errorf("F4 SQLite busy timeout invalid: %d: %w", timeout, err)
	}
	return nil
}
func (s *sqlStore) Close() error {
	if s.businessDB != nil {
		_ = s.businessDB.Close()
	}
	return s.db.Close()
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("read F4 business settings SQLite: %w", err)
	}
	filePath := filepath.ToSlash(abs)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	dsn := (&url.URL{Scheme: "file", Path: filePath, RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *sqlStore) RetentionDays(ctx context.Context, fallback int) (int, error) {
	var value string
	var err error
	if s.mode == ModeSQLite {
		err = s.businessDB.QueryRowContext(ctx, "SELECT value_json FROM system_settings WHERE system_account_id=? AND key=?", "sys_admin", "operationLogRetentionDays").Scan(&value)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT value_json FROM juhe_business.system_settings WHERE system_account_id=$1 AND key=$2", "sys_admin", "operationLogRetentionDays").Scan(&value)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read operationLogRetentionDays: %w", err)
	}
	var number any
	if err := json.Unmarshal([]byte(value), &number); err != nil {
		return 0, fmt.Errorf("operationLogRetentionDays is not JSON: %w", err)
	}
	parsed, ok := number.(float64)
	if !ok || parsed != float64(int(parsed)) || parsed < 1 || parsed > 3650 {
		return 0, fmt.Errorf("operationLogRetentionDays must be integer 1..3650")
	}
	return int(parsed), nil
}
func (s *sqlStore) table(name string) string {
	if s.mode == ModePostgres {
		return "juhe_dataset." + name
	}
	return name
}
func (s *sqlStore) bind(q string) string {
	if s.mode != ModePostgres {
		return q
	}
	for n := 1; strings.Contains(q, "?"); n++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", n), 1)
	}
	return q
}
func (s *sqlStore) EnsureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if _, err := s.db.ExecContext(ctx, sqliteSchema); err != nil {
			return fmt.Errorf("initialize F4 sqlite schema: %w", err)
		}
	} else {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(763847296)"); err != nil {
			return err
		}
		for _, q := range strings.Split(postgresSchema, ";") {
			if q = strings.TrimSpace(q); q != "" {
				if _, err = tx.ExecContext(ctx, q); err != nil {
					return fmt.Errorf("initialize F4 postgres schema: %w", err)
				}
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	s.schemaReady = true
	return nil
}
func (s *sqlStore) AcquireOwnerLease(ctx context.Context, owner string, d time.Duration) (OwnerLease, bool, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	if s.mode == ModePostgres {
		q := `INSERT INTO juhe_dataset.operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at) VALUES ('f4-operation-log-persistence',?,1,clock_timestamp()+(? * INTERVAL '1 millisecond'),clock_timestamp()) ON CONFLICT(lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_dataset.operation_log_owner_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=clock_timestamp() WHERE juhe_dataset.operation_log_owner_leases.lease_until<=clock_timestamp() RETURNING fence_token`
		var token int64
		err := s.db.QueryRowContext(ctx, s.bind(q), owner, d.Milliseconds()).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerLease{}, false, nil
		}
		return OwnerLease{owner, token}, err == nil, err
	}
	now := time.Now().UTC()
	q := `INSERT INTO operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at) VALUES ('f4-operation-log-persistence',?,1,?,?) ON CONFLICT(lease_key) DO UPDATE SET owner_id=excluded.owner_id,fence_token=operation_log_owner_leases.fence_token+1,lease_until=excluded.lease_until,updated_at=excluded.updated_at WHERE operation_log_owner_leases.lease_until<=? RETURNING fence_token`
	var token int64
	err := s.db.QueryRowContext(ctx, q, owner, now.Add(d).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	return OwnerLease{owner, token}, err == nil, err
}
func (s *sqlStore) RenewOwnerLease(ctx context.Context, l OwnerLease, d time.Duration) (bool, error) {
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	q := `UPDATE ` + s.table("operation_log_owner_leases") + ` SET lease_until=?,updated_at=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>?`
	args := []any{time.Now().UTC().Add(d).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), l.OwnerID, l.FenceToken, time.Now().UTC().Format(time.RFC3339Nano)}
	if s.mode == ModePostgres {
		q = `UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()+(? * INTERVAL '1 millisecond'),updated_at=clock_timestamp() WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>clock_timestamp()`
		args = []any{d.Milliseconds(), l.OwnerID, l.FenceToken}
	}
	r, err := s.db.ExecContext(ctx, s.bind(q), args...)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s *sqlStore) ReleaseOwnerLease(ctx context.Context, l OwnerLease) error {
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	q := `UPDATE ` + s.table("operation_log_owner_leases") + ` SET lease_until=?,updated_at=? WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=?`
	args := []any{time.Unix(0, 0).UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), l.OwnerID, l.FenceToken}
	if s.mode == ModePostgres {
		q = `UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=to_timestamp(0),updated_at=clock_timestamp() WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=?`
		args = []any{l.OwnerID, l.FenceToken}
	}
	r, err := s.db.ExecContext(ctx, s.bind(q), args...)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}
func (s *sqlStore) verifyLease(ctx context.Context, tx *sql.Tx, l OwnerLease) error {
	q := `SELECT 1 FROM ` + s.table("operation_log_owner_leases") + ` WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>?`
	args := []any{l.OwnerID, l.FenceToken, time.Now().UTC().Format(time.RFC3339Nano)}
	if s.mode == ModePostgres {
		q = `SELECT 1 FROM juhe_dataset.operation_log_owner_leases WHERE lease_key='f4-operation-log-persistence' AND owner_id=? AND fence_token=? AND lease_until>clock_timestamp() FOR UPDATE`
		args = []any{l.OwnerID, l.FenceToken}
	}
	var one int
	if err := tx.QueryRowContext(ctx, s.bind(q), args...).Scan(&one); err != nil {
		return ErrOwnerLeaseLost
	}
	return nil
}
func (s *sqlStore) Persist(ctx context.Context, l OwnerLease, input Input) (bool, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return false, err
	}
	if err = s.EnsureSchema(ctx); err != nil {
		return false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return false, err
	}
	changes, _ := json.Marshal(input.Changes)
	q := `INSERT INTO ` + s.table("operation_logs") + ` (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`
	if s.mode == ModePostgres {
		q = `INSERT INTO juhe_dataset.operation_logs (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`
	}
	r, err := tx.ExecContext(ctx, s.bind(q), input.ID, nilIf(input.TraceID), input.ActorSystemAccountID, nilIf(input.ActorUsername), nilIf(input.ActorDisplayName), input.ActorRole, nilIf(input.OperationScopeSystemAccountID), input.Mode, input.Module, input.Action, input.OperationKey, input.ResourceType, nilIf(input.ResourceID), nilIf(input.ResourceName), input.Summary, input.DetailLevel, input.VisibilityScope, string(changes), string(input.Metadata), nilIf(input.Method), nilIf(input.Path), input.StatusCode, nilIf(input.ClientIP), nilIf(input.UserAgent), input.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return true, tx.Commit()
	}
	for i, t := range input.Targets {
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("operation_log_targets")+` (id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at) VALUES (?,?,?,?,?,?,?,?)`), fmt.Sprintf("optgt_%s_%d", input.ID, i), input.ID, t.TargetType, nilIf(t.TargetID), nilIf(t.TargetName), nilIf(t.TargetOwnerSystemAccountID), t.Relation, input.CreatedAt)
		if err != nil {
			return false, err
		}
	}
	for _, v := range input.Viewers {
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("operation_log_viewers")+` (operation_log_id,system_account_id,visibility_reason,detail_level,created_at) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`), input.ID, v.SystemAccountID, v.VisibilityReason, v.DetailLevel, input.CreatedAt)
		if err != nil {
			return false, err
		}
	}
	for _, term := range searchTerms(input.Summary) {
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("operation_log_summary_search_terms")+` (operation_log_id,term,created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`), input.ID, term, input.CreatedAt)
		if err != nil {
			return false, err
		}
	}
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return false, err
	}
	return false, tx.Commit()
}
func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func searchTerms(value string) []string {
	value = normalizeSearchText(value)
	if value == "" {
		return nil
	}
	compact := strings.ReplaceAll(value, " ", "")
	parts := strings.Fields(value)
	set := map[string]bool{}
	add := func(term string) {
		if length := len([]rune(term)); length >= 1 && length <= 128 {
			set[term] = true
		}
	}
	add(value)
	add(compact)
	for _, part := range parts {
		add(part)
	}
	for _, candidate := range append([]string{value, compact}, parts...) {
		chars := []rune(candidate)
		if len(chars) > 256 {
			chars = chars[:256]
		}
		for length := 1; length <= 128 && length <= len(chars); length++ {
			for start := 0; start+length <= len(chars) && len(set) < 1500; start++ {
				add(string(chars[start : start+length]))
			}
			if len(set) >= 1500 {
				break
			}
		}
		if len(set) >= 1500 {
			break
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func normalizeSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
	var b strings.Builder
	needsSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			needsSpace = false
		} else if b.Len() > 0 && !needsSpace {
			b.WriteByte(' ')
			needsSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *sqlStore) List(ctx context.Context, options ListOptions) (ListResult, error) {
	page := options.Page
	if page < 1 {
		page = 1
	}
	size := options.PageSize
	if size < 1 {
		size = 20
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	maxPage := max(1, (maxListWindowRows-1)/size)
	if page > maxPage {
		page = maxPage
	}
	where := []string{}
	args := []any{}
	add := func(column, value string) {
		value = strings.TrimSpace(value)
		if value != "" && value != "all" {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	add("ol.module", options.Module)
	add("ol.action", options.Action)
	add("ol.resource_type", options.ResourceType)
	add("ol.resource_id", options.ResourceID)
	add("ol.actor_system_account_id", options.ActorSystemAccountID)
	add("ol.operation_scope_system_account_id", options.OperationScopeSystemAccountID)
	if traceID := strings.TrimSpace(options.TraceID); traceID != "" {
		traceColumn := "ol.trace_id"
		if s.mode == ModePostgres {
			traceColumn += ` COLLATE "C"`
		}
		where = append(where, traceColumn+">=? AND "+traceColumn+"<?")
		args = append(args, traceID, textPrefixUpperBound(traceID))
	}
	if options.StartAt != "" {
		where = append(where, "ol.created_at>=?")
		args = append(args, options.StartAt)
	}
	if options.EndAt != "" {
		where = append(where, "ol.created_at<=?")
		args = append(args, options.EndAt)
	}
	if options.AffectedSystemAccountID != "" {
		where = append(where, "(ol.visibility_scope='all_users' OR EXISTS (SELECT 1 FROM "+s.table("operation_log_viewers")+" av WHERE av.operation_log_id=ol.id AND av.system_account_id=?))")
		args = append(args, options.AffectedSystemAccountID)
	}
	if options.SummaryKeyword != "" {
		term := normalizeSearchText(options.SummaryKeyword)
		if len([]rune(term)) > 128 {
			term = strings.ReplaceAll(term, " ", "")
		}
		if len([]rune(term)) <= 128 && term != "" {
			where = append(where, "EXISTS (SELECT 1 FROM "+s.table("operation_log_summary_search_terms")+" st WHERE st.operation_log_id=ol.id AND st.term=?)")
			args = append(args, term)
		} else {
			where = append(where, "1=0")
		}
	}
	queryItems := func(from string, queryWhere []string, queryArgs []any, limit, offset int) ([]ListItem, error) {
		queryClause := ""
		if len(queryWhere) > 0 {
			queryClause = " WHERE " + strings.Join(queryWhere, " AND ")
		}
		q := `SELECT ol.id,COALESCE(ol.trace_id,''),ol.actor_system_account_id,COALESCE(ol.actor_display_name,''),COALESCE(ol.operation_scope_system_account_id,''),ol.module,ol.action,ol.summary,ol.created_at FROM ` + from + queryClause + ` ORDER BY ol.created_at DESC,ol.id DESC LIMIT ? OFFSET ?`
		boundArgs := append(append([]any{}, queryArgs...), limit, offset)
		rows, err := s.db.QueryContext(ctx, s.bind(q), boundArgs...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []ListItem{}
		for rows.Next() {
			var item ListItem
			if scanErr := rows.Scan(&item.ID, &item.TraceID, &item.ActorSystemAccountID, &item.ActorDisplayName, &item.OperationScopeSystemAccountID, &item.Module, &item.Action, &item.Summary, &item.CreatedAt); scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return nil, rowsErr
		}
		return items, nil
	}
	start := (page - 1) * size
	items := []ListItem{}
	var err error
	if options.ViewerID == "" {
		items, err = queryItems(s.table("operation_logs")+" ol", where, args, size+1, start)
		if err != nil {
			return ListResult{}, err
		}
	} else {
		// Keep personal history index-driven: targeted viewer rows and all-user summaries
		// are independent bounded streams, merged only after both SQL reads complete.
		baseWhere := append([]string{}, where...)
		baseArgs := append([]any{}, args...)
		targetedWhere := append(baseWhere, "ol.visibility_scope='targeted'", "visible.system_account_id=?", "NOT EXISTS (SELECT 1 FROM "+s.table("operation_log_viewers")+" previous WHERE previous.operation_log_id=visible.operation_log_id AND previous.system_account_id=visible.system_account_id AND (previous.visibility_reason < visible.visibility_reason OR (previous.visibility_reason=visible.visibility_reason AND previous.detail_level < visible.detail_level)))")
		targetedArgs := append(baseArgs, options.ViewerID)
		allUsersWhere := append(baseWhere, "ol.visibility_scope='all_users'")
		targetedFrom := s.table("operation_log_viewers") + " visible JOIN " + s.table("operation_logs") + " ol ON ol.id=visible.operation_log_id"
		targeted, err := queryItems(targetedFrom, targetedWhere, targetedArgs, maxListWindowRows, 0)
		if err != nil {
			return ListResult{}, err
		}
		allUsers, err := queryItems(s.table("operation_logs")+" ol", allUsersWhere, baseArgs, maxListWindowRows, 0)
		if err != nil {
			return ListResult{}, err
		}
		items = append(targeted, allUsers...)
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt == items[j].CreatedAt {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt > items[j].CreatedAt
		})
		if len(items) > start+size+1 {
			items = items[:start+size+1]
		}
		if start >= len(items) {
			items = []ListItem{}
		} else {
			items = items[start:]
		}
	}
	names, err := s.accountNames(ctx, listAccountIDs(items))
	if err != nil {
		return ListResult{}, err
	}
	for index := range items {
		items[index].ActorSystemAccountName = names[items[index].ActorSystemAccountID]
		items[index].OperationScopeSystemAccountName = names[items[index].OperationScopeSystemAccountID]
	}
	more := len(items) > size
	if more {
		items = items[:size]
	}
	total := (page-1)*size + len(items)
	if more {
		total++
	}
	return ListResult{Items: items, Total: total, HasMore: more, Page: page, PageSize: size}, nil
}

func textPrefixUpperBound(value string) string {
	bytes := []byte(value)
	for index := len(bytes) - 1; index >= 0; index-- {
		if bytes[index] < 0xff {
			return string(append(bytes[:index], bytes[index]+1))
		}
	}
	return value + "\x00"
}

func (s *sqlStore) Detail(ctx context.Context, id, viewerID string) (DetailSupplement, bool, error) {
	where := "ol.id=?"
	args := []any{id}
	if viewerID != "" {
		where += ` AND (ol.visibility_scope='all_users' OR (ol.visibility_scope='targeted' AND EXISTS (SELECT 1 FROM ` + s.table("operation_log_viewers") + ` auth WHERE auth.operation_log_id=ol.id AND auth.system_account_id=?)))`
		args = append(args, viewerID)
	}
	q := `SELECT ol.operation_key,ol.resource_type,COALESCE(ol.resource_id,''),COALESCE(ol.resource_name,''),ol.visibility_scope,ol.detail_level FROM ` + s.table("operation_logs") + ` ol WHERE ` + where + ` LIMIT 1`
	var detail DetailSupplement
	var logLevel string
	err := s.db.QueryRowContext(ctx, s.bind(q), args...).Scan(&detail.OperationKey, &detail.ResourceType, &detail.ResourceID, &detail.ResourceName, &detail.VisibilityScope, &logLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return DetailSupplement{}, false, nil
	}
	if err != nil {
		return DetailSupplement{}, false, err
	}
	full := viewerID == ""
	if viewerID != "" {
		err = s.db.QueryRowContext(ctx, s.bind(`SELECT EXISTS(SELECT 1 FROM `+s.table("operation_log_viewers")+` WHERE operation_log_id=? AND system_account_id=? AND detail_level='full')`), id, viewerID).Scan(&full)
		if err != nil {
			return DetailSupplement{}, false, err
		}
		if !full || logLevel != "full" {
			detail.Changes = []Change{}
			return detail, true, nil
		}
	}
	var changes string
	q = `SELECT changes_json,COALESCE(method,''),COALESCE(path,''),COALESCE(client_ip,'') FROM ` + s.table("operation_logs") + ` WHERE id=? LIMIT 1`
	if err = s.db.QueryRowContext(ctx, s.bind(q), id).Scan(&changes, &detail.Method, &detail.Path, &detail.ClientIP); err != nil {
		return DetailSupplement{}, false, err
	}
	_ = json.Unmarshal([]byte(changes), &detail.Changes)
	if detail.Changes == nil {
		detail.Changes = []Change{}
	}
	if viewerID != "" {
		detail.ClientIP = ""
	}
	targetRows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,target_type,COALESCE(target_id,''),COALESCE(target_name,''),COALESCE(target_owner_system_account_id,''),relation FROM `+s.table("operation_log_targets")+` WHERE operation_log_id=? ORDER BY created_at,id`), id)
	if err != nil {
		return DetailSupplement{}, false, err
	}
	defer targetRows.Close()
	targetOwnerIDs := make([]string, 0)
	for targetRows.Next() {
		var t DetailTarget
		var ownerID string
		if err = targetRows.Scan(&t.ID, &t.TargetType, &t.TargetID, &t.TargetName, &ownerID, &t.Relation); err != nil {
			return DetailSupplement{}, false, err
		}
		targetOwnerIDs = append(targetOwnerIDs, ownerID)
		detail.Targets = append(detail.Targets, t)
	}
	if err = targetRows.Err(); err != nil {
		return DetailSupplement{}, false, err
	}
	if viewerID == "" {
		viewerRows, err := s.db.QueryContext(ctx, s.bind(`SELECT system_account_id,visibility_reason,detail_level FROM `+s.table("operation_log_viewers")+` WHERE operation_log_id=? ORDER BY created_at,system_account_id`), id)
		if err != nil {
			return DetailSupplement{}, false, err
		}
		defer viewerRows.Close()
		for viewerRows.Next() {
			var v DetailViewer
			if err = viewerRows.Scan(&v.SystemAccountID, &v.VisibilityReason, &v.DetailLevel); err != nil {
				return DetailSupplement{}, false, err
			}
			detail.Viewers = append(detail.Viewers, v)
		}
		if err = viewerRows.Err(); err != nil {
			return DetailSupplement{}, false, err
		}
	}
	ids := make([]string, 0, len(targetOwnerIDs)+len(detail.Viewers))
	ids = append(ids, targetOwnerIDs...)
	for _, viewer := range detail.Viewers {
		ids = append(ids, viewer.SystemAccountID)
	}
	names, err := s.accountNames(ctx, ids)
	if err != nil {
		return DetailSupplement{}, false, err
	}
	for index := range detail.Targets {
		detail.Targets[index].TargetOwnerSystemAccountName = names[targetOwnerIDs[index]]
	}
	for index := range detail.Viewers {
		detail.Viewers[index].SystemAccountName = names[detail.Viewers[index].SystemAccountID]
	}
	if detail.Targets == nil {
		detail.Targets = []DetailTarget{}
	}
	if detail.Viewers == nil {
		detail.Viewers = []DetailViewer{}
	}
	return detail, true, nil
}

func listAccountIDs(items []ListItem) []string {
	ids := make([]string, 0, len(items)*2)
	for _, item := range items {
		ids = append(ids, item.ActorSystemAccountID, item.OperationScopeSystemAccountID)
	}
	return ids
}

func (s *sqlStore) accountNames(ctx context.Context, ids []string) (map[string]string, error) {
	result := map[string]string{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, found := result[id]; found {
			continue
		}
		var name string
		var err error
		if s.mode == ModeSQLite {
			err = s.businessDB.QueryRowContext(ctx, "SELECT COALESCE(NULLIF(display_name,''),NULLIF(username,''),id) FROM system_accounts WHERE id=?", id).Scan(&name)
		} else {
			err = s.db.QueryRowContext(ctx, "SELECT COALESCE(NULLIF(display_name,''),NULLIF(username,''),id) FROM juhe_business.system_accounts WHERE id=$1", id).Scan(&name)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read F4 system account name %q: %w", id, err)
		}
		result[id] = name
	}
	return result, nil
}

func (s *sqlStore) CleanupRetention(ctx context.Context, l OwnerLease, cutoff time.Time, limit int) (int64, error) {
	if limit < 1 {
		limit = 1
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return 0, err
	}
	q := `SELECT id FROM ` + s.table("operation_logs") + ` WHERE created_at<? ORDER BY created_at,id LIMIT ?`
	rows, err := tx.QueryContext(ctx, s.bind(q), cutoff.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("operation_logs")+` WHERE id=?`), id); err != nil {
			return 0, err
		}
	}
	if err = s.verifyLease(ctx, tx, l); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
