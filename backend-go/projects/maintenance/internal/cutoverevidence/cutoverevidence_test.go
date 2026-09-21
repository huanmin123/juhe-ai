package cutoverevidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
)

// 本文件用临时目录真实文件走全流程：构造合法 v2 readback manifest → 组装 →
// businesshandoff.VerifyJ3bCutoverEvidence 全链路自验通过；篡改备份哈希
//（evidence 覆写备份文件这一真实可达的错用）、过期清单、缺表清单 → 自验
// 失败且已写文件被删除；必填项缺失 → InputError（CLI exit 2）。verify seam
// 只用于注入真实输入无法构造的校验 I/O 错误分支。不 exec 子进程。

var ceRequiredTables = []string{
	"account_quality_health_hourly",
	"model_check_items",
	"model_check_observations",
	"model_check_runs",
	"model_account_trust_results",
	"model_token_intercept_baseline_versions",
	"model_trust_aggregation_state",
	"model_trust_latest_dirty_accounts",
	"model_trust_observation_receipts",
}

var ceFixedNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// ceBuildManifestBytes 构造自洽的 v2 readback manifest JSON；mutate 在计算
// ManifestHash 之前调用，用于构造"自洽但不被证据接受"的变体（缺表、过期）。
func ceBuildManifestBytes(t *testing.T, verifiedAt time.Time, mutate func(*contracts.J3bReadbackManifest)) []byte {
	t.Helper()
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               "cutoverevidence-test",
		SourceSnapshotIdentity: "ce-snapshot-1",
		SourceSchema:           "juhe_dataset+juhe_stats",
		TargetSchema:           "juhe_j3b",
		ProjectionComplete:     true,
		VerifiedAt:             verifiedAt.UTC().Format(time.RFC3339),
	}
	for _, name := range ceRequiredTables {
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{Name: name, SourceRows: 1, TargetRows: 1, SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64)})
	}
	if mutate != nil {
		mutate(&manifest)
	}
	hash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ceWriteInputs 构造真实备份文件与合法清单文件，返回两者路径与备份内容。
