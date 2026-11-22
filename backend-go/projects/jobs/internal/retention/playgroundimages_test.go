package retention

// 仿 mediajobs_test.go 的 fake store 模式：内存行集模拟 SQL 语义（扫描
// 过滤/删除/置位守卫），任务层只验证编排（分批、幂等、表缺席快速返回、
// 阶段二窗口），SQL 方言正确性由表结构与真实库承担。
import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePlaygroundJob struct {
	account, id   string
	status        string
	expiresAt     int64
	imagesExpired bool
	updatedAt     int64
}

type fakePlaygroundArtifact struct {
	account, id, kind, jobID string
	createdAt                int64
	deleted                  bool
}

type fakePlaygroundStore struct {
	jobs      map[string]*fakePlaygroundJob
	artifacts []*fakePlaygroundArtifact
	// opErrs 按 ("list"|"deleteJob"|"mark"|"legacy"|"orphan") 注入单操作
	// 错误（表缺席/任意错误），缺省 nil。
	opErrs map[string]error

	listCalls, deleteJobCalls, markCalls, legacyCalls, orphanCalls int
}

func (s *fakePlaygroundStore) err(op string) error {
	if s.opErrs == nil {
		return nil
	}
	return s.opErrs[op]
}

func (s *fakePlaygroundStore) key(account, jobID string) string {
	return account + "|" + jobID
}

func (s *fakePlaygroundStore) ListExpiredJobs(_ context.Context, nowMS, failedBeforeMS int64, limit int) ([]PlaygroundImageJobRef, error) {
	s.listCalls++
	if err := s.err("list"); err != nil {
		return nil, err
	}
	// 镜像 SQL 终形：先扫到期臂（expires_at>0，走部分索引），不足一批再
	// 补扫 failed 超窗臂，两臂结果按键去重取并集。
	var refs []PlaygroundImageJobRef
	seen := map[string]bool{}
	collect := func(match func(*fakePlaygroundJob) bool) {
		for _, job := range s.jobs {
			if len(refs) >= limit {
				break
			}
			key := s.key(job.account, job.id)
			if seen[key] || !match(job) {
				continue
			}
			seen[key] = true
			refs = append(refs, PlaygroundImageJobRef{SystemAccountID: job.account, JobID: job.id})
		}
	}
	collect(func(j *fakePlaygroundJob) bool {
		return j.expiresAt > 0 && !j.imagesExpired && j.expiresAt < nowMS
	})
	if len(refs) < limit {
		collect(func(j *fakePlaygroundJob) bool {
			return j.status == "failed" && !j.imagesExpired && j.updatedAt < failedBeforeMS
		})
	}
	return refs, nil
}

func (s *fakePlaygroundStore) DeleteJobArtifacts(_ context.Context, account, jobID string) (int64, error) {
	s.deleteJobCalls++
	if err := s.err("deleteJob"); err != nil {
		return 0, err
	}
	var n int64
	for _, a := range s.artifacts {
		if !a.deleted && a.account == account && a.jobID == jobID &&
			(a.kind == "image_result" || a.kind == "image_reference") {
			a.deleted = true
			n++
		}
	}
	return n, nil
}

func (s *fakePlaygroundStore) MarkJobImagesExpired(_ context.Context, account, jobID string, nowMS int64) (int64, error) {
	s.markCalls++
	if err := s.err("mark"); err != nil {
		return 0, err
	}
	job, ok := s.jobs[s.key(account, jobID)]
	if !ok || job.imagesExpired {
		return 0, nil
	}
	job.imagesExpired = true
	job.updatedAt = nowMS
	return 1, nil
}

func (s *fakePlaygroundStore) DeleteLegacyImagesBefore(_ context.Context, before int64) (int64, error) {
	s.legacyCalls++
	if err := s.err("legacy"); err != nil {
		return 0, err
	}
	var n int64
	for _, a := range s.artifacts {
		if !a.deleted && a.kind == "image" && a.createdAt < before {
			a.deleted = true
			n++
		}
	}
	return n, nil
}

