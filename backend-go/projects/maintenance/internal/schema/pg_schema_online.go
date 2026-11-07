// 在线结构收敛（多节点发布设计 §6.3）：expand --online 专用的索引执行路径。
// 普通 EnsurePostgresFiltered 保持原「SET search_path; DDL」批执行；本文件只
// 对 CREATE INDEX 使用固定连接 + CONCURRENTLY，避免 expand 期间阻塞写入。
package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	createIndexNameRE = regexp.MustCompile(`(?i)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?:"([^"]+)"|([a-zA-Z0-9_]+))`)
	concurrentlyRE    = regexp.MustCompile(`(?i)\s+CONCURRENTLY\b`)
)

// createIndexSpec 是从 CREATE INDEX 语句解析出的在线构建所需字段。
// Suffix 从列清单的 "(" 开始，保留 INCLUDE/WHERE/TABLESPACE 等尾部语义。
type createIndexSpec struct {
	Unique      bool
	IndexName   string
	TableSchema string
	TableName   string
	Suffix      string
}

// EnsurePostgresFilteredOnline 与 EnsurePostgresFiltered 同语义，但加法相中的
// CREATE INDEX 走 CONCURRENTLY（专用连接、逐条、不在事务内），其余语句仍按
// 既有语句级锁重试执行。只应由 upgrade --phase=expand --online 调用。
func EnsurePostgresFilteredOnline(ctx context.Context, db *sql.DB, phase EnsurePhase) (PGResult, error) {
	createdSchemas := make(map[string]bool)
	executed := 0
	for i, statement := range postgresSchemaStatements {
		if !phaseIncludes(phase, statement.SQL) {
			continue
		}
		if !createdSchemas[statement.SchemaName] {
			createdSchemas[statement.SchemaName] = true
			createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quotePGIdentifier(statement.SchemaName))
			if err := execPostgresStatementWithLockRetry(ctx, db, createSchema, createSchema); err != nil {
				return PGResult{}, fmt.Errorf("create postgres schema %s: %w", statement.SchemaName, err)
			}
		}
		if phase != EnsureDestructive && isIndexStatement(statement.SQL) {
			if err := execPostgresIndexConcurrently(ctx, db, statement.SchemaName, statement.SQL); err != nil {
				return PGResult{}, fmt.Errorf("postgres online index statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
			}
		} else {
			execSQL := fmt.Sprintf("SET search_path TO %s, public;\n%s", quotePGIdentifier(statement.SchemaName), statement.SQL)
			if err := execPostgresStatementWithLockRetry(ctx, db, execSQL, statement.SQL); err != nil {
				return PGResult{}, fmt.Errorf("postgres schema statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
			}
		}
		executed++
	}
	if phase != EnsureDestructive {
		if err := VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err != nil {
			return PGResult{}, fmt.Errorf("verify accounts.capability_domain shape: %w", err)
		}
	}
	return PGResult{SchemaCount: len(createdSchemas), StatementCount: executed}, nil
}

func isIndexStatement(sqlText string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sqlText))
	return strings.HasPrefix(upper, "CREATE INDEX ") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX ")
}

// concurrentIndexSQL 把 CREATE [UNIQUE] INDEX [IF NOT EXISTS] 改写为
// CONCURRENTLY 形态；已带 CONCURRENTLY 的语句原样返回。
func concurrentIndexSQL(sqlText string) string {
	upper := strings.ToUpper(sqlText)
	if strings.Contains(upper, " INDEX CONCURRENTLY ") {
		return sqlText
	}
	switch {
	case strings.HasPrefix(upper, "CREATE UNIQUE INDEX "):
		return "CREATE UNIQUE INDEX CONCURRENTLY " + sqlText[len("CREATE UNIQUE INDEX "):]
	case strings.HasPrefix(upper, "CREATE INDEX "):
		return "CREATE INDEX CONCURRENTLY " + sqlText[len("CREATE INDEX "):]
	default:
		return sqlText
	}
}

func indexNameFromStatement(sqlText string) (string, bool) {
	match := createIndexNameRE.FindStringSubmatch(strings.TrimSpace(sqlText))
	if len(match) != 3 {
		return "", false
	}
	if match[1] != "" {
		return match[1], true
	}
	return match[2], true
}

