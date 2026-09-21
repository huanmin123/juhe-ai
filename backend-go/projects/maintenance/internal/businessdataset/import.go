package businessdataset

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"path/filepath"
	"strconv"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// ImportOptions binds one import invocation. AllowedMissingColumns registers
// explicit "table.column" entries whose source column may be absent from the
// target schema; every other source-only column stays a blocker.
//
// ExpectedTargetIdentity enables the target-identity gate: when non-empty it
// must equal the ACTUAL target database name (current_database() read inside
// the import transaction); any other value blocks the import. This is the
// fail-closed "wrong database" guard; whether the actual name also matches the
// manifest's recorded TargetIdentity stays informational.
//
// ExpectedSourceIdentity enables the source-identity gate: when non-empty the
// manifest's recorded SourceIdentity must be a real database name (not a
// placeholder) and must equal the expectation exactly.
//
// 生产切流（authoritative 业务库导入）时两个期望都必须由 CLI 的
// --business-dataset-expected-target-db / --business-dataset-expected-source-db
// 显式提供并全部匹配；任一 gate 不匹配即整体阻断，绝不允许静默导入。
//
// ReplaceExisting enables in-place replacement: inside the import transaction
// and before the first INSERT, every whitelist data table is emptied with
// `DELETE FROM <table>` following the reverse of the FK topological order
// (children before parents), so authoritative production rows can replace
// built-in seed rows in an already-seeded target database. The three
// structural-only tables are never deleted and keep their existing zero-row
// assertion. With false the import stays strictly insert-only and any
// pre-existing row in a data table surfaces as a primary-key conflict that
// rolls the whole transaction back (fail-closed, unchanged default behavior).
type ImportOptions struct {
	ExpectedTargetIdentity string
	ExpectedSourceIdentity string
	AllowedMissingColumns  []string
	ReplaceExisting        bool
}

// ImportTableStat records the per-table import outcome.
type ImportTableStat struct {
	Name                     string   `json:"name"`
	ManifestRows             int64    `json:"manifestRows"`
	InsertedRows             int64    `json:"insertedRows"`
	PublicColumns            []string `json:"publicColumns"`
	DroppedSourceColumns     []string `json:"droppedSourceColumns"`
	OmittedTargetOnlyColumns []string `json:"omittedTargetOnlyColumns"`
	StructuralOnly           bool     `json:"structuralOnly"`
	SequenceName             string   `json:"sequenceName"`
	SequenceValue            int64    `json:"sequenceValue"`
	SequenceAdvanced         bool     `json:"sequenceAdvanced"`
	DigestMatch              bool     `json:"digestMatch"`
	Status                   string   `json:"status"`
}

// ImportReport is the JSON report of one ImportBusinessDataset call. Ready
// requires zero blockers, verified files, an empty structural proof, matched
// digests and a committed transaction.
type ImportReport struct {
	Mode                    string             `json:"mode"`
	WritableTransaction     bool               `json:"writableTransaction"`
	InDir                   string             `json:"inDir"`
	ManifestHash            string             `json:"manifestHash"`
	ManifestSourceIdentity  string             `json:"manifestSourceIdentity"`
	ManifestTargetIdentity  string             `json:"manifestTargetIdentity"`
	TargetIdentity          string             `json:"targetIdentity"`
	TargetIdentityMatch     bool               `json:"targetIdentityMatch"`
	FilesVerified           bool               `json:"filesVerified"`
	ForeignKeysInspected    int                `json:"foreignKeysInspected"`
	TableOrder              []string           `json:"tableOrder"`
	Tables                  []*ImportTableStat `json:"tables"`
	StructuralEmptyVerified bool               `json:"structuralEmptyVerified"`
	Verification            string             `json:"verification"`
	Committed               bool               `json:"committed"`
	Blockers                []string           `json:"blockers"`
}

// Ready reports whether the import completed all-or-nothing with every
// manifest assertion verified. It is the exit-3 gate of the CLI runner.
func (r ImportReport) Ready() bool {
	return len(r.Blockers) == 0 && r.FilesVerified && r.StructuralEmptyVerified && r.Verification == "match" && r.Committed
}