func (s *fakePlaygroundStore) DeleteOrphanReferencesBefore(_ context.Context, before int64) (int64, error) {
	s.orphanCalls++
	if err := s.err("orphan"); err != nil {
		return 0, err
	}
	var n int64
	for _, a := range s.artifacts {
		if !a.deleted && a.kind == "image_reference" && a.jobID == "" && a.createdAt < before {
			a.deleted = true
			n++
		}
	}
	return n, nil
}

func (s *fakePlaygroundStore) aliveArtifact(id string) *fakePlaygroundArtifact {
	for _, a := range s.artifacts {
		if a.id == id {
			return a
		}
	}
	return nil
}

var playgroundTestNowMS = time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC).UnixMilli()

func playgroundTestClock() func() time.Time {
	return func() time.Time { return time.UnixMilli(playgroundTestNowMS) }
}

// newPlaygroundTestStore 按阶段一/阶段二窗口语义铺一个典型行集：
//   - jobA：completed 到期未置位（expires_at=now-1h），带结果图+参考图 → 应删字节并置位；
//   - jobB：completed 未到期（expires_at=now+6d）→ 不动（新 failed 臂不影响既有 completed 行）；
//   - jobC：failed 超窗（updated_at=now-8d，expires_at=0），带参考图 → 应删参考图并置位；
//   - jobD：上轮已置位 → 不重扫不重删；
//   - jobE：failed 未超窗（updated_at=now-6d）→ 不动；
//   - jobF：running 滞留（updated_at=now-8d）→ 不动（failed 臂按 status
//     过滤，不误扫进行中任务）；
//   - v1.0 遗留 kind='image'：8d 旧行删、1d 新行留；
//   - 孤儿参考图（job_id=”）：25h 旧行删、1h 新行留；speech/video_task 旧
//     行归懒清理职责 → 不动。
func newPlaygroundTestStore() *fakePlaygroundStore {
	store := &fakePlaygroundStore{jobs: map[string]*fakePlaygroundJob{}}
	store.jobs["u1|job_a"] = &fakePlaygroundJob{account: "u1", id: "job_a", status: "completed", expiresAt: playgroundTestNowMS - time.Hour.Milliseconds()}
	store.jobs["u1|job_b"] = &fakePlaygroundJob{account: "u1", id: "job_b", status: "completed", expiresAt: playgroundTestNowMS + 6*24*time.Hour.Milliseconds(), updatedAt: playgroundTestNowMS - time.Hour.Milliseconds()}
	store.jobs["u2|job_c"] = &fakePlaygroundJob{account: "u2", id: "job_c", status: "failed", updatedAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()}
	store.jobs["u2|job_d"] = &fakePlaygroundJob{account: "u2", id: "job_d", status: "completed", expiresAt: playgroundTestNowMS - 2*time.Hour.Milliseconds(), imagesExpired: true, updatedAt: playgroundTestNowMS - 2*time.Hour.Milliseconds()}
	store.jobs["u2|job_e"] = &fakePlaygroundJob{account: "u2", id: "job_e", status: "failed", updatedAt: playgroundTestNowMS - 6*24*time.Hour.Milliseconds()}
	store.jobs["u2|job_f"] = &fakePlaygroundJob{account: "u2", id: "job_f", status: "running", updatedAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()}
	store.artifacts = []*fakePlaygroundArtifact{
		{account: "u1", id: "art_a1", kind: "image_result", jobID: "job_a", createdAt: playgroundTestNowMS - time.Hour.Milliseconds()},
		{account: "u1", id: "art_r1", kind: "image_reference", jobID: "job_a", createdAt: playgroundTestNowMS - 2*time.Hour.Milliseconds()},
		{account: "u1", id: "art_b1", kind: "image_result", jobID: "job_b", createdAt: playgroundTestNowMS - time.Hour.Milliseconds()},
		{account: "u2", id: "art_c1", kind: "image_reference", jobID: "job_c", createdAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()},
		{account: "u2", id: "art_d1", kind: "image_result", jobID: "job_d", createdAt: playgroundTestNowMS - 2*time.Hour.Milliseconds()},
		{account: "u2", id: "art_e1", kind: "image_reference", jobID: "job_e", createdAt: playgroundTestNowMS - 6*24*time.Hour.Milliseconds()},
		{account: "u2", id: "art_f1", kind: "image_reference", jobID: "job_f", createdAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()},
		{account: "u3", id: "art_l1", kind: "image", jobID: "", createdAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()},
		{account: "u3", id: "art_l2", kind: "image", jobID: "", createdAt: playgroundTestNowMS - 24*time.Hour.Milliseconds()},
		{account: "u3", id: "art_o1", kind: "image_reference", jobID: "", createdAt: playgroundTestNowMS - 25*time.Hour.Milliseconds()},
		{account: "u3", id: "art_o2", kind: "image_reference", jobID: "", createdAt: playgroundTestNowMS - time.Hour.Milliseconds()},
		{account: "u3", id: "art_s1", kind: "speech", jobID: "", createdAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()},
		{account: "u3", id: "art_v1", kind: "video_task", jobID: "", createdAt: playgroundTestNowMS - 8*24*time.Hour.Milliseconds()},
	}
	return store
}

