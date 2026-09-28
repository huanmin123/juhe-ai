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
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
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
	// Logger 为可选的逐轮 outcome 日志（W2）；nil（零值）时全部日志调用
	// 为 no-op，调度行为与内存记账不变。
	Logger *slog.Logger
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
	logger     *slog.Logger

	mu      sync.Mutex
	jobs    map[string]*jobState
	lanes   map[string]*laneState
	stopped bool

	runWG     sync.WaitGroup // 活跃任务执行
	runActive atomic.Int64

	stopOnce sync.Once
	stopCh   chan struct{}

	// stopBoundCtx 是绑定停机信号的任务根 ctx（BUG-0220）：Scheduler 级
	// 单次构造，Stop/StopAndDrain 在 close(stopCh) 的同一 stopOnce 闭包里
	// 经 stopBoundCancel 取消。每轮 taskCtx 从它派生；不得改为每轮新建
	// ctx + 监听 goroutine——那样每轮泄漏 1 个 goroutine，仅 Scheduler
	// 停机时释放，长驻进程无界累积。
	stopBoundCtx    context.Context
	stopBoundCancel context.CancelFunc
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
	running bool
	// pending 当前无置位路径（BUG-0223）：全文件仅有 armPostRun 的一处清零，
	// 恒为 false。Node coalesceOne 的「结束后补跑一次」在 Go 由「错过锚点
	// 立即照常 fire」承担（见 jobLoop 错过间隔分支），不经过本字段；
	// Snapshot.Pending 保留仅为快照形状兼容，不得在新代码中依赖它表达
	// 补跑状态。
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

	// 超时强制回收后仍在运行的泄漏 handler 观测（mu 保护）：leakStartedAt
	// 是该泄漏 run 的开始时间，handler 迟到返回时清除；stuckLogNext 与
	// stuckLogInterval 驱动 jobsched_run_stuck 的指数重复间隔。
	leakStartedAt    *time.Time
	stuckLogNext     *time.Time
	stuckLogInterval time.Duration
}

// 超时泄漏观测的节奏参数（包级 var 便于测试注入小值验证触发与间隔）。
var (
	// stuckGrace 是超时回收后 handler 仍在跑的宽限：超过 Timeout + grace
	// 才开始打 jobsched_run_stuck。
	stuckGrace = 5 * time.Minute
	// stuckLogRepeat 是 stuck 日志的起始重复间隔，之后按 ×2 指数增长，
	// 封顶 stuckLogRepeatMax，防止日志洪水。
	stuckLogRepeat    = 5 * time.Minute
	stuckLogRepeatMax = 20 * time.Minute
	// stuckWatchInterval 是泄漏 run 的扫描周期。
	stuckWatchInterval = time.Minute
)

// NewScheduler 构建调度器。
func NewScheduler(options Options) *Scheduler {
	if options.Clock == nil {
		options.Clock = SystemClock{}
	}
	if options.Random == nil {
		options.Random = rand.Float64
	}
	// 任务根 ctx 与 stopCh 同生命周期：构造一次，停机时在 stopOnce 内取消。
	stopBoundCtx, stopBoundCancel := context.WithCancel(context.Background())
	scheduler := &Scheduler{
		clock:           options.Clock,
		random:          options.Random,
		stableSeed:      options.StableSeed,
		logger:          options.Logger,
		jobs:            map[string]*jobState{},
		lanes:           map[string]*laneState{},
		stopCh:          make(chan struct{}),
		stopBoundCtx:    stopBoundCtx,
		stopBoundCancel: stopBoundCancel,
	}
	go scheduler.stuckWatchLoop()
	return scheduler
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
	// stopCh 关闭与任务根 ctx 取消必须在同一 stopOnce 闭包内原子执行，
	// 避免停机传播出现只关通道不取消 ctx 的窗口。
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.stopBoundCancel()
	})
	s.mu.Lock()
	s.stopped = true
	s.jobs = map[string]*jobState{}
	s.lanes = map[string]*laneState{}
	s.mu.Unlock()
}