// bdDatasetTable holds one decoded dataset file: its column set and rows.
type bdDatasetTable struct {
	Columns []string
	Rows    []map[string]any
}

// ImportBusinessDataset replays one exported dataset directory into the
// authoritative target database inside a single transaction: file hashes are
// verified first, columns are mapped (public copied / target-only omitted and
// recorded / source-only blocked unless explicitly allowed), tables are
// ordered by the target catalog's foreign key graph (canonical order only as
// tiebreak; cycles and edges leaving the whitelist are blockers), rows are
// inserted with explicit column lists, single-column integer sequences are
// advanced to max(id), and every table is read back and digest-compared
// against the manifest. Any blocker rolls the whole transaction back.
func ImportBusinessDataset(ctx context.Context, db *sql.DB, inDir string, opts ImportOptions) (ImportReport, error) {
	report := ImportReport{Mode: "import", InDir: inDir}
	if db == nil {
		return report, errors.New("business dataset 导入数据库未初始化")
	}
	addBlocker := func(format string, args ...any) {
		report.Blockers = append(report.Blockers, fmt.Sprintf(format, args...))
	}

	manifest, _, err := bdLoadManifest(inDir)
	if err != nil {
		return report, err
	}
	report.ManifestHash = manifest.ManifestHash
	report.ManifestSourceIdentity = manifest.SourceIdentity
	report.ManifestTargetIdentity = manifest.TargetIdentity
	if validationErrors := contracts.ValidateBusinessDatasetManifest(manifest); len(validationErrors) > 0 {
		report.Blockers = append(report.Blockers, validationErrors...)
		return report, nil
	}
	// 源库恒等门：期望值非空时，manifest 记录的源库标识必须是真实库名
	//（非占位值）且与期望严格一致；否则整体阻断。
	if expectedSource := strings.TrimSpace(opts.ExpectedSourceIdentity); expectedSource != "" {
		if IsPlaceholderIdentity(manifest.SourceIdentity) {
			addBlocker("manifest 源库标识是占位值（%q），无法完成源库恒等校验", manifest.SourceIdentity)
		} else if strings.TrimSpace(manifest.SourceIdentity) != expectedSource {
			addBlocker("manifest 源库标识不匹配（manifest=%s 期望=%s）", manifest.SourceIdentity, expectedSource)
		}
	}

	// Pass 1: verify every data file hash and decode its rows.
	dataset := map[string]*bdDatasetTable{}
	entries := map[string]contracts.BusinessDatasetTableEntry{}
	for _, entry := range manifest.Tables {
		entries[entry.Name] = entry
		if contracts.IsBusinessDatasetStructuralOnlyTable(entry.Name) {
			continue
		}
		sha256Hex, columns, rows, rowCount, err := bdReadDatasetFile(filepath.Join(inDir, entry.File))
		if err != nil {
			addBlocker("读取表 %s 数据文件 %s: %v", entry.Name, entry.File, err)
			continue
		}
		if sha256Hex != strings.TrimSpace(entry.Sha256) {
			addBlocker("表 %s 数据文件哈希不匹配（manifest=%s 文件=%s）", entry.Name, entry.Sha256, sha256Hex)
		}
		if rowCount != entry.Rows {
			addBlocker("表 %s 数据文件行数不匹配（manifest=%d 文件=%d）", entry.Name, entry.Rows, rowCount)
		}
		dataset[entry.Name] = &bdDatasetTable{Columns: columns, Rows: rows}
	}
	report.FilesVerified = len(report.Blockers) == 0

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return report, fmt.Errorf("开启导入事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var readOnly string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		return report, fmt.Errorf("校验导入事务写入模式: %w", err)
	}
	report.WritableTransaction = !strings.EqualFold(strings.TrimSpace(readOnly), "on")
	if !report.WritableTransaction {
		addBlocker("目标库导入事务是只读的（transaction_read_only=%s），拒绝导入", strings.TrimSpace(readOnly))
		return report, nil
	}

	var targetIdentity string
	if err := tx.QueryRowContext(ctx, "SELECT current_database()").Scan(&targetIdentity); err != nil {
		return report, fmt.Errorf("读取目标库标识: %w", err)
	}
	report.TargetIdentity = strings.TrimSpace(targetIdentity)
	report.TargetIdentityMatch = report.TargetIdentity == strings.TrimSpace(manifest.TargetIdentity)
	// 期望值必须与实际目标 current_database() 严格一致（fail-closed）；实际
	// 库名与 manifest 记录是否一致作为信息性记录（TargetIdentityMatch），
	// 不做硬门。
	if expected := strings.TrimSpace(opts.ExpectedTargetIdentity); expected != "" && expected != report.TargetIdentity {
		addBlocker("目标库标识不匹配（manifest=%s 实际=%s 期望=%s）", manifest.TargetIdentity, report.TargetIdentity, expected)
	}

	// Target catalog: columns and primary keys for all 40 tables.
	type bdTargetTable struct {
		Columns    []bdColumn
		ColumnSet  map[string]struct{}
		PrimaryKey []string
	}
	target := map[string]*bdTargetTable{}
	for _, table := range contracts.BusinessDatasetTables {
		columns, err := bdLoadColumns(ctx, tx, table)
		if err != nil {
			return report, err
		}
		if len(columns) == 0 {
			addBlocker("目标库缺少 whitelist 表 %s.%s", SchemaName, table)
			continue
		}
		entry := &bdTargetTable{Columns: columns, ColumnSet: map[string]struct{}{}}
		for _, column := range columns {
			entry.ColumnSet[column.Name] = struct{}{}
		}
		primaryKey, err := bdLoadPrimaryKey(ctx, tx, table)
		if err != nil {
			return report, err
		}
		entry.PrimaryKey = primaryKey
		target[table] = entry
	}

	// Foreign key graph from the target catalog, restricted to the whitelist.
	foreignKeys, err := bdLoadForeignKeys(ctx, tx)
	if err != nil {
		return report, err
	}
	report.ForeignKeysInspected = len(foreignKeys)
	whitelist := contracts.BusinessDatasetTableNameSet()
	var edges [][2]int
	for _, fk := range foreignKeys {
		_, fromIn := whitelist[fk.FromTable]
		_, toIn := whitelist[fk.ToTable]
		if !fromIn {
			continue
		}
		if !toIn {
			addBlocker("外键 %s 从 whitelist 表 %s 指向白名单之外的表 %s", fk.ConstraintName, fk.FromTable, fk.ToTable)
			continue
		}
		if fk.ToTable == fk.FromTable {
			// 自引用在表级拓扑中不成边（行级父先于子单独排序）。
			continue
		}
		edges = append(edges, [2]int{bdCanonicalIndex(fk.ToTable), bdCanonicalIndex(fk.FromTable)})
	}

	// Topological table order over the catalog graph; canonical order is only
	// the deterministic tiebreak, never the correctness source.
	tableOrder, cycle := bdTopologicalOrder(edges)
	if cycle {
		addBlocker("目标库外键图存在环，无法拓扑排序")
	}
	report.TableOrder = tableOrder

	// Column mapping three-way split per data table.
	allowed := map[string]struct{}{}
	for _, entry := range opts.AllowedMissingColumns {
		allowed[strings.TrimSpace(entry)] = struct{}{}
	}
	stats := map[string]*ImportTableStat{}
	for _, table := range contracts.BusinessDatasetTables {
		stat := &ImportTableStat{
			Name:           table,
			ManifestRows:   entries[table].Rows,
			StructuralOnly: contracts.IsBusinessDatasetStructuralOnlyTable(table),
			Status:         "pending",
		}
		stats[table] = stat
		report.Tables = append(report.Tables, stat)
	}
	selfReferences := map[string][]bdForeignKey{}
	for _, table := range contracts.BusinessDatasetTables {
		targetTable := target[table]
		data, hasData := dataset[table]
		if contracts.IsBusinessDatasetStructuralOnlyTable(table) || !hasData {
			continue
		}
		stat := stats[table]
		if targetTable == nil {
			continue
		}
		if data.Columns == nil {
			continue
		}
		for _, column := range data.Columns {
			if _, ok := targetTable.ColumnSet[column]; ok {
				stat.PublicColumns = append(stat.PublicColumns, column)
				continue
			}
			if _, ok := allowed[table+"."+column]; ok {
				stat.DroppedSourceColumns = append(stat.DroppedSourceColumns, column)
				continue
			}
			addBlocker("表 %s 源列 %s 在目标库不存在且未登记允许缺失", table, column)
		}
		for _, column := range targetTable.Columns {
			isSource := false
			for _, name := range data.Columns {
				if name == column.Name {
					isSource = true
					break
				}
			}
			if !isSource {
				stat.OmittedTargetOnlyColumns = append(stat.OmittedTargetOnlyColumns, column.Name)
			}
		}
	}
	// Self-referencing foreign keys drive the row-level parent-first ordering.
	for _, fk := range foreignKeys {
		if _, fromIn := whitelist[fk.FromTable]; !fromIn || fk.ToTable != fk.FromTable {
			continue
		}
		selfReferences[fk.FromTable] = append(selfReferences[fk.FromTable], fk)
	}

	if len(report.Blockers) > 0 {
		return report, nil
	}

	// ReplaceExisting：在导入事务内、INSERT 之前，按 FK 拓扑序的逆序（子先
	// 父后）清空每张数据表，使权威生产数据整体替换内建种子行；结构-only 3 表
	// 不 DELETE，维持其零行断言。任一 DELETE 失败即运行错误并整体回滚。
	if opts.ReplaceExisting {
		for index := len(tableOrder) - 1; index >= 0; index-- {
			table := tableOrder[index]
			if contracts.IsBusinessDatasetStructuralOnlyTable(table) {
				continue
			}
			qualified, err := bdQualifiedTable(table)
			if err != nil {
				return report, err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+qualified); err != nil {
				return report, fmt.Errorf("清空表 %s: %w", table, err)
			}
		}
	}

	// Inserts in topological order with deterministic row-level ordering for
	// self-referencing tables.
	for _, table := range tableOrder {
		stat := stats[table]
		if contracts.IsBusinessDatasetStructuralOnlyTable(table) {
			continue
		}
		data, hasData := dataset[table]
		if !hasData || len(data.Rows) == 0 {
			continue
		}
		rows := data.Rows
		if len(selfReferences[table]) > 0 {
			ordered, blockers := bdOrderRowsParentFirst(table, data.Rows, selfReferences[table])
			if len(blockers) > 0 {
				report.Blockers = append(report.Blockers, blockers...)
				break
			}
			rows = ordered
		}
		if len(stat.PublicColumns) == 0 {
			// 全部源列被登记放行：无列可插，交由读回校验按行数差报 blocker。
			continue
		}
		statement, err := bdInsert(table, stat.PublicColumns)
		if err != nil {
			return report, err
		}
		for _, row := range rows {
			args := make([]any, 0, len(stat.PublicColumns))
			for _, column := range stat.PublicColumns {
				value, err := bdParamValue(row[column])
				if err != nil {
					return report, fmt.Errorf("表 %s 列 %s: %w", table, column, err)
				}
				args = append(args, value)
			}
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return report, fmt.Errorf("插入表 %s: %w", table, err)
			}
			stat.InsertedRows++
		}
	}

	if len(report.Blockers) > 0 {
		return report, nil
	}

	// Sequence advance for single-column primary keys (composite primary keys
	// are skipped by contract).
	for _, table := range contracts.BusinessDatasetTables {
		if contracts.IsBusinessDatasetStructuralOnlyTable(table) {
			continue
		}
		targetTable := target[table]
		if targetTable == nil || len(targetTable.PrimaryKey) != 1 {
			continue
		}
		pkColumn := targetTable.PrimaryKey[0]
		var sequence sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT pg_get_serial_sequence($1, $2)", SchemaName+"."+table, pkColumn).Scan(&sequence); err != nil {
			return report, fmt.Errorf("查询表 %s 序列: %w", table, err)
		}
		if !sequence.Valid || strings.TrimSpace(sequence.String) == "" {
			continue
		}
		sequenceName := strings.TrimSpace(sequence.String)
		stats[table].SequenceName = sequenceName
		stats[table].SequenceAdvanced = true
		stats[table].SequenceValue = 1
		isCalled := false
		if stats[table].InsertedRows > 0 {
			qualified, err := bdQualifiedTable(table)
			if err != nil {
				return report, err
			}
			quoted, err := quoteIdent(pkColumn)
			if err != nil {
				return report, err
			}
			var maxValue any
			if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT MAX(%s) FROM %s", quoted, qualified)).Scan(&maxValue); err != nil {
				return report, fmt.Errorf("查询表 %s 最大主键: %w", table, err)
			}
			normalized, err := bdNormalizeValue(maxValue)
			if err != nil {
				return report, fmt.Errorf("规范化表 %s 最大主键: %w", table, err)
			}
			maxID, ok := normalized.(int64)
			if !ok {
				return report, fmt.Errorf("表 %s 序列主键 %s 的最大值不是整数（%T）", table, pkColumn, maxValue)
			}
			stats[table].SequenceValue = maxID
			isCalled = true
		}
		if _, err := tx.ExecContext(ctx, "SELECT setval($1, $2, $3)", sequenceName, stats[table].SequenceValue, isCalled); err != nil {
			return report, fmt.Errorf("推进表 %s 序列 %s: %w", table, sequenceName, err)
		}
	}

	// Readback verification inside the same transaction: row count and
	// per-table digest must match the manifest before commit.
	structuralVerified := true
	allMatch := true
	for _, table := range contracts.BusinessDatasetTables {
		stat := stats[table]
		targetTable := target[table]
		if targetTable == nil {
			allMatch = false
			continue
		}
		readbackColumns := stat.PublicColumns
		if len(readbackColumns) == 0 {
			for _, column := range targetTable.Columns {
				readbackColumns = append(readbackColumns, column.Name)
			}
		}
		order := targetTable.PrimaryKey
		if len(order) == 0 {
			order = append([]string(nil), readbackColumns...)
		}
		query, err := bdSelectAll(table, readbackColumns, order)
		if err != nil {
			return report, err
		}
		digest := newBDDigest()
		var readbackRows int64
		var readbackData []map[string]any
		if err := bdScanRows(ctx, tx, "读回表 "+table, query, readbackColumns, func(row map[string]any) error {
			readbackRows++
			readbackData = append(readbackData, row)
			return digest.writeRow(row)
		}); err != nil {
			return report, err
		}
		entry := entries[table]
		if contracts.IsBusinessDatasetStructuralOnlyTable(table) {
			if readbackRows != 0 {
				structuralVerified = false
				allMatch = false
				addBlocker("结构-only 表 %s 在目标库非空（%d 行）", table, readbackRows)
			} else {
				stat.DigestMatch = true
			}
			if !stat.DigestMatch {
				allMatch = false
			}
			continue
		}
		switch {
		case len(stat.PublicColumns) == 0:
			if readbackRows != 0 {
				allMatch = false
				addBlocker("表 %s 无公共列但目标库已有 %d 行", table, readbackRows)
			} else if entry.Rows == 0 {
				stat.DigestMatch = true
			} else {
				allMatch = false
				addBlocker("表 %s 无公共列可插入但 manifest 记录 %d 行", table, entry.Rows)
			}
		case len(stat.DroppedSourceColumns) == 0:
			// 公共投影与源列完全一致：digest 算法与导出一致，直接与 manifest 对比。
			if readbackRows == entry.Rows && digest.hex() == strings.TrimSpace(entry.Sha256) {
				stat.DigestMatch = true
			} else {
				addBlocker("表 %s 读回校验不匹配（manifest 行=%d 摘要=%s；读回 行=%d 摘要=%s）", table, entry.Rows, entry.Sha256, readbackRows, digest.hex())
			}
		default:
			// 登记放行的缺失列使公共投影必然小于源列，manifest digest 不可比：
			// 退化为按主键逐行对比公共投影（行数仍必须与 manifest 一致）。
			data := dataset[table]
			if readbackRows != entry.Rows || !bdProjectedRowsEqual(targetTable.PrimaryKey, stat.PublicColumns, data.Rows, readbackData) {
				allMatch = false
				addBlocker("表 %s 公共投影逐行读回校验不匹配（manifest 行=%d 读回 行=%d）%s", table, entry.Rows, readbackRows, func() string {
					if bdLastProjectionMismatch != "" {
						return "；首个差异：" + bdLastProjectionMismatch
					}
					return ""
				}())
			} else {
				stat.DigestMatch = true
			}
		}
		if !stat.DigestMatch {
			allMatch = false
		}
	}
	report.StructuralEmptyVerified = structuralVerified

	if len(report.Blockers) > 0 {
		return report, nil
	}
	if allMatch && structuralVerified {
		report.Verification = "match"
	} else {
		report.Verification = "mismatch"
		return report, nil
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("提交导入事务: %w", err)
	}
	report.Committed = true
	for _, table := range contracts.BusinessDatasetTables {
		stats[table].Status = "match"
	}
	return report, nil
}

