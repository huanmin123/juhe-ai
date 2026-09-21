package businessdataset

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// ExportOptions binds one export invocation. Now zero selects the current UTC
// time; Producer empty selects DefaultExportProducer; TargetIdentity empty
// selects DefaultTargetIdentity.
type ExportOptions struct {
	Producer       string
	TargetIdentity string
	Now            time.Time
}

// ExportTableStat records the per-table export evidence.
type ExportTableStat struct {
	Name           string `json:"name"`
	File           string `json:"file"`
	Rows           int64  `json:"rows"`
	Sha256         string `json:"sha256"`
	StructuralOnly bool   `json:"structuralOnly"`
}

// ExportReport is the JSON report of one ExportBusinessDataset call. Ready
// requires the read-only transaction proof, all 40 whitelist tables processed,
// zero blockers and a written manifest.
type ExportReport struct {
	Mode                string            `json:"mode"`
	ReadOnlyTransaction bool              `json:"readOnlyTransaction"`
	SourceIdentity      string            `json:"sourceIdentity"`
	TargetIdentity      string            `json:"targetIdentity"`
	OutDir              string            `json:"outDir"`
	ManifestFile        string            `json:"manifestFile"`
	ManifestHash        string            `json:"manifestHash"`
	CapturedAt          string            `json:"capturedAt"`
	Tables              []ExportTableStat `json:"tables"`
	Blockers            []string          `json:"blockers"`
}

// Ready reports whether the export produced a complete, blocker-free dataset
// with a written manifest. It is the exit-3 gate of the CLI runner.
func (r ExportReport) Ready() bool {
	return len(r.Blockers) == 0 && r.ReadOnlyTransaction && len(r.Tables) == len(contracts.BusinessDatasetTables) && r.ManifestHash != ""
}