// parseCreateIndexStatement 解析 CREATE [UNIQUE] INDEX 的表名、索引名与列清单尾部。
// defaultSchema 用于未限定表名的语句（调用方已 SET search_path）。
func parseCreateIndexStatement(statement, defaultSchema string) (createIndexSpec, bool) {
	trimmed := strings.TrimSpace(statement)
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "CREATE INDEX ") && !strings.HasPrefix(upper, "CREATE UNIQUE INDEX ") {
		return createIndexSpec{}, false
	}
	indexName, ok := indexNameFromStatement(trimmed)
	if !ok {
		return createIndexSpec{}, false
	}
	onPos := strings.Index(upper, " ON ")
	if onPos < 0 {
		return createIndexSpec{}, false
	}
	rest := strings.TrimSpace(trimmed[onPos+len(" ON "):])
	if strings.HasPrefix(strings.ToUpper(rest), "ONLY ") {
		rest = strings.TrimSpace(rest[len("ONLY "):])
	}
	tableToken, suffix := scanPGIdentifierToken(rest)
	if tableToken == "" {
		return createIndexSpec{}, false
	}
	suffix = strings.TrimSpace(suffix)
	if !validCreateIndexSuffix(suffix) {
		return createIndexSpec{}, false
	}
	parts, ok := splitPGQualifiedIdentifier(tableToken)
	if !ok || len(parts) == 0 || len(parts) > 2 {
		return createIndexSpec{}, false
	}
	tableSchema := defaultSchema
	tableName := parts[0]
	if len(parts) == 2 {
		tableSchema = parts[0]
		tableName = parts[1]
	}
	return createIndexSpec{
		Unique:      strings.HasPrefix(upper, "CREATE UNIQUE INDEX "),
		IndexName:   indexName,
		TableSchema: tableSchema,
		TableName:   tableName,
		Suffix:      suffix,
	}, true
}

// validCreateIndexSuffix 校验表名之后是列清单，允许中间出现 USING <method>。
func validCreateIndexSuffix(suffix string) bool {
	trimmed := strings.TrimSpace(suffix)
	if strings.HasPrefix(strings.ToUpper(trimmed), "USING ") {
		rest := strings.TrimSpace(trimmed[len("USING "):])
		method, after := scanPGIdentifierToken(rest)
		if method == "" {
			return false
		}
		trimmed = strings.TrimSpace(after)
	}
	return strings.HasPrefix(trimmed, "(")
}

// scanPGIdentifierToken 从 ON 之后截取表名 token，支持双引号标识符与
// schema.table 形式；返回剩余部分（通常从 "(" 开始）。
func scanPGIdentifierToken(s string) (string, string) {
	i := 0
	for i < len(s) && isPGSQLSpace(s[i]) {
		i++
	}
	start := i
	inQuote := false
	for i < len(s) {
		ch := s[i]
		if ch == '"' {
			if inQuote && i+1 < len(s) && s[i+1] == '"' {
				i += 2
				continue
			}
			inQuote = !inQuote
			i++
			continue
		}
		if !inQuote && (isPGSQLSpace(ch) || ch == '(') {
			break
		}
		i++
	}
	return s[start:i], s[i:]
}

