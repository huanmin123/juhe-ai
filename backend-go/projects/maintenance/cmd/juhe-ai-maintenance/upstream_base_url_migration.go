// 上游 Base URL 版本根（v2）存量迁移命令薄壳：
//   - --migrate-upstream-base-url-version-root --dry-run --manifest-out <path>：
//     只读扫描 juhe_business（account_connections.base_url 明文列与
//     accounts.credentials_encrypted 密文 JSON 的 base_url 键），逐行分类并写
//     出可复核 manifest（dry-run 唯一写入）；人工复核桶只记录不改数据；
//   - --migrate-upstream-base-url-version-root（不带 --dry-run）：要求
//     --node-stopped --go-stopped --backup-confirmed，在单个业务库事务内按
//     「旧值相等」条件写入并写出执行后 manifest；幂等可重跑；
//   - --verify-upstream-base-url-version-root --upstream-base-url-check=
//     preflight|postflight：只读核验（preflight=迁移前分类报告；postflight=
//     完成态核验，残留 rewrite/fill 拒绝就绪）。
//
// 存储目标为 --dsn 单形态；开库复用 openBusinessPostgres（能力域命令同一
// maintenance 连接契约）。
//
// 退出码契约：0 成功（dry-run 与 preflight 报告输出、apply 成功；复核桶非空
// 不影响退出码——复核桶是设计内「不改数据、人工裁决」产物）；1 运行失败（库
// 操作错误）；2 用法错误（门禁/旗标缺失或矛盾）；3 未就绪（apply 条件更新未
// 命中，或 postflight 仍有 rewrite/fill 残留；同命令重跑续跑）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upstreambaseurlmigration"
)

// upstreamBaseURLMigrationResult 返回 migrate 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。门禁与 manifest-out 检查先于一切
// 开库动作。
func upstreamBaseURLMigrationResult(dryRun bool, dsn, manifestOut, secret string, nodeStopped, goStopped, backupConfirmed bool) int {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--migrate-upstream-base-url-version-root 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	outPath := strings.TrimSpace(manifestOut)
	if outPath == "" {
		fmt.Fprintln(os.Stderr, "--migrate-upstream-base-url-version-root 需要 --manifest-out 指向 manifest 输出文件（dry-run 的唯一写入；apply 后同样落盘供复核）")
		return 2
	}
	if !dryRun && (!nodeStopped || !goStopped || !backupConfirmed) {
		fmt.Fprintln(os.Stderr, "--migrate-upstream-base-url-version-root 正式执行要求 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	db, usageCode := openBusinessPostgres(parsedDSN)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	options := upstreambaseurlmigration.Options{Secret: secret}
	var manifest upstreambaseurlmigration.Manifest
	var err error
	if dryRun {
		manifest, err = upstreambaseurlmigration.Plan(context.Background(), db, options)
	} else {
		manifest, err = upstreambaseurlmigration.Apply(context.Background(), db, options)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "上游 Base URL 版本根迁移失败: %v\n", err)
		return 1
	}
	if err := writeUpstreamBaseURLManifest(outPath, manifest); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	fmt.Printf("%s manifest 已写入 %s (connections: total=%d rewrite=%d fill=%d review=%d; credentials: total=%d rewrite=%d fill=%d review=%d)\n",
		mode, outPath,
		manifest.Connections.Total, manifest.Connections.Rewritten, manifest.Connections.Filled, manifest.Connections.Review,
		manifest.Credentials.Total, manifest.Credentials.Rewritten, manifest.Credentials.Filled, manifest.Credentials.Review)
	for _, entry := range manifest.Review {
		fmt.Fprintf(os.Stderr, "review: kind=%s id=%s reason=%s value=%s\n", entry.Kind, entry.ID, entry.Reason, entry.Value)
	}
	if len(manifest.Failures) > 0 {
		for _, failure := range manifest.Failures {
			fmt.Fprintf(os.Stderr, "failure: kind=%s id=%s reason=%s detail=%s\n", failure.Kind, failure.ID, failure.Reason, failure.Detail)
		}
		fmt.Fprintf(os.Stderr, "上游 Base URL 版本根迁移未就绪：%d 项失败（条件更新未命中即停写窗口被改写；修复后同命令重跑，已提交行保持幂等）。\n", len(manifest.Failures))
		return 3
	}
	return 0
}

// upstreamBaseURLVerifyResult 返回 verify 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。只读：全程零写入。
func upstreamBaseURLVerifyResult(dsn, check string) int {
	normalizedCheck := strings.ToLower(strings.TrimSpace(check))
	if normalizedCheck != "preflight" && normalizedCheck != "postflight" {
		fmt.Fprintln(os.Stderr, "--verify-upstream-base-url-version-root 需要 --upstream-base-url-check=preflight|postflight（preflight=迁移前只读分类报告；postflight=完成态核验；不隐式切换核验语义）")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	if normalizedCheck == "preflight" {
		manifest, err := upstreambaseurlmigration.Plan(context.Background(), db, upstreambaseurlmigration.Options{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "上游 Base URL 版本根预检失败: %v\n", err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
			fmt.Fprintf(os.Stderr, "encode upstream base URL preflight manifest: %v\n", err)
			return 1
		}
		if manifest.Connections.Review+manifest.Credentials.Review > 0 {
			fmt.Fprintf(os.Stderr, "上游 Base URL 版本根预检提示：%d 行进人工复核桶（不改数据，见 review 清单；非合规值需人工裁决后处理）。\n",
				manifest.Connections.Review+manifest.Credentials.Review)
		}
		return 0
	}
	report, err := upstreambaseurlmigration.Verify(context.Background(), db, upstreambaseurlmigration.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "上游 Base URL 版本根核验失败: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode upstream base URL postflight report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "上游 Base URL 版本根核验未通过：残留待迁移 connections=%d credentials=%d（先运行 --migrate-upstream-base-url-version-root --dry-run --manifest-out <path> 复核后再正式执行）。\n",
			report.PendingConnections, report.PendingCredentials)
		return 3
	}
	return 0
}

// writeUpstreamBaseURLManifest 把 manifest 以稳定缩进 JSON 落盘（逐行分类结
// 果含复核桶与改写明细；--manifest-out 是 dry-run 唯一写入）。
func writeUpstreamBaseURLManifest(outPath string, manifest upstreambaseurlmigration.Manifest) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("encode upstream base URL manifest: %w", err)
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写 manifest %s 失败: %w", outPath, err)
	}
	return nil
}
