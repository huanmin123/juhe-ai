// Package jobsched 是 Node modules/background/worker-scheduler.ts 的 Go 移植：
// 单进程内多任务调度循环。语义对齐项：fixedRate/fixedDelay 两种调度模式、
// passive jitter（复用平台 schedulejitter 的窗口策略）、stable phase 窗口、
// overlap 策略（skip / coalesceOne）、resource lane 串行与交接、单轮超时、
// 失败退避（指数封顶 + 随机比例）、错过间隔的 skip/补跑、停机排空与运行快照。
//
// 与 Node 的结构差异（语义保持）：
//   - Node 用每任务 timer 回调 + 单线程事件循环；Go 用每任务一个状态 goroutine
//     串行推进 fire → run → 重排。"运行中不重入"约束等价于 Node 的
//     overlapPolicy=skip 默认行为；错过间隔（任务执行跨过锚点）按各自
//     overlapPolicy 处理：skip 记跳过，coalesceOne 结束后最多补跑一次。
//   - 租约获取不在调度器内（与 Node 一致：runWithPostgresScheduledLease 在
//     scheduler 之外包裹 task）；组合根通过 Task 闭包接入 taskruns.RunWithScheduledLease。
package jobsched

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	platformjitter "github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

// ScheduleMode 与 Node WorkerScheduledJobScheduleMode 一致。
type ScheduleMode string

const (
	// ScheduleModeFixedRate 对齐锚点推进（默认）。
	ScheduleModeFixedRate ScheduleMode = "fixedRate"
	// ScheduleModeFixedDelay 上一轮结束后再排下一轮。
	ScheduleModeFixedDelay ScheduleMode = "fixedDelay"
)

// OverlapPolicy 与 Node WorkerScheduledJobOverlapPolicy 一致。
type OverlapPolicy string

const (
	OverlapSkip OverlapPolicy = "skip"
	// OverlapCoalesceOne 错过间隔或 lane 释放后最多补跑一次。
	OverlapCoalesceOne OverlapPolicy = "coalesceOne"
)

// Outcome 与 Node WorkerScheduledJobOutcome 的任务级子集一致。
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomePartial Outcome = "partial"
	OutcomeSkipped Outcome = "skipped"
)

// LeaseState 与 Node WorkerScheduledJobLeaseState 一致。
type LeaseState string

const (
	LeaseNotRequired LeaseState = "not_required"
	LeaseAcquired    LeaseState = "acquired"
	LeaseBusy        LeaseState = "busy"
	LeaseLost        LeaseState = "lost"
)

// Backoff 与 Node WorkerScheduledJobFailureBackoffOptions 一致。
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// TaskContext 与 Node WorkerScheduledJobTaskContext 一致。
type TaskContext struct {
	ScheduledAt time.Time
	StartedAt   time.Time
	DeadlineAt  *time.Time
}

// TaskResult 与 Node WorkerScheduledJobTaskResult 一致；零值视为 success。
type TaskResult struct {
	Outcome    Outcome
	Warning    string
	LeaseState LeaseState
}

// Task 是一轮任务执行；返回非 nil error 记为失败并进入失败退避。
type Task func(ctx context.Context, taskCtx TaskContext) (TaskResult, error)

// Spec 与 Node WorkerScheduledJobOptions 一致。
type Spec struct {
	Name              string
	Interval          time.Duration
	InitialDelay      time.Duration
	StablePhaseWindow time.Duration
	PassiveJitter     bool
	// DeferFirstRun 为 true 时首轮推迟一个完整间隔（对应 Node
	// runImmediately=false；零值即 Node 默认的立即首轮）。
	DeferFirstRun bool
	ScheduleMode  ScheduleMode
	OverlapPolicy OverlapPolicy
	Timeout       time.Duration
	Lane          string
	Backoff       *Backoff
	Task          Task
}

// Timer/Clock 支持测试注入假时钟（mock 时间推进）。
type Timer interface {
	C() <-chan time.Time
	Stop()
}

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.timer.C }
func (t realTimer) Stop()               { t.timer.Stop() }

// Clock 注入时间源。
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// SystemClock 返回真实时钟。
type SystemClock struct{}