// ExportBusinessDataset streams the fixed 40-table business whitelist out of
// the source database inside an explicit READ ONLY transaction. Each of the
// 37 data tables becomes one canonical JSONL file (<table>.jsonl); the 3
// structural-only tables are asserted empty and produce no file. The source
// database is never written. The manifest is written only when the export
// finished without blockers.
func ExportBusinessDataset(ctx context.Context, db *sql.DB, outDir string, opts ExportOptions) (ExportReport, error) {
	report := ExportReport{Mode: "export", OutDir: outDir}
	if db == nil {
		return report, errors.New("business dataset 导出数据库未初始化")
	}
	if strings.TrimSpace(outDir) == "" {
		return report, errors.New("business dataset 导出目录为空")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return report, fmt.Errorf("创建导出目录: %w", err)
	}
	addBlocker := func(format string, args ...any) {
		report.Blockers = append(report.Blockers, fmt.Sprintf(format, args...))
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("开启只读导出事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var readOnly string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		return report, fmt.Errorf("校验导出事务只读模式: %w", err)
	}
	report.ReadOnlyTransaction = strings.EqualFold(strings.TrimSpace(readOnly), "on")
	if !report.ReadOnlyTransaction {
		return report, fmt.Errorf("导出事务不是 READ ONLY（transaction_read_only=%s），拒绝导出", strings.TrimSpace(readOnly))
	}
	var sourceIdentity string
	if err := tx.QueryRowContext(ctx, "SELECT current_database()").Scan(&sourceIdentity); err != nil {
		return report, fmt.Errorf("读取源库标识: %w", err)
	}
	report.SourceIdentity = strings.TrimSpace(sourceIdentity)
	report.TargetIdentity = strings.TrimSpace(opts.TargetIdentity)
	if report.TargetIdentity == "" {
		report.TargetIdentity = DefaultTargetIdentity
	}
	producer := strings.TrimSpace(opts.Producer)
	if producer == "" {
		producer = DefaultExportProducer
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	capturedAt := now.UTC().Format(time.RFC3339)
	report.CapturedAt = capturedAt

	for _, table := range contracts.BusinessDatasetTables {
		stat, tableErr := bdExportTable(ctx, tx, table, outDir)
		if tableErr != nil {
			return report, tableErr
		}
		report.Tables = append(report.Tables, stat)
		if contracts.IsBusinessDatasetStructuralOnlyTable(table) {
			if stat.Rows != 0 {
				addBlocker("结构-only 表 %s 在源库非空（%d 行），拒绝导出", table, stat.Rows)
			}
		}
	}

	if len(report.Blockers) > 0 {
		return report, nil
	}
	manifest := contracts.BusinessDatasetManifest{
		FormatVersion:  contracts.BusinessDatasetManifestFormatVersion,
		Scope:          contracts.BusinessDatasetManifestScope,
		Producer:       producer,
		SourceIdentity: report.SourceIdentity,
		TargetIdentity: report.TargetIdentity,
		CapturedAt:     capturedAt,
		Tables:         make([]contracts.BusinessDatasetTableEntry, 0, len(report.Tables)),
	}
	for _, stat := range report.Tables {
		manifest.Tables = append(manifest.Tables, contracts.BusinessDatasetTableEntry{Name: stat.Name, File: stat.File, Rows: stat.Rows, Sha256: stat.Sha256})
	}
	hash, err := contracts.ComputeBusinessDatasetManifestHash(manifest)
	if err != nil {
		return report, fmt.Errorf("计算 manifest 哈希: %w", err)
	}
	manifest.ManifestHash = hash
	manifestPath, err := bdWriteManifestFile(outDir, manifest)
	if err != nil {
		return report, err
	}
	report.ManifestFile = manifestPath
	report.ManifestHash = hash
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("提交只读导出事务: %w", err)
	}
	return report, nil
}

// bdExportTable streams one whitelist table into its JSONL file (data tables)
// or counts its rows (structural-only tables), returning the canonical
// content digest and row count.
func bdExportTable(ctx context.Context, tx *sql.Tx, table string, outDir string) (ExportTableStat, error) {
	stat := ExportTableStat{Name: table, File: table + ".jsonl", StructuralOnly: contracts.IsBusinessDatasetStructuralOnlyTable(table)}
	columns, err := bdLoadColumns(ctx, tx, table)
	if err != nil {
		return stat, err
	}
	if len(columns) == 0 {
		return stat, fmt.Errorf("源库缺少 whitelist 表 %s.%s", SchemaName, table)
	}
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}
	primaryKey, err := bdLoadPrimaryKey(ctx, tx, table)
	if err != nil {
		return stat, err
	}
	order := primaryKey
	if len(order) == 0 {
		// No primary key: order by every column, fail-closed when even that
		// is impossible.
		order = names
	}
	query, err := bdSelectAll(table, names, order)
	if err != nil {
		return stat, err
	}

	var file *os.File
	var writer *bufio.Writer
	var digest = sha256.New()
	var rowCount int64
	if !stat.StructuralOnly {
		file, err = os.Create(filepath.Join(outDir, stat.File))
		if err != nil {
			return stat, fmt.Errorf("创建表 %s 数据文件: %w", table, err)
		}
		defer file.Close()
		writer = bufio.NewWriter(file)
		defer func() {
			if writer != nil {
				_ = writer.Flush()
			}
		}()
	}
	err = bdScanRows(ctx, tx, "表 "+table, query, names, func(row map[string]any) error {
		line, err := bdCanonicalRowJSON(row)
		if err != nil {
			return err
		}
		rowCount++
		// sha256.Write 按标准库契约不会返回错误。
		_, _ = digest.Write(line)
		_, _ = digest.Write([]byte("\n"))
		if writer != nil {
			if _, err := writer.Write(line); err != nil {
				return err
			}
			if err := writer.WriteByte('\n'); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return stat, err
	}
	if writer != nil {
		if err := writer.Flush(); err != nil {
			return stat, fmt.Errorf("写入表 %s 数据文件: %w", table, err)
		}
		writer = nil
	}
	stat.Rows = rowCount
	stat.Sha256 = hex.EncodeToString(digest.Sum(nil))
	return stat, nil
}