func ceWriteInputs(t *testing.T, manifestData []byte) (dir, backupPath, manifestPath string) {
	t.Helper()
	dir = t.TempDir()
	backupPath = filepath.Join(dir, "migration-backup.tar")
	if err := os.WriteFile(backupPath, []byte("immutable backup payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath = filepath.Join(dir, "readback-manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, backupPath, manifestPath
}

func ceOptions(backupPath, manifestPath string) Options {
	return Options{
		OldOwner:             DefaultOldOwner,
		OwnerEpoch:           "j3b-cutover-20260921-001",
		RollbackReplayCursor: "batch-20260921-000042",
		MaxAgeSeconds:        DefaultMaxAgeSeconds,
		BackupArtifactPath:   backupPath,
		ReadbackManifestPath: manifestPath,
	}
}

func TestAssembleCutoverEvidenceHappyPath(t *testing.T) {
	_, backupPath, manifestPath := ceWriteInputs(t, ceBuildManifestBytes(t, ceFixedNow, nil))
	out := filepath.Join(t.TempDir(), "evidence.json")
	report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), out, ceFixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || len(report.Errors) != 0 {
		t.Fatalf("合法输入必须就绪: %+v", report)
	}
	if report.NewOwner != "go-gateway" || report.OldOwner != DefaultOldOwner || report.OwnerEpoch != "j3b-cutover-20260921-001" {
		t.Fatalf("报告 owner 字段错误: %+v", report)
	}
	if report.FreshnessCapturedAt != ceFixedNow.UTC().Format(time.RFC3339) {
		t.Fatalf("capturedAt 必须是注入时钟: %q", report.FreshnessCapturedAt)
	}
	if report.BackupSha256 == "" {
		t.Fatal("报告必须记录备份哈希")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("自验通过后文件必须保留: %v", err)
	}
	// 文件级断言：显式零值与空 digest 直接体现在 JSON 中（缺字段会被网关
	// 契约判为缺失而非零值）。
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"inFlight": 0`, `"blockedFindings": 0`, `"drainCompleted": true`, `"activePathZero": true`, `"newOwner": "go-gateway"`, `"sourceDigest": ""`, `"targetDigest": ""`} {
		if !strings.Contains(string(data), fragment) {
			t.Fatalf("证据 JSON 缺少 %s:\n%s", fragment, data)
		}
	}
	// 与 gateway 相同的契约链对写出的文件复验（真实文件、非 seam）。
	verifyReport, err := businesshandoff.VerifyJ3bCutoverEvidence(out, ceFixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyReport.Ready {
		t.Fatalf("网关契约链必须接受组装产物: %+v", verifyReport.Errors)
	}
	// 解码回读：引用身份字段与清单文件一致。
	evidence, err := contracts.DecodeJ3bCutoverEvidence(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contracts.DecodeJ3bReadbackManifest(ceBuildManifestBytes(t, ceFixedNow, nil))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ReadbackManifest.SourceSnapshotIdentity != manifest.SourceSnapshotIdentity || evidence.ReadbackManifest.SourceSchema != manifest.SourceSchema || evidence.ReadbackManifest.TargetSchema != manifest.TargetSchema || evidence.ReadbackManifest.FormatVersion != manifest.FormatVersion || evidence.ReadbackManifest.Scope != manifest.Scope {
		t.Fatalf("引用身份字段必须取自清单文件: %+v", evidence.ReadbackManifest)
	}
	if evidence.ReadbackManifest.Path != manifestPath {
		t.Fatalf("引用路径错误: %+v", evidence.ReadbackManifest)
	}
}

func TestAssembleCutoverEvidenceInputErrors(t *testing.T) {
	_, backupPath, manifestPath := ceWriteInputs(t, ceBuildManifestBytes(t, ceFixedNow, nil))
	t.Run("missing old owner", func(t *testing.T) {
		opts := ceOptions(backupPath, manifestPath)
		opts.OldOwner = "   "
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "old owner")
	})
	t.Run("missing owner epoch", func(t *testing.T) {
		opts := ceOptions(backupPath, manifestPath)
		opts.OwnerEpoch = ""
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "owner epoch")
	})
	t.Run("missing replay cursor", func(t *testing.T) {
		opts := ceOptions(backupPath, manifestPath)
		opts.RollbackReplayCursor = ""
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "replay cursor")
	})
	t.Run("non positive max age", func(t *testing.T) {
		for _, maxAge := range []int64{0, -1} {
			opts := ceOptions(backupPath, manifestPath)
			opts.MaxAgeSeconds = maxAge
			report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
			assertInputError(t, report, err, "max age")
		}
	})
	t.Run("missing backup artifact path", func(t *testing.T) {
		opts := ceOptions("", manifestPath)
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "backup artifact path")
	})
	t.Run("missing readback manifest path", func(t *testing.T) {
		opts := ceOptions(backupPath, "")
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "readback manifest path")
	})
	t.Run("missing output path", func(t *testing.T) {
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), "   ", ceFixedNow)
		assertInputError(t, report, err, "output path")
	})
	t.Run("zero clock", func(t *testing.T) {
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), filepath.Join(t.TempDir(), "evidence.json"), time.Time{})
		assertInputError(t, report, err, "clock")
	})
	t.Run("backup artifact missing", func(t *testing.T) {
		opts := ceOptions(filepath.Join(t.TempDir(), "nope.tar"), manifestPath)
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "read backup artifact")
	})
	t.Run("backup artifact is a directory", func(t *testing.T) {
		opts := ceOptions(t.TempDir(), manifestPath)
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "regular file")
	})
	t.Run("readback manifest missing", func(t *testing.T) {
		opts := ceOptions(backupPath, filepath.Join(t.TempDir(), "nope.json"))
		report, err := AssembleCutoverEvidence(opts, filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "read readback manifest")
	})
	t.Run("readback manifest malformed", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, bad), filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "decode readback manifest")
	})
	t.Run("readback manifest unknown field", func(t *testing.T) {
		unknown := filepath.Join(t.TempDir(), "unknown.json")
		if err := os.WriteFile(unknown, []byte(`{"formatVersion":"j3b-readback-manifest/v2","surprise":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, unknown), filepath.Join(t.TempDir(), "evidence.json"), ceFixedNow)
		assertInputError(t, report, err, "decode readback manifest")
	})
}

func assertInputError(t *testing.T, report Report, err error, fragment string) {
	t.Helper()
	var inputErr *InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("必须是 InputError: %v", err)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("错误必须说明 %q: %v", fragment, err)
	}
	if report.Ready {
		t.Fatalf("输入错误不得就绪: %+v", report)
	}
	if report.EvidencePath != "" {
		if _, statErr := os.Stat(report.EvidencePath); !os.IsNotExist(statErr) {
			t.Fatalf("输入错误不得写文件: %v", statErr)
		}
	}
}