func bdCanonicalIndex(table string) int {
	for index, name := range contracts.BusinessDatasetTables {
		if name == table {
			return index
		}
	}
	return len(contracts.BusinessDatasetTables)
}

// bdTopologicalOrder orders the whitelist tables so every referenced table
// precedes its referencing table. Ties fall back to canonical order. The
// second return reports a cycle.
func bdTopologicalOrder(edges [][2]int) ([]string, bool) {
	total := len(contracts.BusinessDatasetTables)
	indegree := make([]int, total)
	children := make([][]int, total)
	for _, edge := range edges {
		parent, child := edge[0], edge[1]
		if parent < 0 || parent >= total || child < 0 || child >= total {
			continue
		}
		children[parent] = append(children[parent], child)
		indegree[child]++
	}
	ready := &bdIndexHeap{}
	for index, degree := range indegree {
		if degree == 0 {
			heap.Push(ready, index)
		}
	}
	order := make([]string, 0, total)
	for ready.Len() > 0 {
		parent := heap.Pop(ready).(int)
		order = append(order, contracts.BusinessDatasetTables[parent])
		for _, child := range children[parent] {
			indegree[child]--
			if indegree[child] == 0 {
				heap.Push(ready, child)
			}
		}
	}
	if len(order) != total {
		return order, true
	}
	return order, false
}