func TestPlaygroundImagesRetentionExpiresAndDeletesArtifacts(t *testing.T) {
	store := newPlaygroundTestStore()
	job := &PlaygroundImagesRetentionJob{Store: store, BatchSize: 500, Clock: playgroundTestClock()}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExpiredJobs != 2 || result.DeletedArtifacts != 3 {
		t.Fatalf("phase1 = %+v, want expiredJobs:2 deletedArtifacts:3（jobA 到期 + jobC failed 超窗）", result)
	}
	if result.DeletedLegacyImages != 1 || result.DeletedOrphanReferences != 1 {
		t.Fatalf("phase2 = %+v, want legacy:1 orphan:1", result)
	}
	if result.Batches != 1 {
		t.Fatalf("batches = %d, want 1", result.Batches)
	}
	jobA := store.jobs["u1|job_a"]
	if !jobA.imagesExpired || jobA.updatedAt != playgroundTestNowMS {
		t.Fatalf("job_a 置位错误: %+v", jobA)
	}
	if store.aliveArtifact("art_a1").deleted != true || store.aliveArtifact("art_r1").deleted != true {
		t.Fatal("job_a 的结果图/参考图应删除")
	}
	jobC := store.jobs["u2|job_c"]
	if !jobC.imagesExpired || jobC.updatedAt != playgroundTestNowMS {
		t.Fatalf("job_c（failed 超窗）置位错误: %+v", jobC)
	}
	if !store.aliveArtifact("art_c1").deleted {
		t.Fatal("job_c 的参考图应删除")
	}
	// 非目标行不动。
	for _, id := range []string{"art_b1", "art_d1", "art_e1", "art_f1", "art_l2", "art_o2", "art_s1", "art_v1"} {
		if store.aliveArtifact(id).deleted {
			t.Fatalf("%s 不应删除", id)
		}
	}
	if store.jobs["u1|job_b"].imagesExpired || store.jobs["u2|job_d"].updatedAt != playgroundTestNowMS-2*time.Hour.Milliseconds() ||
		store.jobs["u2|job_e"].imagesExpired || store.jobs["u2|job_f"].imagesExpired {
		t.Fatal("未到期/已置位/未超窗 failed/滞留 running 任务行不应被改写")
	}
}

func TestPlaygroundImagesRetentionIdempotentRerun(t *testing.T) {
	store := newPlaygroundTestStore()
	job := &PlaygroundImagesRetentionJob{Store: store, BatchSize: 500, Clock: playgroundTestClock()}
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.ExpiredJobs != 0 || second.DeletedArtifacts != 0 ||
		second.DeletedLegacyImages != 0 || second.DeletedOrphanReferences != 0 {
		t.Fatalf("second result = %+v, want 全零（重跑不重复删/不重复置位）", second)
	}
	if second.Batches != 1 {
		t.Fatalf("second batches = %d, want 1", second.Batches)
	}
	// 删除态与置位态与首轮一致。
	for _, id := range []string{"art_a1", "art_r1", "art_c1", "art_l1", "art_o1"} {
		if !store.aliveArtifact(id).deleted {
			t.Fatalf("%s 首轮已删，重跑后应保持删除态", id)
		}
	}
	if store.jobs["u1|job_a"].updatedAt != playgroundTestNowMS {
		t.Fatalf("job_a updated_at 被重跑改写: %d", store.jobs["u1|job_a"].updatedAt)
	}
}