func splitPGQualifiedIdentifier(token string) ([]string, bool) {
	var parts []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(token); i++ {
		ch := token[i]
		if ch == '"' {
			if inQuote && i+1 < len(token) && token[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if ch == '.' && !inQuote {
			parts = append(parts, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(ch)
	}
	if inQuote {
		return nil, false
	}
	parts = append(parts, b.String())
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, false
		}
	}
	return parts, true
}

func isPGSQLSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func stripConcurrentIndexSQL(statement string) string {
	return concurrentlyRE.ReplaceAllString(statement, "")
}

func quotePGQualified(schemaName, objectName string) string {
	return quotePGIdentifier(schemaName) + "." + quotePGIdentifier(objectName)
}

func parentIndexSQL(statement string, spec createIndexSpec) string {
	onPos := strings.Index(strings.ToUpper(statement), " ON ")
	prefix := strings.TrimSpace(stripConcurrentIndexSQL(statement[:onPos]))
	return prefix + " ON ONLY " + quotePGQualified(spec.TableSchema, spec.TableName) + " " + spec.Suffix
}

func partitionIndexSQL(spec createIndexSpec, partitionSchema, partitionTable, partitionIndex string) string {
	unique := ""
	if spec.Unique {
		unique = "UNIQUE "
	}
	return "CREATE " + unique + "INDEX CONCURRENTLY IF NOT EXISTS " + quotePGIdentifier(partitionIndex) +
		" ON " + quotePGQualified(partitionSchema, partitionTable) + " " + spec.Suffix
}

func partitionIndexName(base, partitionSchema, partitionTable string) string {
	sum := sha256.Sum256([]byte(partitionSchema + "." + partitionTable))
	suffix := "_p_" + hex.EncodeToString(sum[:4])
	const maxPGIdentifierBytes = 63
	if len(base)+len(suffix) > maxPGIdentifierBytes {
		base = base[:maxPGIdentifierBytes-len(suffix)]
	}
	return base + suffix
}

// execPostgresIndexConcurrently 在专用连接上执行一条 CREATE INDEX CONCURRENTLY。
// 重试前清理上一轮失败留下的 invalid index，避免 IF NOT EXISTS 跳过残骸。
func execPostgresIndexConcurrently(ctx context.Context, db *sql.DB, schemaName, statement string) error {
	spec, ok := parseCreateIndexStatement(statement, schemaName)
	if !ok {
		return fmt.Errorf("无法从在线 DDL 语句解析索引定义: %s", pgStatementLogSummary(statement))
	}
	indexName := spec.IndexName
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取在线 DDL 专用连接: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET search_path TO "+quotePGIdentifier(schemaName)+", public"); err != nil {
		return fmt.Errorf("设置在线 DDL search_path: %w", err)
	}
	partitioned, err := pgTableIsPartitioned(ctx, conn, spec.TableSchema, spec.TableName)
	if err != nil {
		return err
	}
	if partitioned {
		return execPostgresPartitionedIndex(ctx, conn, statement, spec)
	}
	concurrentSQL := concurrentIndexSQL(statement)
	for attempt := 0; ; attempt++ {
		if err := dropInvalidIndex(ctx, conn, schemaName, indexName); err != nil {
			return fmt.Errorf("清理 invalid index %s: %w", indexName, err)
		}
		_, err := conn.ExecContext(ctx, concurrentSQL)
		if err == nil {
			return nil
		}
		sqlState, retryable := pgTransientLockSQLState(err)
		if !retryable || attempt >= pgLockRetryMaxRetries {
			return err
		}
		delay := pgLockRetryDelay(attempt)
		fmt.Fprintf(os.Stderr,
			"postgres online index 瞬时锁冲突，准备重试（SQLSTATE=%s，第 %d/%d 次，退避 %s）：%s\n",
			sqlState, attempt+1, pgLockRetryMaxRetries, delay, pgStatementLogSummary(statement))
		if sleepErr := pgLockRetrySleep(ctx, delay); sleepErr != nil {
			return err
		}
	}
}

type pgIndexState struct {
	Exists bool
	Valid  bool
}

func readPGIndexState(ctx context.Context, conn *sql.Conn, schemaName, indexName string) (pgIndexState, error) {
	var valid bool
	err := conn.QueryRowContext(ctx,
		`SELECT i.indisvalid
		 FROM pg_index i
		 JOIN pg_class c ON c.oid = i.indexrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`,
		schemaName, indexName).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return pgIndexState{}, nil
	}
	if err != nil {
		return pgIndexState{}, err
	}
	return pgIndexState{Exists: true, Valid: valid}, nil
}

func pgTableIsPartitioned(ctx context.Context, conn *sql.Conn, schemaName, tableName string) (bool, error) {
	var relkind string
	err := conn.QueryRowContext(ctx,
		`SELECT c.relkind
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`,
		schemaName, tableName).Scan(&relkind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("在线 DDL 目标表不存在: %s", quotePGQualified(schemaName, tableName))
	}
	if err != nil {
		return false, fmt.Errorf("读取在线 DDL 目标表类型: %w", err)
	}
	return relkind == "p", nil
}

type pgPartition struct {
	Schema  string
	Table   string
	Relkind string
}