type bdIndexHeap []int

func (h *bdIndexHeap) Len() int           { return len(*h) }
func (h *bdIndexHeap) Less(i, j int) bool { return (*h)[i] < (*h)[j] }
func (h *bdIndexHeap) Swap(i, j int)      { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *bdIndexHeap) Push(value any)     { *h = append(*h, value.(int)) }
func (h *bdIndexHeap) Pop() any {
	old := *h
	value := old[len(old)-1]
	*h = old[:len(old)-1]
	return value
}

// bdOrderRowsParentFirst returns the rows of one self-referencing table so
// that every referenced parent row precedes its referencing child row. A
// cycle among rows is a blocker.
func bdOrderRowsParentFirst(table string, rows []map[string]any, selfFKs []bdForeignKey) ([]map[string]any, []string) {
	count := len(rows)
	indegree := make([]int, count)
	children := make([][]int, count)
	for _, fk := range selfFKs {
		parentIndex := map[string][]int{}
		for index, row := range rows {
			parentIndex[bdValueKeys(fkRowValues(row, fk.ToColumns))] = append(parentIndex[bdValueKeys(fkRowValues(row, fk.ToColumns))], index)
		}
		for index, row := range rows {
			values := fkRowValues(row, fk.FromColumns)
			root := false
			for _, value := range values {
				if value == nil {
					root = true
					break
				}
			}
			if root {
				continue
			}
			for _, parent := range parentIndex[bdValueKeys(values)] {
				children[parent] = append(children[parent], index)
				indegree[index]++
			}
		}
	}
	ready := &bdIndexHeap{}
	for index, degree := range indegree {
		if degree == 0 {
			heap.Push(ready, index)
		}
	}
	ordered := make([]map[string]any, 0, count)
	for ready.Len() > 0 {
		parent := heap.Pop(ready).(int)
		ordered = append(ordered, rows[parent])
		for _, child := range children[parent] {
			indegree[child]--
			if indegree[child] == 0 {
				heap.Push(ready, child)
			}
		}
	}
	if len(ordered) != count {
		return nil, []string{fmt.Sprintf("表 %s 自引用行存在环，无法确定父先于子的插入顺序", table)}
	}
	return ordered, nil
}