// Now 实现 Clock。
func (SystemClock) Now() time.Time { return time.Now() }

// NewTimer 实现 Clock。
func (SystemClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

// Snapshot 与 Node WorkerScheduledJobRuntimeSnapshot 对齐（字段子集）。
type Snapshot struct {
	Name             string
	IntervalMS       int64
	InitialDelayMS   int64
	StablePhaseMS    int64
	PassiveJitter    bool
	ScheduleMode     ScheduleMode
	OverlapPolicy    OverlapPolicy
	TimeoutMS        int64
	ResourceLane     string
	Running          bool
	Pending          bool
	QueuedForLane    bool
	NextRunAt        *time.Time
	RunningSince     *time.Time
	LastScheduledAt  *time.Time
	LastStartedAt    *time.Time
	LastFinishedAt   *time.Time
	LastSuccessAt    *time.Time
	LastErrorAt      *time.Time
	LastError        string
	LastWarningAt    *time.Time
	LastWarning      string
	LastSkipAt       *time.Time
	LastSkipReason   string
	LastOutcome      string
	LeaseState       LeaseState
	LastDurationMS   int64
	MaxDurationMS    int64
	ConsecutiveFails int64
	RunCount         int64
	SuccessCount     int64
	FailureCount     int64
	PartialCount     int64
	SkippedCount     int64
	TaskSkippedCount int64
	CoalescedCount   int64
	TimedOutCount    int64
}

// Options 与 Node WorkerSchedulerOptions 一致。
type Options struct {
	StableSeed string
	Clock      Clock
	Random     func() float64
}

// laneState 对齐 Node WorkerScheduledJobLaneState。
type laneState struct {
	runningJob string
	queue      []string
}

// Scheduler 是 WorkerScheduler 的 Go 等价。
type Scheduler struct {
	clock      Clock
	random     func() float64
	stableSeed string

	mu      sync.Mutex
	jobs    map[string]*jobState
	lanes   map[string]*laneState
	stopped bool

	runWG     sync.WaitGroup // 活跃任务执行
	runActive atomic.Int64

	stopOnce sync.Once
	stopCh   chan struct{}
}

type fireKind int

const (
	fireRegular fireKind = iota
	fireDeferred
	fireLaneWake
)

type jobState struct {
	spec       Spec
	stableMS   int64
	wake       chan struct{}
	laneQueued bool

	// 以下状态由 Scheduler.mu 保护。
	running       bool
	pending       bool
	fixedRateNext *time.Time
	deferredAt    *time.Time
	backoffUntil  *time.Time

	consecFail int64
	runCount   int64
	success    int64
	failure    int64
	partial    int64
	skipped    int64
	taskSkip   int64
	coalesced  int64
	timedOut   int64

	lastScheduledAt *time.Time
	lastStartedAt   *time.Time
	lastFinishedAt  *time.Time
	lastSuccessAt   *time.Time
	lastErrorAt     *time.Time
	lastError       string
	lastWarningAt   *time.Time
	lastWarning     string
	lastSkipAt      *time.Time
	lastSkipReason  string
	lastOutcome     Outcome
	leaseState      LeaseState
	lastDurationMS  int64
	maxDurationMS   int64
	runningSince    *time.Time
}

// NewScheduler 构建调度器。
func NewScheduler(options Options) *Scheduler {
	if options.Clock == nil {
		options.Clock = SystemClock{}
	}
	if options.Random == nil {
		options.Random = rand.Float64
	}
	return &Scheduler{
		clock:      options.Clock,
		random:     options.Random,
		stableSeed: options.StableSeed,
		jobs:       map[string]*jobState{},
		lanes:      map[string]*laneState{},
		stopCh:     make(chan struct{}),
	}
}

// Schedule 注册一个任务；停止后或重名注册被忽略（对齐 Node schedule）。
func (s *Scheduler) Schedule(spec Spec) {
	if spec.Name == "" || spec.Task == nil || spec.Interval <= 0 {
		return
	}
	if spec.ScheduleMode == "" {
		spec.ScheduleMode = ScheduleModeFixedRate
	}
	if spec.OverlapPolicy == "" {
		spec.OverlapPolicy = OverlapSkip
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, exists := s.jobs[spec.Name]; exists {
		s.mu.Unlock()
		return
	}
	state := &jobState{spec: spec, wake: make(chan struct{}, 1)}
	if spec.StablePhaseWindow > 0 {
		state.stableMS = stableOffsetMS(s.stableSeed+":"+spec.Name, spec.StablePhaseWindow)
	}
	s.jobs[spec.Name] = state
	s.mu.Unlock()
	go s.jobLoop(state)
}

// Stop 立即停止调度并丢弃全部任务（不等待活跃任务结束）。
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	s.stopped = true
	s.jobs = map[string]*jobState{}
	s.lanes = map[string]*laneState{}
	s.mu.Unlock()
}

// StopAndDrain 停止调度并等待活跃任务结束；超时未排空返回 drained=false 与
// 仍在运行的约数（对齐 Node stopAndDrain）。
func (s *Scheduler) StopAndDrain(timeout time.Duration) (drained bool, activeCount int) {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	s.stopped = true
	s.jobs = map[string]*jobState{}
	s.lanes = map[string]*laneState{}
	s.mu.Unlock()
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	done := make(chan struct{})
	go func() {
		s.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true, 0
	case <-time.After(timeout):
		return false, int(s.runActive.Load())
	}
}

// Snapshots 返回全部任务运行快照（按名字排序，对齐 Node snapshots）。
func (s *Scheduler) Snapshots() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Snapshot, 0, len(s.jobs))
	for name, job := range s.jobs {
		result = append(result, Snapshot{
			Name:             name,
			IntervalMS:       job.spec.Interval.Milliseconds(),
			InitialDelayMS:   job.spec.InitialDelay.Milliseconds(),
			StablePhaseMS:    job.stableMS,
			PassiveJitter:    job.spec.PassiveJitter,
			ScheduleMode:     job.spec.ScheduleMode,
			OverlapPolicy:    job.spec.OverlapPolicy,
			TimeoutMS:        job.spec.Timeout.Milliseconds(),
			ResourceLane:     job.spec.Lane,
			Running:          job.running,
			Pending:          job.pending,
			QueuedForLane:    job.laneQueued,
			NextRunAt:        earlierTime(job.fixedRateNext, job.deferredAt),
			RunningSince:     job.runningSince,
			LastScheduledAt:  job.lastScheduledAt,
			LastStartedAt:    job.lastStartedAt,
			LastFinishedAt:   job.lastFinishedAt,
			LastSuccessAt:    job.lastSuccessAt,
			LastErrorAt:      job.lastErrorAt,
			LastError:        job.lastError,
			LastWarningAt:    job.lastWarningAt,
			LastWarning:      job.lastWarning,
			LastSkipAt:       job.lastSkipAt,
			LastSkipReason:   job.lastSkipReason,
			LastOutcome:      string(job.lastOutcome),
			LeaseState:       job.leaseState,
			LastDurationMS:   job.lastDurationMS,
			MaxDurationMS:    job.maxDurationMS,
			ConsecutiveFails: job.consecFail,
			RunCount:         job.runCount,
			SuccessCount:     job.success,
			FailureCount:     job.failure,
			PartialCount:     job.partial,
			SkippedCount:     job.skipped,
			TaskSkippedCount: job.taskSkip,
			CoalescedCount:   job.coalesced,
			TimedOutCount:    job.timedOut,
		})
	}
	sortSnapshots(result)
	return result
}

