package businesshandoff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件补齐 J3b 证据链中既有测试未触达的入口：owner/epoch 定向校验、
// backfill 证据语义（目标 digest 可缺省但清单必查）、文件校验错误分支。

func TestWMValidateJ3bCutoverEvidenceForOwner(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	report := ValidateJ3bCutoverEvidenceForOwner(evidence, contracts.J3bGatewayCutoverOwner, evidence.OwnerEpoch, now)
	if !report.Ready {
		t.Fatalf("匹配 owner/epoch 的证据必须就绪: %+v", report.Errors)
	}
	wrongOwner := ValidateJ3bCutoverEvidenceForOwner(evidence, "someone-else", evidence.OwnerEpoch, now)
	if wrongOwner.Ready {
		t.Fatalf("owner 不匹配必须失败闭环: %+v", wrongOwner.Errors)
	}
	wrongEpoch := ValidateJ3bCutoverEvidenceForOwner(evidence, contracts.J3bGatewayCutoverOwner, "other-epoch", now)
	if wrongEpoch.Ready {
		t.Fatalf("epoch 不匹配必须失败闭环: %+v", wrongEpoch.Errors)
	}
}

func TestWMValidateJ3bBackfillEvidenceAllowsMissingTargetDigest(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	// backfill 前置证据允许缺失目标 digest（尚无回读目标），但清单必须完整。
	evidence.TargetDigest = ""
	report := ValidateJ3bBackfillEvidence(evidence, now)
	if !report.Ready {
		t.Fatalf("backfill 证据在目标 digest 缺省时必须就绪: %+v", report.Errors)
	}
	// 契约在证据层只校验 digest 形态（提供时必须是 SHA-256），相等性由
	// readback manifest 的逐表 digest 对比保证，因此两侧不一致仍就绪。
	evidence.TargetDigest = strings.Repeat("b", 64)
	mismatch := ValidateJ3bBackfillEvidence(evidence, now)
	if !mismatch.Ready {
		t.Fatalf("backfill 证据层不做 digest 相等性判定: %+v", mismatch.Errors)
	}
	// 但 digest 形态非法必须失败闭环。
	evidence.TargetDigest = "not-a-digest"
	malformed := ValidateJ3bBackfillEvidence(evidence, now)
	if malformed.Ready || !containsWMErrors(malformed.Errors, "must be a SHA-256 digest") {
		t.Fatalf("digest 形态非法必须失败闭环: %+v", malformed.Errors)
	}
}

func TestWMVerifyJ3bBackfillEvidenceFileFlow(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	// writeValidJ3bReadbackManifest 产生的清单同时满足 backfill 语义。
	evidence.TargetDigest = ""
	data, err := marshalWMEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wm-backfill-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bBackfillEvidence(path, now)
	if err != nil {
		t.Fatalf("VerifyJ3bBackfillEvidence: %v", err)
	}
	if !report.Ready {
		t.Fatalf("文件证据必须就绪: %+v", report.Errors)
	}
	if _, err := VerifyJ3bBackfillEvidence(filepath.Join(t.TempDir(), "missing.json"), now); err == nil {
		t.Fatal("缺失文件必须返回输入错误")
	}
	if _, err := VerifyJ3bCutoverEvidence("", now); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("空路径必须拒绝: %v", err)
	}
}

func TestWMVerifyJ3bBackupArtifactBranches(t *testing.T) {
	t.Run("unreadable path", func(t *testing.T) {
		err := verifyJ3bBackupArtifact(J3bBackupArtifact{Path: filepath.Join(t.TempDir(), "nope.bin"), Hash: "00"})
		if err == nil || !strings.Contains(err.Error(), "path is unreadable") {
			t.Fatalf("缺失备份必须报错: %v", err)
		}
	})
	t.Run("non regular file", func(t *testing.T) {
		dir := t.TempDir()
		err := verifyJ3bBackupArtifact(J3bBackupArtifact{Path: dir, Hash: "00"})
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("目录备份必须报错: %v", err)
		}
	})
	t.Run("hash mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "backup.bin")
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := verifyJ3bBackupArtifact(J3bBackupArtifact{Path: path, Hash: strings.Repeat("0", 64)})
		if err == nil || !strings.Contains(err.Error(), "hash does not match") {
			t.Fatalf("哈希不匹配必须报错: %v", err)
		}
	})
}

func TestWMVerifyJ3bReadbackManifestRejectsBadArtifacts(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	manifestPath, manifestHash := writeValidJ3bReadbackManifest(t, now)
	base := evidence
	base.ReadbackManifest = J3bReadbackManifestReference{Path: manifestPath, Hash: manifestHash, FormatVersion: contracts.J3bReadbackManifestFormatVersion, Scope: contracts.J3bReadbackManifestScope, SourceSnapshotIdentity: "snapshot-1", SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite"}

	t.Run("unreadable manifest", func(t *testing.T) {
		mutated := base
		mutated.ReadbackManifest.Path = filepath.Join(t.TempDir(), "missing.json")
		report := ValidateJ3bCutoverEvidence(mutated, now)
		if report.Ready || !containsWMErrors(report.Errors, "read manifest") {
			t.Fatalf("清单不可读必须失败闭环: %+v", report.Errors)
		}
	})
	t.Run("file hash mismatch", func(t *testing.T) {
		mutated := base
		mutated.ReadbackManifest.Hash = strings.Repeat("0", 64)
		report := ValidateJ3bCutoverEvidence(mutated, now)
		if report.Ready || !containsWMErrors(report.Errors, "SHA-256 does not match") {
			t.Fatalf("清单文件哈希不匹配必须失败闭环: %+v", report.Errors)
		}
	})
	t.Run("identity mismatch", func(t *testing.T) {
		mutated := base
		mutated.ReadbackManifest.Scope = "other-scope"
		report := ValidateJ3bCutoverEvidence(mutated, now)
		if report.Ready || !containsWMErrors(report.Errors, "identity does not match") {
			t.Fatalf("清单身份不匹配必须失败闭环: %+v", report.Errors)
		}
	})
	t.Run("expired manifest", func(t *testing.T) {
		// 以远晚于清单 verifiedAt 的时钟校验：新鲜度必须失败闭环。
		report := ValidateJ3bCutoverEvidence(base, now.Add(24*time.Hour))
		if report.Ready || !containsWMErrors(report.Errors, "expired") {
			t.Fatalf("过期清单必须失败闭环: %+v", report.Errors)
		}
	})
}

func containsWMErrors(errors []string, fragment string) bool {
	for _, message := range errors {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func marshalWMEvidence(evidence J3bCutoverEvidence) ([]byte, error) {
	return json.Marshal(evidence)
}

func TestWMCanonicalPathNormalizesInputs(t *testing.T) {
	if got := canonicalPath("  "); got != "" {
		t.Fatalf("空白路径必须归一为空: %q", got)
	}
	relative := canonicalPath("some/relative/../path")
	if !filepath.IsAbs(relative) || strings.Contains(relative, "..") {
		t.Fatalf("相对路径必须转为绝对路径并清理: %q", relative)
	}
	if got := canonicalPath("/tmp/x"); filepath.IsAbs(got) == false && !strings.HasSuffix(got, "x") {
		t.Fatalf("绝对路径应保持: %q", got)
	}
}