func fkRowValues(row map[string]any, columns []string) []any {
	values := make([]any, len(columns))
	for i, column := range columns {
		values[i] = row[column]
	}
	return values
}

// bdDigest accumulates the canonical JSONL digest of one table: SHA-256 over
// the concatenation of each row's canonical JSON line plus a newline, i.e.
// exactly the bytes of the exported data file.
type bdDigest struct {
	hash sha256digest
}

// sha256digest is the minimal hash interface used for table digests.
type sha256digest = interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
	Reset()
	Size() int
	BlockSize() int
}

func newBDDigest() *bdDigest {
	return &bdDigest{hash: sha256.New()}
}

func (d *bdDigest) writeRow(row map[string]any) error {
	line, err := bdCanonicalRowJSON(row)
	if err != nil {
		return err
	}
	// sha256.Write 按标准库契约不会返回错误。
	d.hash.Write(line)
	d.hash.Write([]byte("\n"))
	return nil
}

func (d *bdDigest) hex() string {
	return hex.EncodeToString(d.hash.Sum(nil))
}

// bdProjectedValueKey renders one value as a type-agnostic comparison key.
// JSONL 解析使用 UseNumber：源侧数字是 json.Number，读回侧是 int64/float64，
// 直接 bdValueKey 会把相等的数值误判为不同（"n:5" vs "j:5"）。此处统一为
// 数值语义键：整数值 "i:<n>"、非整数值 "f:<最短表示>"，其余走 bdValueKey。
func bdProjectedValueKey(value any) string {
	// 所有数值统一走 float64 最短表示：源侧 json.Number("10") 与读回侧
	// float64(10)/int64(10) 必须得到同一键。两侧对同一行同列做同一转换，
	// bigint 超出 2^53 的精度损失在两侧对称，不影响相等性判断。
	toFloat := func() (float64, bool) {
		switch typed := value.(type) {
		case json.Number:
			if f, err := typed.Float64(); err == nil {
				return f, true
			}
		case int:
			return float64(typed), true
		case int32:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case float64:
			return typed, true
		}
		return 0, false
	}
	if f, ok := toFloat(); ok {
		return "f:" + strconv.FormatFloat(f, 'g', -1, 64)
	}
	return bdValueKey(value)
}