func sortSnapshots(list []Snapshot) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Name < list[j-1].Name; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func earlierTime(left, right *time.Time) *time.Time {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	case right.Before(*left):
		return right
	default:
		return left
	}
}

// ---------------------------------------------------------------------------
// 任务主循环

func (s *Scheduler) jobLoop(job *jobState) {
	spec := job.spec
	now := s.clock.Now()

	initialScheduleDelay := spec.InitialDelay + time.Duration(job.stableMS)*time.Millisecond
	if spec.DeferFirstRun {
		initialScheduleDelay += spec.Interval
	}
	firstDelay := initialScheduleDelay
	if spec.PassiveJitter && initialScheduleDelay > 0 {
		firstDelay = passiveScheduleInitialDelayMS(initialScheduleDelay, spec.Interval, s.random)
	}

	if firstDelay <= 0 {
		if spec.ScheduleMode == ScheduleModeFixedRate {
			next := now.Add(passiveIntervalDelay(spec.Interval, spec.PassiveJitter, s.random))
			s.setFixedRateNext(job, &next)
		}
		stamp := now
		s.setLastScheduled(job, &stamp)
		s.fire(job, fireRegular, now)
	} else {
		// fixedRateNext 同时承载 fixedDelay 的首个触发目标。
		first := now.Add(firstDelay)
		s.setFixedRateNext(job, &first)
		stamp := first
		s.setLastScheduled(job, &stamp)
	}
	if s.isStopped() {
		return
	}

	var lastRunEndedAt time.Time
	for {
		target, kind := s.nextTarget(job)
		if target == nil {
			return
		}
		wake := s.waitTarget(job, *target)
		if !wake && s.isStopped() {
			return
		}
		now = s.clock.Now()
		if wake {
			kind = fireLaneWake
		} else if kind == fireRegular && spec.ScheduleMode == ScheduleModeFixedRate {
			s.advanceFixedRate(job, *target)
		} else if kind == fireRegular && spec.ScheduleMode == ScheduleModeFixedDelay {
			// fixedDelay 的触发目标一次性：fire 后清空，由收尾重排排定下一轮。
			s.setFixedRateNext(job, nil)
		}
		if kind == fireRegular {
			// 错过间隔（上一轮执行跨过本锚点）按 overlap 策略处理。
			if !lastRunEndedAt.IsZero() && target.Before(lastRunEndedAt) {
				if spec.OverlapPolicy == OverlapCoalesceOne {
					s.markCoalesced(job, now)
				} else {
					s.recordSkip(job, now, "running")
					continue
				}
			}
			if s.inBackoff(job, now) {
				s.recordSkip(job, now, "failure_backoff")
				continue
			}
		}
		if kind == fireDeferred {
			s.clearDeferred(job)
		}
		s.fire(job, kind, scheduledAtFor(kind, *target, now))
		lastRunEndedAt = s.clock.Now()

		// 收尾重排：退避延迟重试 / 补跑 / fixedDelay 下一轮。
		if s.armPostRun(job) {
			continue
		}
	}
}