func listPGPartitions(ctx context.Context, conn *sql.Conn, schemaName, tableName string) ([]pgPartition, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT n.nspname, c.relname, c.relkind
		 FROM pg_inherits i
		 JOIN pg_class p ON p.oid = i.inhparent
		 JOIN pg_namespace pn ON pn.oid = p.relnamespace
		 JOIN pg_class c ON c.oid = i.inhrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE pn.nspname = $1 AND p.relname = $2
		 ORDER BY n.nspname, c.relname`,
		schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var partitions []pgPartition
	for rows.Next() {
		var partition pgPartition
		if err := rows.Scan(&partition.Schema, &partition.Table, &partition.Relkind); err != nil {
			return nil, err
		}
		partitions = append(partitions, partition)
	}
	return partitions, rows.Err()
}

func pgIndexAttached(ctx context.Context, conn *sql.Conn, parentSchema, parentIndex, partSchema, partIndex string) (bool, error) {
	var attached bool
	err := conn.QueryRowContext(ctx,
		`SELECT EXISTS (
		   SELECT 1
		   FROM pg_inherits i
		   JOIN pg_class p ON p.oid = i.inhparent
		   JOIN pg_namespace pn ON pn.oid = p.relnamespace
		   JOIN pg_class c ON c.oid = i.inhrelid
		   JOIN pg_namespace cn ON cn.oid = c.relnamespace
		   WHERE pn.nspname = $1 AND p.relname = $2
		     AND cn.nspname = $3 AND c.relname = $4
		 )`,
		parentSchema, parentIndex, partSchema, partIndex).Scan(&attached)
	return attached, err
}

func dropPGIndexConcurrently(ctx context.Context, conn *sql.Conn, schemaName, indexName string) error {
	_, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+quotePGQualified(schemaName, indexName))
	return err
}

// execPostgresPartitionedIndex 在线构建分区表索引：
//  1. CREATE INDEX ... ON ONLY parent 建立无效父索引（不递归锁全部分区）；
//  2. 逐分区 CREATE INDEX CONCURRENTLY；
//  3. ALTER INDEX parent ATTACH PARTITION，全部 attach 后父索引转为有效。
//
// PostgreSQL 不支持在分区父表上直接 CREATE INDEX CONCURRENTLY，因此不能
// 与普通表共用同一条路径。
func execPostgresPartitionedIndex(ctx context.Context, conn *sql.Conn, statement string, spec createIndexSpec) error {
	partitions, err := listPGPartitions(ctx, conn, spec.TableSchema, spec.TableName)
	if err != nil {
		return fmt.Errorf("读取分区表 %s 的分区: %w", quotePGQualified(spec.TableSchema, spec.TableName), err)
	}
	parentState, err := readPGIndexState(ctx, conn, spec.TableSchema, spec.IndexName)
	if err != nil {
		return fmt.Errorf("读取父索引 %s 状态: %w", spec.IndexName, err)
	}
	if parentState.Exists && parentState.Valid {
		return nil
	}
	if len(partitions) == 0 {
		if parentState.Exists {
			if err := dropPGIndexConcurrently(ctx, conn, spec.TableSchema, spec.IndexName); err != nil {
				return fmt.Errorf("清理无效父索引 %s: %w", spec.IndexName, err)
			}
		}
		if _, err := conn.ExecContext(ctx, stripConcurrentIndexSQL(statement)); err != nil {
			return fmt.Errorf("创建空分区表索引 %s: %w", spec.IndexName, err)
		}
		return nil
	}
	if !parentState.Exists {
		if _, err := conn.ExecContext(ctx, parentIndexSQL(statement, spec)); err != nil {
			return fmt.Errorf("创建分区父索引 %s (ON ONLY): %w", spec.IndexName, err)
		}
	}
	for _, partition := range partitions {
		if partition.Relkind == "p" {
			return fmt.Errorf("分区 %s 仍是分区父表，当前在线索引只支持一级分区",
				quotePGQualified(partition.Schema, partition.Table))
		}
		partIndex := partitionIndexName(spec.IndexName, partition.Schema, partition.Table)
		partState, err := readPGIndexState(ctx, conn, partition.Schema, partIndex)
		if err != nil {
			return fmt.Errorf("读取分区索引 %s 状态: %w", partIndex, err)
		}
		if partState.Exists && !partState.Valid {
			if err := dropPGIndexConcurrently(ctx, conn, partition.Schema, partIndex); err != nil {
				return fmt.Errorf("清理无效分区索引 %s: %w", partIndex, err)
			}
			partState = pgIndexState{}
		}
		if !partState.Exists {
			if _, err := conn.ExecContext(ctx, partitionIndexSQL(spec, partition.Schema, partition.Table, partIndex)); err != nil {
				return fmt.Errorf("在线创建分区索引 %s: %w", quotePGQualified(partition.Schema, partIndex), err)
			}
		}
		attached, err := pgIndexAttached(ctx, conn, spec.TableSchema, spec.IndexName, partition.Schema, partIndex)
		if err != nil {
			return fmt.Errorf("检查分区索引 attach 状态: %w", err)
		}
		if attached {
			continue
		}
		attachSQL := fmt.Sprintf("ALTER INDEX %s ATTACH PARTITION %s",
			quotePGQualified(spec.TableSchema, spec.IndexName),
			quotePGQualified(partition.Schema, partIndex))
		if _, err := conn.ExecContext(ctx, attachSQL); err != nil {
			return fmt.Errorf("attach 分区索引 %s: %w", quotePGQualified(partition.Schema, partIndex), err)
		}
	}
	parentState, err = readPGIndexState(ctx, conn, spec.TableSchema, spec.IndexName)
	if err != nil {
		return fmt.Errorf("复核父索引 %s 状态: %w", spec.IndexName, err)
	}
	if !parentState.Exists || !parentState.Valid {
		return fmt.Errorf("父索引 %s 在 attach 全部分区后仍无效", quotePGQualified(spec.TableSchema, spec.IndexName))
	}
	return nil
}

func dropInvalidIndex(ctx context.Context, conn *sql.Conn, schemaName, indexName string) error {
	var valid bool
	err := conn.QueryRowContext(ctx,
		`SELECT i.indisvalid
		 FROM pg_index i
		 JOIN pg_class c ON c.oid = i.indexrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`,
		schemaName, indexName).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf(
		`DROP INDEX CONCURRENTLY IF EXISTS %s.%s`,
		quotePGIdentifier(schemaName), quotePGIdentifier(indexName)))
	return err
}
