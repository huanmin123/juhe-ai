package accounts

// w14l 覆盖率收尾：store 事务链的“第 n 条语句失败”扫描 + Commit/Rollback
// 边界故障注入。setup 阶段不注册故障（必然成功），faults 注册后只影响 run。
// 序列内部所有错误都被容忍（本文件只服务覆盖率，不校验业务结果）。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// w14lField 构造批量更新字段。
func w14lField(value any) BatchUpdateField { return BatchUpdateField{Enabled: true, Value: value} }

// w14lSequences 定义被扫描的事务链：setup 在故障注册前执行，run 在故障下执行。
type w14lSequence struct {
	setup func(f *w14lFixture)
	run   func(f *w14lFixture) error
}

func w14lSequences() map[string]w14lSequence {
	return map[string]w14lSequence{
		"create": {
			run: func(f *w14lFixture) error {
				_, err := f.store.Create(context.Background(), w14lCreateInput("w14l-sweep-create"), f.scope())
				return err
			},
		},
		"patch-basic": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-patch") },
			run: func(f *w14lFixture) error {
				name := "w14l-patch-2"
				status := "active"
				notes := "w14l 备注"
				priority := 3
				limit := 7
				_, err := f.store.Patch(context.Background(), f.firstID(), PatchInput{
					ExpectedConfigRevision: 1,
					Name:                   &name,
					Status:                 &status,
					Notes:                  &notes,
					Priority:               &priority,
					ConcurrencyLimit:       &limit,
					Tags:                   []string{"w14l-tag-a", "w14l-tag-b"},
					TagsPresent:            true,
				}, f.scope())
				return err
			},
		},
		"patch-models": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-models") },
			run: func(f *w14lFixture) error {
				mode := "chat_json"
				_, err := f.store.Patch(context.Background(), f.firstID(), PatchInput{
					ExpectedConfigRevision:  1,
					SupportedModels:         []string{"gpt-4o-mini", "gpt-4o-mini"},
					SupportedModelsPresent:  true,
					ModelMappings:           []ModelMapping{{SourceModel: "gpt-4o-mini", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-4o-mini", UpstreamEndpointFamily: "chat_completions"}},
					ModelMappingsPresent:    true,
					HealthCheckEndpointMode: &mode,
				}, f.scope())
				return err
			},
		},
		"patch-schedule": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-sched") },
			run: func(f *w14lFixture) error {
				_, err := f.store.Patch(context.Background(), f.firstID(), PatchInput{
					ExpectedConfigRevision:      1,
					AvailabilitySchedulePresent: true,
					AvailabilitySchedule: map[string]any{
						"enabled": true, "timezone": "UTC", "mode": "allow_windows",
						"windows": []any{map[string]any{"daysOfWeek": []any{float64(1), float64(2)}, "start": "01:00", "end": "02:00"}},
					},
				}, f.scope())
				return err
			},
		},
		"delete": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-del") },
			run: func(f *w14lFixture) error {
				_, err := f.store.Delete(context.Background(), f.firstID(), f.scope())
				return err
			},
		},
		"batch-fields": {
			setup: func(f *w14lFixture) {
				f.createViaStore("w14l-b1")
				f.createViaStore("w14l-b2")
			},
			run: func(f *w14lFixture) error {
				_, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
					Targets: []BatchUpdateTarget{
						{AccountID: f.firstID(), ConfigRevision: 1},
						{AccountID: f.secondID(), ConfigRevision: 1},
					},
					Updates: map[string]BatchUpdateField{
						"concurrencyLimit": w14lField(float64(9)),
						"priority":         w14lField(float64(3)),
						"notes":            w14lField("w14l 批量"),
						"fallbackEnabled":  w14lField(true),
						"tags":             w14lField([]any{"w14l-batch-tag"}),
					},
				}, f.scope())
				return err
			},
		},
		"batch-models": {
			setup: func(f *w14lFixture) {
				f.createViaStore("w14l-bm1")
				f.createViaStore("w14l-bm2")
			},
			run: func(f *w14lFixture) error {
				_, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
					Targets: []BatchUpdateTarget{
						{AccountID: f.firstID(), ConfigRevision: 1},
						{AccountID: f.secondID(), ConfigRevision: 1},
					},
					Updates: map[string]BatchUpdateField{
						"supportedModels": w14lField([]any{"gpt-4o-mini"}),
						"modelMappings":   w14lField([]any{map[string]any{"sourceModel": "gpt-4o-mini", "sourceEndpointFamily": "chat_completions", "upstreamModel": "gpt-4o-mini", "upstreamEndpointFamily": "chat_completions"}}),
					},
				}, f.scope())
				return err
			},
		},
		"import-execute": {
			run: func(f *w14lFixture) error {
				_, err := f.store.ExecuteImport(context.Background(), w13aRawDoc(
					[]any{map[string]any{"name": "w14l-imp", "providerCode": "openai",
						"providerProtocolProfileId": openAICompatibleProfileID,
						"type": "api_key", "status": "active", "groupName": "w14l-imp-group",
						"proxyRef": "p1",
						"credentials": map[string]any{"api_key": "sk-w14l-imp", "base_url": "https://api.openai.com/v1"}}},
					[]any{map[string]any{"ref": "p1", "name": "w14l-imp-proxy", "type": "socks5", "host": "proxy.example.com", "port": float64(1080)}},
				), "", ImportOptions{}, f.scope())
				return err
			},
		},
		"force-activate": {
			setup: func(f *w14lFixture) {
				input := w14lCreateInput("w14l-fa")
				input.Status = CreationStatus{Status: "pending_test", Schedulable: true}
				result, err := f.store.Create(context.Background(), input, f.scope())
				if err != nil {
					f.t.Fatal(err)
				}
				f.created = append(f.created, result.ID)
			},
			run: func(f *w14lFixture) error {
				_, err := f.store.ForceActivatePending(context.Background(), f.firstID(), f.scope())
				return err
			},
		},
		"lock": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-lock") },
			run: func(f *w14lFixture) error {
				_, err := f.store.SetLock(context.Background(), SetLockInput{
					AccountID: f.firstID(), Enabled: true, ExpectedConfigRevision: 1,
				}, f.scope())
				return err
			},
		},
		"reset-runtime": {
			setup: func(f *w14lFixture) { f.createViaStore("w14l-reset") },
			run: func(f *w14lFixture) error {
				_, err := f.store.ResetAccountRuntimeState(context.Background(), f.firstID(), 1, f.scope())
				return err
			},
		},
		"session-lifecycle": {
			run: func(f *w14lFixture) error {
				scope := f.scope()
				session, err := f.store.CreateTestSession(context.Background(), scope)
				if err != nil {
					return err
				}
				if _, err := f.store.HeartbeatTestSession(context.Background(), session.ID, &scope); err != nil {
					return err
				}
				_, err = f.store.CompleteTestSession(context.Background(), session.ID, &scope)
				return err
			},
		},
		"task-cancel": {
			run: func(f *w14lFixture) error {
				scope := f.scope()
				session, err := f.store.CreateTestSession(context.Background(), scope)
				if err != nil {
					return err
				}
				task, err := f.store.CreateTestTask(context.Background(), TestTaskCreateInput{
					AccountID: "w14l-task-acc", AccountName: "w14l-task", ProviderCode: "gpt",
					ProviderProtocolProfileID: "prof-gpt", ProtocolCode: "openai", ProtocolVersion: "v1",
					AccountType: "api_key", Access: scope, Diagnostics: "full",
					SessionID: session.ID, Model: "gpt-4o-mini", TestEndpointMode: "chat_json",
				})
				if err != nil {
					return err
				}
				if _, err := f.store.CancelTestTask(context.Background(), task.ID, &scope); err != nil {
					return err
				}
				_, err = f.store.CancelTestSession(context.Background(), session.ID, &scope, "w14l 取消")
				return err
			},
		},
		"cleanup-tasks": {
			setup: func(f *w14lFixture) {
				old := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
				f.exec(`INSERT INTO account_test_tasks (id, account_id, account_name, provider_code,
					provider_protocol_profile_id, protocol_code, protocol_version, account_type,
					request_system_account_id, request_role, diagnostics, status, status_message, cancel_requested,
					queued_at, queued_deadline_at, finished_at, created_at, updated_at)
					VALUES ('task-w14l-old', 'acc-x', '旧任务', 'gpt', 'prof-gpt', 'openai', 'v1', 'api_key',
					?, 'super_admin', 'full', 'succeeded', 'done', 0, ?, ?, ?, ?, ?)`,
					f.owner, old, old, old, old, old)
				f.exec(`INSERT INTO account_test_sessions (id, request_system_account_id, request_role,
					status, last_heartbeat_at, created_at, updated_at, finished_at)
					VALUES ('sess-w14l-old', ?, 'super_admin', 'succeeded', ?, ?, ?, ?)`,
					f.owner, old, old, old, old)
				f.exec(`INSERT INTO account_test_session_tasks (session_id, task_id, created_at)
					VALUES ('sess-w14l-old', 'task-w14l-old', ?)`, old)
			},
			run: func(f *w14lFixture) error {
				return f.store.cleanupExpiredTestTasks(context.Background())
			},
		},
		"read-family": {
			setup: func(f *w14lFixture) {
				f.createViaStore("w14l-read")
			},
			run: func(f *w14lFixture) error {
				ctx := context.Background()
				scope := f.scope()
				ids := f.accountIDs()
				if _, err := f.store.ListPage(ctx, scope, ListOptions{Page: 1, PageSize: 10}); err != nil {
					return err
				}
				if _, err := f.store.ListOptionSummaries(ctx, scope, ListOptions{}); err != nil {
					return err
				}
				if _, err := f.store.FindEditBasicDetail(ctx, ids[0], scope); err != nil {
					return err
				}
				if _, err := f.store.FindAdvancedDetail(ctx, ids[0], scope); err != nil {
					return err
				}
				if _, err := f.store.FindCloneContext(ctx, ids[0], scope); err != nil {
					return err
				}
				if _, err := f.store.ExportAccounts(ctx, ExportOptions{AccountIDs: ids}, scope); err != nil {
					return err
				}
				_, err := f.store.CollectExportIDs(ctx, map[string]any{}, scope)
				return err
			},
		},
		"traffic-migrate": {
			setup: func(f *w14lFixture) {
				f.createViaStore("w14l-tm-src")
				f.createViaStore("w14l-tm-dst")
			},
			run: func(f *w14lFixture) error {
				ids := f.accountIDs()
				_, err := f.store.migrateOwnerTraffic(context.Background(), ids[0],
					TrafficMigrationInput{TargetAccountID: ids[1]}, f.scope())
				return err
			},
		},
	}
}