func scheduledAtFor(kind fireKind, target, now time.Time) time.Time {
	if kind == fireLaneWake {
		return now
	}
	return target
}

// waitTarget 等待目标时间到达、lane 交接唤醒或停机；返回是否为 lane 唤醒。
// 延迟按注入时钟计算，测试可用假时钟推进。
func (s *Scheduler) waitTarget(job *jobState, target time.Time) (laneWake bool) {
	timer := s.clock.NewTimer(clampDelay(target.Sub(s.clock.Now())))
	defer timer.Stop()
	select {
	case <-s.stopCh:
		return false
	case <-timer.C():
		return false
	case <-job.wake:
		return true
	}
}

// nextTarget 返回最近的触发目标（regular 或 deferred 中更早者）。
func (s *Scheduler) nextTarget(job *jobState) (*time.Time, fireKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case job.fixedRateNext != nil && job.deferredAt != nil:
		if job.deferredAt.Before(*job.fixedRateNext) {
			return job.deferredAt, fireDeferred
		}
		return job.fixedRateNext, fireRegular
	case job.deferredAt != nil:
		return job.deferredAt, fireDeferred
	case job.fixedRateNext != nil:
		return job.fixedRateNext, fireRegular
	}
	return nil, fireRegular
}

// advanceFixedRate 推进 fixedRate 锚点（对齐 Node 在 fire 时先排下一轮）。
func (s *Scheduler) advanceFixedRate(job *jobState, scheduledAt time.Time) {
	now := s.clock.Now()
	next := nextFixedRateTarget(scheduledAt, job.spec.Interval, now)
	if job.spec.PassiveJitter {
		offset := time.Duration(passiveScheduleOffsetMS(job.spec.Interval, s.random)) * time.Millisecond
		if candidate := next.Add(offset); candidate.After(now) {
			next = candidate
		} else {
			next = now.Add(time.Millisecond)
		}
	}
	s.setFixedRateNext(job, &next)
}