// StopAndDrain 停止调度并等待活跃任务结束；超时未排空返回 drained=false 与
// 仍在运行的约数（对齐 Node stopAndDrain）。
func (s *Scheduler) StopAndDrain(timeout time.Duration) (drained bool, activeCount int) {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.stopBoundCancel()
	})
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
	defer safego.Recover("jobsched.scheduler.jobLoop")
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
					// 缺陷修复：skip 直接 continue 会跳过收尾的 armPostRun；
					// fixedDelay 的触发目标已在 fire 时一次性清空，不重建会让
					// nextTarget 双空、jobLoop return（任务静默死亡）。
					// BUG-0223 补注：skip-continue 路径一律必须 rearm，原因
					// 同下——本分支与 inBackoff 分支都不经过 armPostRun 收尾。
					s.rearmAfterRegularSkip(job, now)
					continue
				}
			}
			if s.inBackoff(job, now) {
				s.recordSkip(job, now, "failure_backoff")
				// BUG-0223 防御收口：skip-continue 不经过 armPostRun 的收尾
				// 重排。当前靠「backoff 活跃 ⇒ 失败收尾的 armPostRun 已布防
				// deferredAt」隐式不变量续命，严密但脆弱（无注释无测试）；
				// 此处显式补一行幂等 rearm（目标存在则不覆盖，见
				// rearmAfterRegularSkip 守卫），不再依赖该隐式不变量。
				s.rearmAfterRegularSkip(job, now)
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
		// W2：lane 忙此前静默 return，Debug 留痕补齐行级定位。
		if s.logger != nil {
			s.logger.Debug("jobsched_lane_busy", "job", job.spec.Name, "lane", job.spec.Lane)
		}
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
	// 交接再入：releaseLane 交接时已把 runningJob 置为队首任务名再唤醒它，
	// 被唤醒任务的 fire 必须认领自己的名字，否则会把自身重新排队且永不
	// 释放——lane 自死锁，同 lane 全部任务永久 resource_lane_busy
	// （2026-09-28 生产：external-account-maintenance 与 stats-online 双双
	// 在重启后首轮交接即冻结）。
	if lane.runningJob == "" || lane.runningJob == job.spec.Name {
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

// runCompletion 承载一轮任务执行的终态样本：handler goroutine 经 buffered
// channel 恰好投递一次；超时强制回收后主路径不再读取，迟到结果天然只被
// 丢弃一次（不二次记账/二次释放/二次日志）。
type runCompletion struct {
	result   TaskResult
	runErr   error
	panicked any
	ctxErr   error
}

// runOnce 执行一轮任务：handler 放入独立 goroutine，主路径等待其完成或
// ctx 到期。超时是强制回收而非建议性提示——Timeout 只是 taskCtx 取消，
// handler 忽略 ctx 卡死时本函数按 timeout 记账并立即返回（fire 随后释放
// lane、job.running 复位，下一轮可正常调度），泄漏的 handler goroutine
// 迟到结果静默丢弃。否则 handler 永不返回 = lane 永久占死（生产
// 2026-09-27 external-account-maintenance 停摆 9 小时的根因）。
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

	// 两层 ctx 各自持有 cancel（BUG-0220）：Timeout>0 时 WithTimeout 层覆盖
	// cancel 引用，WithCancel 层的 taskCancel 不得随之丢失——该层不取消会让
	// ctx 节点作为停机根的 child 永久滞留。两个 cancel 均幂等，由 handler
	// 收尾的 defer 一并调用。
	taskCtx, taskCancel := context.WithCancel(s.contextBoundToStop())
	cancel := taskCancel
	var deadline *time.Time
	if spec.Timeout > 0 {
		taskCtx, cancel = context.WithTimeout(taskCtx, spec.Timeout)
		deadlineValue := startedAt.Add(spec.Timeout)
		deadline = &deadlineValue
	}

	s.runWG.Add(1)
	s.runActive.Add(1)
	completions := make(chan runCompletion, 1)
	go func() {
		var completion runCompletion
		// panicked 非 nil 表示本轮以 panic 收场（缺陷修复：任务级 recover
		// 隔离）。Handle 直接作为 defer 调用（recover 在其自身帧内，见
		// safego 包注释）；注册在首位使其最后执行，err 赋值为最终值。
		defer safego.Handle("jobsched.scheduler.task", func(recovered any) {
			completion.panicked = recovered
			completion.runErr = fmt.Errorf("后台任务 panic：%v", recovered)
			completions <- completion
		})
		defer s.runWG.Done()
		defer s.runActive.Add(-1)
		// 先取样 ctx 状态再 cancel：cancel 本身会把 Err 变成 Canceled，
		// 不能作为停机/超时的判定依据。两层 cancel 都调用（幂等）。
		defer func() { completion.ctxErr = taskCtx.Err(); cancel(); taskCancel() }()
		completion.result, completion.runErr = spec.Task(taskCtx, TaskContext{
			ScheduledAt: scheduledAt,
			StartedAt:   startedAt,
			DeadlineAt:  deadline,
		})
		completions <- completion
	}()

	select {
	case completion := <-completions:
		// handler 先完成：走既有成功/失败/partial/timeout 记账与日志路径，
		// 语义不变。
		s.finishRun(job, completion, startedAt, spec.Timeout > 0 && completion.ctxErr == context.DeadlineExceeded)
	case <-taskCtx.Done():
		if spec.Timeout > 0 && taskCtx.Err() == context.DeadlineExceeded {
			// 超时先到：立即按 timeout 记账并交还调度循环（lane 由 fire 的
			// releaseLane 释放），handler 迟到结果交由泄漏 watcher 丢弃。
			s.finishRun(job, runCompletion{}, startedAt, true)
			s.trackLeakedHandler(job, startedAt, completions)
			return
		}
		// 调度器停机（parent ctx 取消）：保持既有行为——等待 handler 返回
		// 后按 scheduler_stopped 记账，不强记 timeout。handler 卡死时 job
		// 已被 Stop 移出调度、快照与 lane 表，不再占用资源。
		completion := <-completions
		s.finishRun(job, completion, startedAt, spec.Timeout > 0 && completion.ctxErr == context.DeadlineExceeded)
	}
}

// finishRun 落一轮的终态记账与逐轮日志。timedOut 为 true 表示超时：或为
// ctx 到期后的强制回收（completion 为零值），或为 handler 返回时其 ctx 已
// 因 deadline 结束（completion.ctxErr == DeadlineExceeded）。
func (s *Scheduler) finishRun(job *jobState, completion runCompletion, startedAt time.Time, timedOut bool) {
	spec := job.spec
	finishedAt := s.clock.Now()
	duration := finishedAt.Sub(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	stoppedRun := !timedOut && (completion.ctxErr == context.Canceled || s.isStopped())

	// W2 逐轮 outcome 日志的锁内快照：日志在记账完成后锁外输出，避免持锁
	// 调用 handler；nil logger 时提前返回，不构造任何日志参数。
	var (
		logConsecFail   int64
		logBackoffUntil *time.Time
		logWarning      string
		logSkipReason   string
	)
	s.mu.Lock()
	job.running = false
	job.runningSince = nil
	job.lastFinishedAt = &finishedAt
	job.lastDurationMS = duration
	if job.maxDurationMS == 0 || duration > job.maxDurationMS {
		job.maxDurationMS = duration
	}
	switch {
	// panic 轮（含停机/超时窗口内的 panic）按失败记账（落入 runErr 分支，
	// 对齐非停机 panic）：panic 是任务代码缺陷，不得因停机被记成 skipped。
	case stoppedRun && completion.panicked == nil:
		job.taskSkip++
		job.lastOutcome = OutcomeSkipped
		job.lastSkipAt = &finishedAt
		job.lastSkipReason = "scheduler_stopped"
	case timedOut && completion.panicked == nil:
		job.failure++
		job.timedOut++
		job.consecFail++
		job.lastOutcome = OutcomeSkipped
		job.lastSkipAt = &finishedAt
		job.lastSkipReason = "timeout"
		job.lastErrorAt = &finishedAt
		job.lastError = "后台任务执行超时"
		job.backoffUntil = s.backoffTargetLocked(job, job.consecFail, finishedAt)
		logConsecFail = job.consecFail
		logBackoffUntil = job.backoffUntil
	case completion.runErr != nil:
		job.failure++
		job.consecFail++
		job.lastOutcome = OutcomeSkipped
		job.lastErrorAt = &finishedAt
		job.lastError = completion.runErr.Error()
		job.backoffUntil = s.backoffTargetLocked(job, job.consecFail, finishedAt)
		logConsecFail = job.consecFail
		logBackoffUntil = job.backoffUntil
	default:
		result := completion.result
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
			logWarning = job.lastWarning
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
			logSkipReason = job.lastSkipReason
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

	if s.logger == nil {
		return
	}
	// W2：逐轮 outcome 日志（锁外、每轮恰好一条）。级别策略：timeout /
	// runErr / partial 失败本体打 Warn 保证可归因；task skipped、停机与成功
	// 打 Debug，防止高频任务的正常轮刷屏。
	switch {
	case stoppedRun && completion.panicked == nil:
		s.logger.Debug("jobsched_run_stopped", "job", spec.Name, "skipReason", "scheduler_stopped")
	case completion.panicked != nil:
		// 缺陷修复：panic 单列 Error 日志（event 含任务名与 panic 值），级别
		// 高于普通失败的 Warn——panic 是任务代码缺陷，需要被巡检直接发现。
		attrs := []any{"job", spec.Name, "panic", fmt.Sprintf("%v", completion.panicked), "durationMs", duration, "consecFail", logConsecFail}
		if logBackoffUntil != nil {
			attrs = append(attrs, "nextRetryAt", *logBackoffUntil)
		}
		s.logger.Error("jobsched_run_panicked", attrs...)
	case timedOut:
		attrs := []any{"job", spec.Name, "durationMs", duration, "consecFail", logConsecFail}
		if logBackoffUntil != nil {
			attrs = append(attrs, "nextRetryAt", *logBackoffUntil)
		}
		s.logger.Warn("jobsched_run_timeout", attrs...)
	case completion.runErr != nil:
		attrs := []any{"job", spec.Name, "error", completion.runErr.Error(), "durationMs", duration, "consecFail", logConsecFail}
		if logBackoffUntil != nil {
			attrs = append(attrs, "backoffMs", logBackoffUntil.Sub(finishedAt).Milliseconds())
		}
		s.logger.Warn("jobsched_run_failed", attrs...)
	case completion.result.Outcome == OutcomePartial:
		s.logger.Warn("jobsched_run_partial", "job", spec.Name, "warning", logWarning)
	case completion.result.Outcome == OutcomeSkipped:
		s.logger.Debug("jobsched_run_skipped", "job", spec.Name, "skipReason", logSkipReason)
	default:
		s.logger.Debug("jobsched_run_success", "job", spec.Name, "durationMs", duration)
	}
}

// trackLeakedHandler 登记超时后仍在跑的泄漏 run（供 jobsched_run_stuck
// 周期观测），并起一个轻量 watcher：handler 迟到返回时清理登记、打一条
// Info（含 job 与迟到时长）便于观测。watcher 不进入 runWG——StopAndDrain
// 的等待语义仍由 handler 自身的计数承载。
func (s *Scheduler) trackLeakedHandler(job *jobState, startedAt time.Time, completions <-chan runCompletion) {
	s.mu.Lock()
	leakStartedAt := startedAt
	job.leakStartedAt = &leakStartedAt
	s.mu.Unlock()
	leakedAt := s.clock.Now()
	go func() {
		<-completions
		delay := s.clock.Now().Sub(leakedAt)
		s.mu.Lock()
		job.leakStartedAt = nil
		job.stuckLogNext = nil
		job.stuckLogInterval = 0
		s.mu.Unlock()
		if s.logger != nil {
			s.logger.Info("jobsched_run_leaked_finished", "job", job.spec.Name, "delayMs", delay.Milliseconds())
		}
	}()
}

// stuckWatchLoop 周期扫描超时后仍在跑的泄漏 run：对超过 Timeout +
// stuckGrace 仍未返回的打 Error jobsched_run_stuck（job、runningSince、
// 超时时长），之后按指数间隔重复，防止日志洪水。停机退出。
func (s *Scheduler) stuckWatchLoop() {
	for {
		timer := s.clock.NewTimer(stuckWatchInterval)
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-timer.C():
		}
		s.logStuckRuns()
	}
}

// logStuckRuns 扫描全部 job 并对越限的泄漏 run 打 stuck 日志（锁内记账、
// 锁外输出）。首次触发在 startedAt + Timeout + stuckGrace 之后，重复间隔
// 自 stuckLogRepeat 起 ×2 指数增长、封顶 stuckLogRepeatMax。
func (s *Scheduler) logStuckRuns() {
	if s.logger == nil {
		return
	}
	now := s.clock.Now()
	type stuckRun struct {
		name         string
		runningSince time.Time
		overdueMS    int64
	}
	var stuck []stuckRun
	s.mu.Lock()
	for _, job := range s.jobs {
		if job.leakStartedAt == nil {
			continue
		}
		overdue := now.Sub(job.leakStartedAt.Add(job.spec.Timeout + stuckGrace))
		if overdue <= 0 {
			continue
		}
		if job.stuckLogNext != nil && now.Before(*job.stuckLogNext) {
			continue
		}
		stuck = append(stuck, stuckRun{
			name:         job.spec.Name,
			runningSince: *job.leakStartedAt,
			overdueMS:    overdue.Milliseconds(),
		})
		interval := job.stuckLogInterval * 2
		if interval < stuckLogRepeat {
			interval = stuckLogRepeat
		}
		if interval > stuckLogRepeatMax {
			interval = stuckLogRepeatMax
		}
		next := now.Add(interval)
		job.stuckLogInterval = interval
		job.stuckLogNext = &next
	}
	s.mu.Unlock()
	for _, run := range stuck {
		s.logger.Error("jobsched_run_stuck",
			"job", run.name, "runningSince", run.runningSince, "overdueMs", run.overdueMS)
	}
}

// rearmAfterRegularSkip 在错过间隔的 skip-continue 路径上保证下一轮触发目标
// 仍存在（skip 不经过 armPostRun 的收尾重排）。只对 fixedDelay 生效：其触发
// 目标一次性（fire 即清空、由收尾重排重建），skip 路径必须补上等价重建；
// fixedRate 的锚点在 fire 时已推进，重建反而会多挪一个间隔，故不动。目标或
// 退避重试仍存在时不覆盖。保证任何策略组合下 nextTarget 不双空。
func (s *Scheduler) rearmAfterRegularSkip(job *jobState, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.spec.ScheduleMode != ScheduleModeFixedDelay {
		return
	}
	if job.fixedRateNext != nil || job.deferredAt != nil {
		return
	}
	next := now.Add(passiveIntervalDelay(job.spec.Interval, job.spec.PassiveJitter, s.random))
	job.fixedRateNext = &next
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
	// BUG-0223：pending 当前无置位路径（见 jobState.pending 注释），本分支
	// 实际不可达；保留以维持与 Node finally 补跑语义的形状对应——若未来
	// 恢复置位（补跑标记），此处的立即重试逻辑仍然正确。
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

// contextBoundToStop 返回绑定停机信号的任务根 ctx：Scheduler 级单次构造
// （NewScheduler），Stop/StopAndDrain 时随 stopCh 关闭一并取消。保留方法做
// 单一入口；不得改回每轮新建 ctx + 监听 goroutine 的实现——每轮调用一次就
// 泄漏 1 个 goroutine，长驻进程无界累积（BUG-0220）。
func (s *Scheduler) contextBoundToStop() context.Context {
	return s.stopBoundCtx
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
	job.skipped++
	stamp := now
	job.lastSkipAt = &stamp
	job.lastSkipReason = reason
	job.lastOutcome = OutcomeSkipped
	s.mu.Unlock()
	// W2：退避期跳过只打 Debug（失败本体已有 Warn，退避期每轮 Warn 会刷屏，
	// 如 3s 任务 × 5min 退避 = 100 条）；"running" overlap 策略属正常路径，
	// 只靠既有 skipped 计数。
	if reason == "failure_backoff" && s.logger != nil {
		s.logger.Debug("jobsched_run_backoff_skip", "job", job.spec.Name)
	}
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