func TestPlaygroundImagesRetentionTableMissingFastReturn(t *testing.T) {
	// Store 未初始化显式报错。
	if _, err := (&PlaygroundImagesRetentionJob{Clock: playgroundTestClock()}).Run(context.Background()); err == nil {
		t.Fatal("store 未初始化应报错")
	}
	// 任一表缺席 → 零任务快速返回（nil error）。缺席时点不同，快速返回
	// 前已完成的工作保留（list/deleteJob 在阶段一前段中断故全零；legacy
	// 在阶段一完成后中断，已完成计数保留、阶段二计数为零）。
	for op, expected := range map[string]PlaygroundImagesRetentionResult{
		"list":      {},
		"deleteJob": {},
		"legacy":    {ExpiredJobs: 2, DeletedArtifacts: 3, Batches: 1},
	} {
		message := `pq: relation "juhe_business.playground_artifacts" does not exist`
		if op == "list" {
			message = `pq: relation "juhe_business.playground_image_jobs" does not exist`
		}
		store := newPlaygroundTestStore()
		store.opErrs = map[string]error{op: errors.New(message)}
		job := &PlaygroundImagesRetentionJob{Store: store, BatchSize: 500, Clock: playgroundTestClock()}
		result, err := job.Run(context.Background())
		if err != nil {
			t.Fatalf("op %s 表缺席应零任务返回, got err: %v", op, err)
		}
		if result != expected {
			t.Fatalf("op %s 快速返回结果 = %+v, want %+v", op, result, expected)
		}
	}
	// 非缺席错误照常透出，不吞。
	store := newPlaygroundTestStore()
	store.opErrs = map[string]error{"list": errors.New("connection refused")}
	job := &PlaygroundImagesRetentionJob{Store: store, BatchSize: 500, Clock: playgroundTestClock()}
	if _, err := job.Run(context.Background()); err == nil {
		t.Fatal("非表缺席错误应透出")
	}
}

func TestPlaygroundImagesRetentionBatchCap(t *testing.T) {
	// 少量积压：25 行 × 批 10 → 3 批清完即退。
	small := &fakePlaygroundStore{jobs: map[string]*fakePlaygroundJob{}}
	for i := 0; i < 25; i++ {
		id := "job_s" + string(rune('a'+i))
		small.jobs["u1|"+id] = &fakePlaygroundJob{account: "u1", id: id, expiresAt: playgroundTestNowMS - time.Hour.Milliseconds()}
	}
	job := &PlaygroundImagesRetentionJob{Store: small, BatchSize: 10, Clock: playgroundTestClock()}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExpiredJobs != 25 || result.Batches != 3 || small.listCalls != 3 {
		t.Fatalf("small = %+v listCalls:%d, want 25/3/3", result, small.listCalls)
	}
	// 大积压：250 行 × 批 10 → 单轮 20 批封顶（200 行），剩余留下轮。
	large := &fakePlaygroundStore{jobs: map[string]*fakePlaygroundJob{}}
	for i := 0; i < 250; i++ {
		id := "job_l" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		large.jobs["u1|"+id] = &fakePlaygroundJob{account: "u1", id: id, expiresAt: playgroundTestNowMS - time.Hour.Milliseconds()}
	}
	job = &PlaygroundImagesRetentionJob{Store: large, BatchSize: 10, Clock: playgroundTestClock()}
	result, err = job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExpiredJobs != 200 || result.Batches != 20 || large.listCalls != 20 {
		t.Fatalf("large = %+v listCalls:%d, want 200/20/20（单轮批次上限）", result, large.listCalls)
	}
}