// fire 执行一轮：lane 获取 → 执行 → 记账 → lane 释放。
func (s *Scheduler) fire(job *jobState, kind fireKind, scheduledAt time.Time) {
	if s.isStopped() {
		return
	}
	if !s.acquireLane(job) {
		return
	}
	s.runOnce(job, scheduledAt)
	s.releaseLane(job)
}

// acquireLane 对齐 Node acquireLane。
func (s *Scheduler) acquireLane(job *jobState) bool {
	laneName := job.spec.Lane
	if laneName == "" {
		return true
	}
	s.mu.Lock()
	lane, ok := s.lanes[laneName]
	if !ok {
		lane = &laneState{}
		s.lanes[laneName] = lane
	}
	if lane.runningJob == "" {
		lane.runningJob = job.spec.Name
		s.mu.Unlock()
		return true
	}
	now := s.clock.Now()
	if job.spec.OverlapPolicy == OverlapSkip {
		job.skipped++
		stamp := now
		job.lastSkipAt = &stamp
		job.lastSkipReason = "resource_lane_busy:" + laneName
		job.lastOutcome = OutcomeSkipped
		s.mu.Unlock()
		return false
	}
	if !job.laneQueued {
		job.laneQueued = true
		stamp := now
		job.lastSkipAt = &stamp
		job.lastSkipReason = "resource_lane_busy:" + laneName
		lane.queue = append(lane.queue, job.spec.Name)
	}
	s.mu.Unlock()
	return false
}

// releaseLane 对齐 Node releaseLane：释放后交接队首任务并唤醒其 goroutine。
func (s *Scheduler) releaseLane(job *jobState) {
	laneName := job.spec.Lane
	if laneName == "" {
		return
	}
	s.mu.Lock()
	lane, ok := s.lanes[laneName]
	if !ok {
		s.mu.Unlock()
		return
	}
	lane.runningJob = ""
	for len(lane.queue) > 0 && !s.stopped {
		nextName := lane.queue[0]
		lane.queue = lane.queue[1:]
		next, exists := s.jobs[nextName]
		if !exists || next.running {
			continue
		}
		next.laneQueued = false
		lane.runningJob = nextName
		s.mu.Unlock()
		select {
		case next.wake <- struct{}{}:
		default:
		}
		return
	}
	s.mu.Unlock()
}