// TestW14LQueryFaultSweep 扫描各序列的第 n 条语句失败。
func TestW14LQueryFaultSweep(t *testing.T) {
	for name, seq := range w14lSequences() {
		seq := seq
		t.Run(name, func(t *testing.T) {
			for n := 1; n <= 40; n++ {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					f := newW14LFaultFixture(t)
					if seq.setup != nil {
						seq.setup(f)
					}
					// 故障在 setup 之后注入，只影响 run 内的语句。
					f.script.failNth("", n)
					_ = seq.run(f)
				})
			}
		})
	}
}

// TestW14LCommitFaultArms 对每个事务链注入 Commit 失败，命中各提交臂与
// 提交后链路前的错误返回。
func TestW14LCommitFaultArms(t *testing.T) {
	for name, seq := range w14lSequences() {
		seq := seq
		t.Run(name, func(t *testing.T) {
			f := newW14LFaultFixture(t)
			if seq.setup != nil {
				seq.setup(f)
			}
			f.script.failCommit()
			_ = seq.run(f)
		})
	}
}

// TestW14LRollbackFaultArms 在“第 n 条语句失败 + Rollback 失败”组合下命中
// 显式检查回滚错误的臂（defer 里的忽略式回滚不受影响）。
func TestW14LRollbackFaultArms(t *testing.T) {
	for name, seq := range w14lSequences() {
		seq := seq
		t.Run(name, func(t *testing.T) {
			for n := 2; n <= 12; n += 2 {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					f := newW14LFaultFixture(t)
					if seq.setup != nil {
						seq.setup(f)
					}
					f.script.failRollback()
					f.script.failNth("", n)
					_ = seq.run(f)
				})
			}
		})
	}
}