// bdProjectedRowsEqual compares the allowed-missing projection: every source
// row and every readback row must pair one-to-one by primary key with all
// public columns equal. Ordering plays no role, so this stays correct across
// integer primary key collation differences between real databases.
func bdProjectedRowsEqual(primaryKeys []string, public []string, source, readback []map[string]any) bool {
	if len(primaryKeys) == 0 {
		return false
	}
	for _, key := range primaryKeys {
		found := false
		for _, column := range public {
			if column == key {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	projectedKeys := func(row map[string]any, columns []string) string {
		values := make([]string, 0, len(columns))
		for _, column := range columns {
			values = append(values, bdProjectedValueKey(row[column]))
		}
		return strings.Join(values, "")
	}
	sourceByKey := map[string]map[string]any{}
	for _, row := range source {
		key := projectedKeys(row, primaryKeys)
		if _, duplicate := sourceByKey[key]; duplicate {
			return false
		}
		sourceByKey[key] = row
	}
	if len(readback) != len(sourceByKey) {
		return false
	}
	for _, row := range readback {
		key := projectedKeys(row, primaryKeys)
		sourceRow, ok := sourceByKey[key]
		if !ok {
			return false
		}
		for _, column := range public {
			if bdProjectedValueKey(sourceRow[column]) != bdProjectedValueKey(row[column]) {
				bdLastProjectionMismatch = fmt.Sprintf("行主键=%q 列=%q 源=%q(%T) 读回=%q(%T)",
					key, column, sourceRow[column], sourceRow[column], row[column], row[column])
				return false
			}
		}
	}
	return true
}

// bdLastProjectionMismatch 记录最近一次投影对比失败的首个差异（仅诊断用）。
var bdLastProjectionMismatch string