// runOnce 同步执行一轮任务（对应 Node runJob）。
func (s *Scheduler) runOnce(job *jobState, scheduledAt time.Time) {
	spec := job.spec
	startedAt := s.clock.Now()

	s.mu.Lock()
	job.running = true
	job.runCount++
	job.lastStartedAt = &startedAt
	runningSince := startedAt
	job.runningSince = &runningSince
	scheduledStamp := scheduledAt
	job.lastScheduledAt = &scheduledStamp
	s.mu.Unlock()

	taskCtx, cancel := context.WithCancel(s.contextBoundToStop())
	var deadline *time.Time
	if spec.Timeout > 0 {
		taskCtx, cancel = context.WithTimeout(taskCtx, spec.Timeout)
		deadlineValue := startedAt.Add(spec.Timeout)
		deadline = &deadlineValue
	}

	s.runWG.Add(1)
	s.runActive.Add(1)
	var ctxErr error
	result, runErr := func() (result TaskResult, err error) {
		defer s.runWG.Done()
		defer s.runActive.Add(-1)
		// 先取样 ctx 状态再 cancel：cancel 本身会把 Err 变成 Canceled，
		// 不能作为停机/超时的判定依据。
		defer func() { ctxErr = taskCtx.Err(); cancel() }()
		return spec.Task(taskCtx, TaskContext{
			ScheduledAt: scheduledAt,
			StartedAt:   startedAt,
			DeadlineAt:  deadline,
		})
	}()

	finishedAt := s.clock.Now()
	duration := finishedAt.Sub(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	timedOut := spec.Timeout > 0 && ctxErr == context.DeadlineExceeded
	stoppedRun := !timedOut && (ctxErr == context.Canceled || s.isStopped())

	s.mu.Lock()
	job.running = false
	job.runningSince = nil
	job.lastFinishedAt = &finishedAt
	job.lastDurationMS = duration
	if job.maxDurationMS == 0 || duration > job.maxDurationMS {
		job.maxDurationMS = duration
	}
	switch {
	case stoppedRun:
		job.taskSkip++
		job.lastOutcome = OutcomeSkipped
		job.lastSkipAt = &finishedAt
		job.lastSkipReason = "scheduler_stopped"
	case timedOut:
		job.failure++
		job.timedOut++
		job.consecFail++
		job.lastOutcome = OutcomeSkipped
		job.lastSkipAt = &finishedAt
		job.lastSkipReason = "timeout"
		job.lastErrorAt = &finishedAt
		job.lastError = "后台任务执行超时"
		job.backoffUntil = s.backoffTargetLocked(job, job.consecFail, finishedAt)
	case runErr != nil:
		job.failure++
		job.consecFail++
		job.lastOutcome = OutcomeSkipped
		job.lastErrorAt = &finishedAt
		job.lastError = runErr.Error()
		job.backoffUntil = s.backoffTargetLocked(job, job.consecFail, finishedAt)
	default:
		switch result.Outcome {
		case OutcomePartial:
			job.partial++
			job.consecFail = 0
			job.backoffUntil = nil
			job.lastOutcome = OutcomePartial
			job.lastWarningAt = &finishedAt
			if result.Warning == "" {
				job.lastWarning = "后台任务部分完成"
			} else {
				job.lastWarning = result.Warning
			}
			job.lastError = ""
		case OutcomeSkipped:
			job.taskSkip++
			job.consecFail = 0
			job.backoffUntil = nil
			job.lastOutcome = OutcomeSkipped
			job.lastSkipAt = &finishedAt
			if result.Warning == "" {
				job.lastSkipReason = "task_skipped"
			} else {
				job.lastSkipReason = result.Warning
			}
			job.lastError = ""
		default:
			job.success++
			job.consecFail = 0
			job.backoffUntil = nil
			job.lastOutcome = OutcomeSuccess
			job.lastSuccessAt = &finishedAt
			job.lastError = ""
			job.lastWarning = ""
		}
		if result.LeaseState != "" {
			job.leaseState = result.LeaseState
		}
	}
	s.mu.Unlock()
}

// armPostRun 在一轮结束后收尾重排（对齐 Node runJob finally）：失败退避安排
// 一次到期重试；补跑标记立即重试；fixedDelay 从本轮结束排定下一轮。返回 true
// 表示已安排 deferred fire。
func (s *Scheduler) armPostRun(job *jobState) bool {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.spec.Backoff != nil && job.backoffUntil != nil && now.Before(*job.backoffUntil) {
		// 失败退避：安排一次退避到期后的重试（Node scheduleDeferredRun）。
		at := *job.backoffUntil
		job.deferredAt = &at
		return true
	}
	if job.pending && !job.laneQueued {
		job.pending = false
		job.deferredAt = &now
		return true
	}
	if job.spec.ScheduleMode == ScheduleModeFixedDelay {
		next := now.Add(passiveIntervalDelay(job.spec.Interval, job.spec.PassiveJitter, s.random))
		job.fixedRateNext = &next
	}
	return false
}

func (s *Scheduler) contextBoundToStop() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

func (s *Scheduler) setFixedRateNext(job *jobState, at *time.Time) {
	s.mu.Lock()
	job.fixedRateNext = at
	s.mu.Unlock()
}

func (s *Scheduler) setLastScheduled(job *jobState, at *time.Time) {
	s.mu.Lock()
	job.lastScheduledAt = at
	s.mu.Unlock()
}

func (s *Scheduler) clearDeferred(job *jobState) {
	s.mu.Lock()
	job.deferredAt = nil
	s.mu.Unlock()
}

func (s *Scheduler) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Scheduler) inBackoff(job *jobState, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return job.backoffUntil != nil && now.Before(*job.backoffUntil)
}