func TestAssembleCutoverEvidenceSelfVerifyNotReady(t *testing.T) {
	t.Run("expired manifest freshness", func(t *testing.T) {
		// 清单 verifiedAt 远早于组装时钟：新鲜度门必须失败闭环并删除文件。
		expired := ceBuildManifestBytes(t, ceFixedNow.Add(-91*24*time.Hour), nil)
		_, backupPath, manifestPath := ceWriteInputs(t, expired)
		out := filepath.Join(t.TempDir(), "evidence.json")
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), out, ceFixedNow)
		if err != nil {
			t.Fatalf("未就绪属于报告而非运行错误: %v", err)
		}
		assertNotReadyRemoved(t, report, out, "expired")
	})
	t.Run("manifest missing required table", func(t *testing.T) {
		incomplete := ceBuildManifestBytes(t, ceFixedNow, func(m *contracts.J3bReadbackManifest) {
			kept := m.Tables[:0]
			for _, table := range m.Tables {
				if table.Name != "model_check_runs" {
					kept = append(kept, table)
				}
			}
			m.Tables = kept
		})
		_, backupPath, manifestPath := ceWriteInputs(t, incomplete)
		out := filepath.Join(t.TempDir(), "evidence.json")
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), out, ceFixedNow)
		if err != nil {
			t.Fatalf("未就绪属于报告而非运行错误: %v", err)
		}
		assertNotReadyRemoved(t, report, out, "model_check_runs")
	})
	t.Run("output overwriting the backup artifact fails hash gate", func(t *testing.T) {
		// 真实可达的错用：把证据写到备份文件自身。组装先按备份内容算哈希，
		// 覆写后自验重读必然哈希不符 → 未就绪且文件删除。
		_, backupPath, manifestPath := ceWriteInputs(t, ceBuildManifestBytes(t, ceFixedNow, nil))
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), backupPath, ceFixedNow)
		if err != nil {
			t.Fatalf("未就绪属于报告而非运行错误: %v", err)
		}
		assertNotReadyRemoved(t, report, backupPath, "backupArtifact")
	})
}

func assertNotReadyRemoved(t *testing.T, report Report, out, fragment string) {
	t.Helper()
	if report.Ready {
		t.Fatalf("自验失败必须未就绪: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Errors, "; "), fragment) {
		t.Fatalf("错误必须说明 %q: %+v", fragment, report.Errors)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("未就绪产物必须被删除: %v", err)
	}
}

func TestAssembleCutoverEvidenceWriteFailure(t *testing.T) {
	_, backupPath, manifestPath := ceWriteInputs(t, ceBuildManifestBytes(t, ceFixedNow, nil))
	out := filepath.Join(t.TempDir(), "missing-dir", "evidence.json")
	report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), out, ceFixedNow)
	var inputErr *InputError
	if err == nil || errors.As(err, &inputErr) {
		t.Fatalf("写出失败属于运行错误而非输入错误: %v", err)
	}
	if !strings.Contains(err.Error(), "write cutover evidence") {
		t.Fatalf("错误必须说明写出失败: %v", err)
	}
	if report.Ready {
		t.Fatalf("写出失败不得就绪: %+v", report)
	}
	if report.EvidencePath != out {
		t.Fatalf("报告必须保留目标路径: %+v", report)
	}
}

func TestAssembleCutoverEvidenceVerifySeamInjected(t *testing.T) {
	_, backupPath, manifestPath := ceWriteInputs(t, ceBuildManifestBytes(t, ceFixedNow, nil))
	saved := verifyAssembled
	t.Cleanup(func() { verifyAssembled = saved })

	t.Run("verifier error removes file and returns runtime error", func(t *testing.T) {
		verifyAssembled = func(path string, now time.Time) (contracts.J3bCutoverEvidenceReport, error) {
			return contracts.J3bCutoverEvidenceReport{}, errors.New("injected verifier io failure")
		}
		target := filepath.Join(t.TempDir(), "evidence.json")
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), target, ceFixedNow)
		var inputErr *InputError
		if err == nil || errors.As(err, &inputErr) {
			t.Fatalf("校验器故障属于运行错误: %v", err)
		}
		if !strings.Contains(err.Error(), "self-verify written cutover evidence") {
			t.Fatalf("错误必须说明自验失败: %v", err)
		}
		if report.Ready {
			t.Fatalf("校验器故障不得就绪: %+v", report)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("校验器故障后文件必须删除: %v", statErr)
		}
	})
	t.Run("verifier not ready removes file and reports errors", func(t *testing.T) {
		verifyAssembled = func(path string, now time.Time) (contracts.J3bCutoverEvidenceReport, error) {
			return contracts.J3bCutoverEvidenceReport{Errors: []string{"injected not ready"}}, nil
		}
		target := filepath.Join(t.TempDir(), "evidence.json")
		report, err := AssembleCutoverEvidence(ceOptions(backupPath, manifestPath), target, ceFixedNow)
		if err != nil {
			t.Fatalf("未就绪属于报告而非运行错误: %v", err)
		}
		assertNotReadyRemoved(t, report, target, "injected not ready")
	})
}

func TestInputErrorUnwrap(t *testing.T) {
	sentinel := errors.New("sentinel")
	var inputErr *InputError = inputErrorf("wrapped: %w", sentinel)
	if !errors.Is(inputErr, sentinel) {
		t.Fatal("InputError 必须支持 Unwrap/errors.Is")
	}
	if inputErr.Error() != "wrapped: sentinel" {
		t.Fatalf("Error() 文本错误: %q", inputErr.Error())
	}
}