func (s *Scheduler) recordSkip(job *jobState, now time.Time, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job.skipped++
	stamp := now
	job.lastSkipAt = &stamp
	job.lastSkipReason = reason
	job.lastOutcome = OutcomeSkipped
}

func (s *Scheduler) markCoalesced(job *jobState, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job.coalesced++
	stamp := now
	job.lastSkipAt = &stamp
	job.lastSkipReason = "running:coalesced"
}

// backoffTargetLocked 计算 backoffUntil（对齐 failureBackoffDelayMs）。
func (s *Scheduler) backoffTargetLocked(job *jobState, consecFail int64, now time.Time) *time.Time {
	backoff := job.spec.Backoff
	if backoff == nil || backoff.Base <= 0 {
		return nil
	}
	max := backoff.Max
	if max < backoff.Base {
		max = backoff.Base
	}
	exponent := consecFail - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 30 {
		exponent = 30
	}
	ceiling := backoff.Base << uint(exponent)
	if ceiling > max {
		ceiling = max
	}
	fraction := clamp01(s.random())
	if fraction >= 1 {
		return ptrTime(now.Add(ceiling))
	}
	delayMS := fraction * float64(ceiling.Milliseconds()+1)
	return ptrTime(now.Add(time.Duration(delayMS) * time.Millisecond))
}

func ptrTime(t time.Time) *time.Time { return &t }

func clampDelay(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// passiveIntervalDelay 对齐 Node nextPassiveIntervalDelay。
func passiveIntervalDelay(interval time.Duration, passive bool, random func() float64) time.Duration {
	if !passive {
		return interval
	}
	offset := time.Duration(passiveScheduleOffsetMS(interval, random)) * time.Millisecond
	delay := interval + offset
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

// nextFixedRateTarget 对齐 Node nextFixedRateTargetMs。
func nextFixedRateTarget(previous time.Time, interval time.Duration, now time.Time) time.Time {
	elapsed := now.Sub(previous)
	if elapsed < 0 {
		elapsed = 0
	}
	intervals := elapsed/interval + 1
	return previous.Add(intervals * interval)
}

// stableOffsetMS 对齐 Node stableScheduledJobOffsetMs（FNV-1a % window）。
func stableOffsetMS(seed string, window time.Duration) int64 {
	windowMS := window.Milliseconds()
	if windowMS <= 0 {
		return 0
	}
	var hash uint32 = 2166136261
	for _, character := range seed {
		hash ^= uint32(character)
		hash *= 16777619
	}
	return int64(hash % uint32(windowMS))
}

// passiveScheduleOffsetMS 对齐 Node passiveScheduleOffsetMs：对称窗口内随机
// 偏移，0 归一为 1ms。
func passiveScheduleOffsetMS(interval time.Duration, random func() float64) int64 {
	windowMS := platformjitter.Window(interval).Milliseconds()
	if windowMS <= 0 {
		return 0
	}
	sampled := clamp01(random())
	offset := int64(sampled*float64(windowMS*2+1)) - windowMS
	if offset == 0 {
		return 1
	}
	return offset
}

// passiveScheduleInitialDelayMS 对齐 Node passiveScheduleInitialDelayMs：
// 首个延迟启动被有界扰动，且不允许被提前到 0。
func passiveScheduleInitialDelayMS(initialDelay, interval time.Duration, random func() float64) time.Duration {
	delayMS := initialDelay.Milliseconds()
	if delayMS < 1 {
		delayMS = 1
	}
	windowMS := platformjitter.Window(interval).Milliseconds()
	if half := delayMS / 2; windowMS > half {
		windowMS = half
	}
	if windowMS <= 0 {
		return time.Duration(delayMS) * time.Millisecond
	}
	sampled := clamp01(random())
	offset := int64(sampled*float64(windowMS*2+1)) - windowMS
	result := delayMS + offset
	if result < 1 {
		result = 1
	}
	return time.Duration(result) * time.Millisecond
}

func clamp01(value float64) float64 {
	if value < 0 || value != value {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
