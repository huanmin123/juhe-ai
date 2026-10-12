package chat

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// Assistant timeline + generation runner ported from chat-assistant-timeline.ts
// and chat-generation-runner.ts. Event types, projection budgets, timeline
// status transitions and terminal event payloads mirror Node byte for byte;
// the only structural change is context.Context instead of AbortController.

const (
	chatGenerationTextMaxBytes      = 192 * 1024
	chatGenerationReasoningMaxBytes = 192 * 1024
	chatGenerationToolJSONMaxBytes  = 192 * 1024
	maxTimelineTotalBytes           = chatGenerationTextMaxBytes + chatGenerationReasoningMaxBytes + chatGenerationToolJSONMaxBytes + 64*1024
)

// assistant process/tool status unions (chat-assistant-timeline.ts).
const (
	asstStarted   = "started"
	asstCompleted = "completed"
	asstFailed    = "failed"
	asstCanceled  = "canceled"
	asstUpdated   = "updated"
)

func isTerminalProcessStatus(status string) bool {
	return status == asstCompleted || status == asstFailed || status == asstCanceled
}

// assistantBlock mirrors AssistantContentBlock. Field order matches the Node
// object literals so persisted JSON keeps the same shape. M7 媒体块字段
//（问答音视频工具设计 §3）：JobID/MediaKind/Progress/Model/PromptSummary/
// MediaError 是 output_media_task 的任务面字段（status 为 media_jobs 任务状态
// 词表）。
type assistantBlock struct {
	Type          string         `json:"type"`
	BlockID       string         `json:"blockId,omitempty"`
	Order         int64          `json:"order,omitempty"`
	Text          string         `json:"text,omitempty"`
	Status        string         `json:"status,omitempty"`
	CallID        string         `json:"callId,omitempty"`
	ToolType      string         `json:"toolType,omitempty"`
	Item          map[string]any `json:"item,omitempty"`
	AssetID       string         `json:"assetId,omitempty"`
	MimeType      string         `json:"mimeType,omitempty"`
	Width         *int64         `json:"width,omitempty"`
	Height        *int64         `json:"height,omitempty"`
	RevisedPrompt string         `json:"revisedPrompt,omitempty"`
	JobID         string         `json:"jobId,omitempty"`
	MediaKind     string         `json:"kind,omitempty"`
	Progress      *int64         `json:"progress,omitempty"`
	Model         string         `json:"model,omitempty"`
	PromptSummary string         `json:"promptSummary,omitempty"`
	MediaError    string         `json:"error,omitempty"`
}

func cloneAssistantBlock(block *assistantBlock) *assistantBlock {
	cloned := *block
	if block.Item != nil {
		cloned.Item = cloneJSONMap(block.Item)
	}
	return &cloned
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	// Round-trip through JSON for deep-clone parity with Node cloneJsonObject.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

type assistantTimelineSnapshot struct {
	Status        string
	ContentText   string
	ContentBlocks []*assistantBlock
}

// assistantTimeline mirrors AssistantTimeline.
type assistantTimeline struct {
	blocks []*assistantBlock
	status string
}

func newAssistantTimeline() *assistantTimeline {
	return &assistantTimeline{status: asstStarted}
}

func (t *assistantTimeline) ensureMutable() error {
	if t.status != asstStarted {
		return errors.New("助手时间线已进入终态")
	}
	return nil
}

func (t *assistantTimeline) nextBlockID() string {
	return "assistant_block_" + itoa(len(t.blocks)+1)
}

// AppendText mirrors appendText.
func (t *assistantTimeline) AppendText(text string) *assistantBlock {
	if err := t.ensureMutable(); err != nil {
		return nil
	}
	if text == "" {
		return nil
	}
	if last := t.lastBlock(); last != nil && last.Type == "output_text" {
		last.Text += text
		return cloneAssistantBlock(last)
	}
	block := &assistantBlock{Type: "output_text", BlockID: t.nextBlockID(), Order: int64(len(t.blocks) + 1), Text: text}
	t.blocks = append(t.blocks, block)
	return cloneAssistantBlock(block)
}

// AppendReasoning mirrors appendReasoning.
func (t *assistantTimeline) AppendReasoning(text string) *assistantBlock {
	if err := t.ensureMutable(); err != nil {
		return nil
	}
	if text == "" {
		return nil
	}
	if last := t.lastBlock(); last != nil && last.Type == "reasoning" && last.Status == asstStarted {
		last.Text += text
		return cloneAssistantBlock(last)
	}
	block := &assistantBlock{Type: "reasoning", BlockID: t.nextBlockID(), Order: int64(len(t.blocks) + 1), Text: text, Status: asstStarted}
	t.blocks = append(t.blocks, block)
	return cloneAssistantBlock(block)
}

func (t *assistantTimeline) lastBlock() *assistantBlock {
	if len(t.blocks) == 0 {
		return nil
	}
	return t.blocks[len(t.blocks)-1]
}

// CompleteBlock mirrors completeBlock.
func (t *assistantTimeline) CompleteBlock(blockID string) (*assistantBlock, error) {
	if err := t.ensureMutable(); err != nil {
		return nil, err
	}
	for _, block := range t.blocks {
		if block.BlockID != blockID {
			continue
		}
		if block.Type == "reasoning" && block.Status == asstStarted {
			block.Status = asstCompleted
		}
		return cloneAssistantBlock(block), nil
	}
	return nil, errors.New("未知的助手内容块: " + blockID)
}

// Finalize mirrors finalize.
func (t *assistantTimeline) Finalize(status string) assistantTimelineSnapshot {
	if t.status != asstStarted {
		return t.Snapshot()
	}
	for _, block := range t.blocks {
		if block.Type == "reasoning" && block.Status == asstStarted {
			block.Status = status
		}
		if block.Type == "tool_call" && (block.Status == asstStarted || block.Status == asstUpdated) {
			block.Status = status
		}
		// output_image / output_audio / output_media_task 等生成媒体块已随
		// 站内工具体系退场，不再有新建写入路径；存量块由读取侧占位降级。
	}
	t.status = status
	return t.Snapshot()
}

// Snapshot mirrors snapshot.
func (t *assistantTimeline) Snapshot() assistantTimelineSnapshot {
	blocks := make([]*assistantBlock, 0, len(t.blocks))
	contentText := ""
	texts := make([]*assistantBlock, 0, len(t.blocks))
	for _, block := range t.blocks {
		blocks = append(blocks, cloneAssistantBlock(block))
		if block.Type == "output_text" {
			texts = append(texts, block)
		}
	}
	for _, block := range texts {
		contentText += block.Text
	}
	return assistantTimelineSnapshot{Status: t.status, ContentText: contentText, ContentBlocks: blocks}
}

// --- runner ---

// ChatGenerationIdentity mirrors ChatGenerationIdentity.
type ChatGenerationIdentity struct {
	OwnerID            string
	ConversationID     string
	TurnID             string
	AssistantMessageID string
}

// ChatGenerationEvent mirrors ChatGenerationEvent.
type ChatGenerationEvent struct {
	Type         string
	EventVersion int64
	Data         map[string]any
}

// ChatGenerationProjectionUpdate mirrors ChatGenerationProjectionUpdate.
type ChatGenerationProjectionUpdate struct {
	ContentTextDelta   *string
	ReasoningTextDelta *string
	ReasoningCompleted bool
}

func (u ChatGenerationProjectionUpdate) hasUpdate() bool {
	return (u.ContentTextDelta != nil && *u.ContentTextDelta != "") ||
		(u.ReasoningTextDelta != nil && *u.ReasoningTextDelta != "") ||
		u.ReasoningCompleted
}

// ChatGenerationSubscriber mirrors ChatGenerationSubscriber.
type ChatGenerationSubscriber interface {
	TrySend(event ChatGenerationEvent) bool
}

// ChatGenerationTerminalResult mirrors ChatGenerationTerminalResult.
type ChatGenerationTerminalResult struct {
	Status string // completed|failed|canceled
	Data   map[string]any
}

// ChatGenerationStatusSnapshot mirrors ChatGenerationStatusSnapshot.
type ChatGenerationStatusSnapshot struct {
	State                  string // running|terminal
	EventVersion           int64
	LastSemanticActivityAt string
	AssistantMessageID     string
}

// ChatGenerationRunnerOptions mirrors ChatGenerationRunnerOptions.
type ChatGenerationRunnerOptions struct {
	Identity               ChatGenerationIdentity
	Execute                func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error)
	OnUnexpectedError      func(publicError PublicChatGenerationError) error
	UnexpectedErrorTraceID string
	Now                    func() string
}

// ChatGenerationExecutionContext mirrors ChatGenerationExecutionContext; the
// Context carries the abort signal (Node AbortSignal) and SnapshotBlocks
// mirrors runner.snapshotContentBlocks().
type ChatGenerationExecutionContext struct {
	Context        context.Context
	Publish        func(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool
	Aborted        func() bool
	SnapshotBlocks func() []*assistantBlock
}

type runnerSubscription struct {
	subscriber       ChatGenerationSubscriber
	deliveredVersion int64
}

// ChatGenerationRunner mirrors ChatGenerationRunner.
type ChatGenerationRunner struct {
	Identity ChatGenerationIdentity

	ctx               context.Context
	mu                sync.Mutex
	cancel            contextCancelFunc
	cancelled         contextCancelledFunc
	subscribers       []*runnerSubscription
	timeline          *assistantTimeline
	eventVersion      int64
	lastSemanticActAt string
	started           bool
	authoritativeTerm bool
	currentState      string

	execute                func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error)
	onUnexpectedError      func(PublicChatGenerationError) error
	unexpectedErrorTraceID string
	now                    func() string
	completion             chan struct{}
	completionOnce         sync.Once
}

// contextCancelFunc abstracts context cancellation so tests can run without a
// real context tree when desired.
type contextCancelFunc = func()
type contextCancelledFunc = func() bool

// NewChatGenerationRunner builds a runner. cancel/cancelled implement the
// AbortController pair (use context.WithCancel).
func NewChatGenerationRunner(options ChatGenerationRunnerOptions, ctx context.Context, cancel contextCancelFunc, cancelled contextCancelledFunc) *ChatGenerationRunner {
	now := options.Now
	if now == nil {
		now = func() string { return isoMillis(time.Now()) }
	}
	return &ChatGenerationRunner{
		Identity:               options.Identity,
		ctx:                    ctx,
		cancel:                 cancel,
		cancelled:              cancelled,
		timeline:               newAssistantTimeline(),
		currentState:           "pending",
		lastSemanticActAt:      now(),
		execute:                options.Execute,
		onUnexpectedError:      options.OnUnexpectedError,
		unexpectedErrorTraceID: options.UnexpectedErrorTraceID,
		now:                    now,
		completion:             make(chan struct{}),
	}
}

func nowWallclock() time.Time { return time.Now() }

// State returns the runner state.
func (r *ChatGenerationRunner) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentState
}

// Terminal reports terminal state.
func (r *ChatGenerationRunner) Terminal() bool {
	return isTerminalProcessStatus(r.State())
}

// AuthoritativeTerminal mirrors authoritativeTerminal.
func (r *ChatGenerationRunner) AuthoritativeTerminal() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.authoritativeTerm
}

// Completion resolves when the run settles.
func (r *ChatGenerationRunner) Completion() <-chan struct{} { return r.completion }

// Wait blocks until the run settles.
func (r *ChatGenerationRunner) Wait() { <-r.completion }

// SnapshotContentBlocks mirrors snapshotContentBlocks.
func (r *ChatGenerationRunner) SnapshotContentBlocks() []*assistantBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.timeline.Snapshot().ContentBlocks
}

// StatusSnapshot mirrors statusSnapshot.
func (r *ChatGenerationRunner) StatusSnapshot() ChatGenerationStatusSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := "running"
	if isTerminalProcessStatus(r.currentState) {
		state = "terminal"
	}
	return ChatGenerationStatusSnapshot{
		State:                  state,
		EventVersion:           r.eventVersion,
		LastSemanticActivityAt: r.lastSemanticActAt,
		AssistantMessageID:     r.Identity.AssistantMessageID,
	}
}

// Start mirrors start; onSettled fires after the run settles.
func (r *ChatGenerationRunner) Start(onSettled func()) bool {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return false
	}
	r.started = true
	r.currentState = "running"
	r.mu.Unlock()
	go r.run(onSettled)
	return true
}

// Publish mirrors publish.
func (r *ChatGenerationRunner) Publish(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentState != "running" {
		return false
	}
	if !update.hasUpdate() {
		return r.emitEventLocked(eventType, data)
	}
	r.applyProjectionUpdateLocked(update)
	return true
}

// Subscribe mirrors subscribe; delivers a message.snapshot first.
func (r *ChatGenerationRunner) Subscribe(subscriber ChatGenerationSubscriber) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, registration := range r.subscribers {
		if registration.subscriber == subscriber {
			return true
		}
	}
	registration := &runnerSubscription{subscriber: subscriber, deliveredVersion: r.eventVersion}
	snapshot := ChatGenerationEvent{
		Type:         "message.snapshot",
		EventVersion: r.eventVersion,
		Data: map[string]any{
			"turnId":    r.Identity.TurnID,
			"assistant": r.snapshotAssistantLocked(),
		},
	}
	if !r.trySendLocked(registration, snapshot) {
		return false
	}
	r.subscribers = append(r.subscribers, registration)
	return true
}

// Unsubscribe mirrors unsubscribe. Node detaches from a single-threaded
// event loop; here a terminal-event dispatch holds r.mu while the detached
// subscriber's callback re-enters, so contention resolves to a no-op: the
// in-flight dispatch already prunes failed subscribers.
func (r *ChatGenerationRunner) Unsubscribe(subscriber ChatGenerationSubscriber) bool {
	if !r.mu.TryLock() {
		return true
	}
	defer r.mu.Unlock()
	for index, registration := range r.subscribers {
		if registration.subscriber == subscriber {
			r.subscribers = append(r.subscribers[:index], r.subscribers[index+1:]...)
			return true
		}
	}
	return false
}

// Abort mirrors abort.
func (r *ChatGenerationRunner) Abort() bool {
	r.mu.Lock()
	terminal := isTerminalProcessStatus(r.currentState)
	r.mu.Unlock()
	if terminal || r.cancelled() {
		return false
	}
	r.cancel()
	return true
}

func (r *ChatGenerationRunner) aborted() bool { return r.cancelled() }

func (r *ChatGenerationRunner) run(onSettled func()) {
	// BUG-0246：panic 兜底也必须收尾。原 defer safego.Recover 只记日志，
	// execute 闭包或收尾段 panic 时 completion 永不 close、hub 槽位永不
	// 释放。改为 Handle：recover 后补 finish（close completion + onSettled
	// 释放槽位）。正常路径的 finish 在 settle 内先于 return 调用，而 panic
	// 只能发生在 settle 的 finish 之前（finish 后立即 return，defer 时
	// recover()==nil），onSettled 不会双触发；finish 内 onSettled 另有
	// recover 防护、completionOnce 防 double close。
	defer safego.Handle("chat.generation_runner.execute", func(recovered any) {
		r.finish(onSettled)
	})
	result, err := r.execute(&ChatGenerationExecutionContext{
		Context:        r.ctx,
		Aborted:        r.aborted,
		Publish:        r.Publish,
		SnapshotBlocks: r.SnapshotContentBlocks,
	})
	r.settle(result, err, onSettled)
}

// settle 是 execute 返回后的收尾判定（原 run 主体）：终态投影、状态转移、
// 终态事件发射与 finish。锁不再跨外部回调持有——onUnexpectedError 在无锁
// 状态执行，持锁判定段各自 defer Unlock，任一段 panic 展开后锁均已释放，
// run 的 panic 兜底才能安全补 finish（onSettled 里 hub 会再锁 runner.mu
// 记终态快照，持锁展开会把 runner 锁死成永久占用）。currentState 的运行
// 期写者只有本 goroutine（Start 先于 run 置 running），分段加锁与原单段
// 持锁的判定结果等价。
func (r *ChatGenerationRunner) settle(result ChatGenerationTerminalResult, err error, onSettled func()) {
	r.mu.Lock()
	terminal := isTerminalProcessStatus(r.currentState)
	r.mu.Unlock()
	if terminal {
		r.finish(onSettled)
		return
	}
	if err == nil {
		r.applySuccess(result)
		r.finish(onSettled)
		return
	}
	publicError := ClassifyUnknownChatGenerationError(err)
	authoritativeFailure := false
	if r.onUnexpectedError != nil {
		finalizerErr := r.onUnexpectedError(publicError)
		if finalizerErr == nil {
			authoritativeFailure = true
		}
	}
	r.applyFailure(publicError, authoritativeFailure)
	r.finish(onSettled)
}

// applySuccess 持锁应用成功结果；defer Unlock 保证本段 panic 展开后锁立即可用。
func (r *ChatGenerationRunner) applySuccess(result ChatGenerationTerminalResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if isTerminalProcessStatus(r.currentState) {
		return
	}
	r.finalizeTimelineLocked(result.Status)
	r.currentState = result.Status
	r.authoritativeTerm = true
	r.emitEventLocked("message."+result.Status, result.Data)
}

// applyFailure 持锁应用失败路径的终态化与 message.failed 事件；defer Unlock
// 保证本段 panic 展开后锁立即可用。
func (r *ChatGenerationRunner) applyFailure(publicError PublicChatGenerationError, authoritativeFailure bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if isTerminalProcessStatus(r.currentState) {
		return
	}
	if authoritativeFailure {
		r.finalizeTimelineLocked(asstFailed)
		r.currentState = asstFailed
		r.authoritativeTerm = true
		data := map[string]any{
			"messageId": r.Identity.AssistantMessageID,
			"code":      string(publicError.Code),
			"message":   publicError.Message,
		}
		if r.unexpectedErrorTraceID != "" {
			data["traceId"] = r.unexpectedErrorTraceID
		}
		r.emitEventLocked("message.failed", data)
		return
	}
	r.timeline.Finalize(asstFailed)
	r.currentState = asstFailed
}

func (r *ChatGenerationRunner) finish(onSettled func()) {
	if onSettled != nil {
		func() {
			defer func() { _ = recover() }()
			onSettled()
		}()
	}
	r.completionOnce.Do(func() { close(r.completion) })
}

func (r *ChatGenerationRunner) emitEventLocked(eventType string, data map[string]any) bool {
	r.eventVersion++
	r.lastSemanticActAt = r.now()
	event := ChatGenerationEvent{Type: eventType, EventVersion: r.eventVersion, Data: data}
	survivors := r.subscribers[:0]
	for _, registration := range r.subscribers {
		if event.EventVersion <= registration.deliveredVersion {
			survivors = append(survivors, registration)
			continue
		}
		if r.trySendLocked(registration, event) {
			survivors = append(survivors, registration)
		}
	}
	r.subscribers = survivors
	return true
}

func (r *ChatGenerationRunner) trySendLocked(registration *runnerSubscription, event ChatGenerationEvent) bool {
	delivered := func() (delivered bool) {
		defer func() {
			if recover() != nil {
				delivered = false
			}
		}()
		return registration.subscriber.TrySend(event)
	}()
	if delivered {
		registration.deliveredVersion = event.EventVersion
	}
	return delivered
}

func (r *ChatGenerationRunner) applyProjectionUpdateLocked(update ChatGenerationProjectionUpdate) {
	if update.ContentTextDelta != nil && *update.ContentTextDelta != "" {
		r.appendTextLocked(*update.ContentTextDelta)
	}
	if update.ReasoningTextDelta != nil && *update.ReasoningTextDelta != "" {
		r.appendReasoningLocked(*update.ReasoningTextDelta)
	}
	if update.ReasoningCompleted {
		r.completeReasoningLocked()
	}
}

func remainingTextBytes(blocks []*assistantBlock, blockType string, maxBytes int) int {
	total := 0
	for _, block := range blocks {
		if block.Type == blockType {
			total += len(block.Text)
		}
	}
	remaining := maxBytes - total
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (r *ChatGenerationRunner) appendTextLocked(input string) {
	snapshot := r.timeline.Snapshot()
	delta := truncateUTF8(input, remainingTextBytes(snapshot.ContentBlocks, "output_text", chatGenerationTextMaxBytes))
	if delta == "" {
		return
	}
	last := lastOf(snapshot.ContentBlocks)
	candidate := &assistantBlock{Type: "output_text", BlockID: "assistant_block_" + itoa(len(snapshot.ContentBlocks)+1), Order: int64(len(snapshot.ContentBlocks) + 1), Text: delta}
	if (last == nil || last.Type != "output_text") && !canAppendBlock(snapshot.ContentBlocks, candidate) {
		return
	}
	block := r.timeline.AppendText(delta)
	if block == nil {
		return
	}
	if last != nil && last.Type == "output_text" && last.BlockID == block.BlockID {
		r.emitEventLocked("content_block.delta", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "delta": delta})
		return
	}
	r.emitEventLocked("content_block.started", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
}

func (r *ChatGenerationRunner) appendReasoningLocked(input string) {
	snapshot := r.timeline.Snapshot()
	delta := truncateUTF8(input, remainingTextBytes(snapshot.ContentBlocks, "reasoning", chatGenerationReasoningMaxBytes))
	if delta == "" {
		return
	}
	last := lastOf(snapshot.ContentBlocks)
	activeReasoning := last != nil && last.Type == "reasoning" && last.Status == asstStarted
	if !activeReasoning {
		candidate := &assistantBlock{Type: "reasoning", BlockID: "assistant_block_" + itoa(len(snapshot.ContentBlocks)+1), Order: int64(len(snapshot.ContentBlocks) + 1), Text: delta, Status: asstStarted}
		if !canAppendBlock(snapshot.ContentBlocks, candidate) {
			return
		}
	}
	block := r.timeline.AppendReasoning(delta)
	if block == nil {
		return
	}
	if activeReasoning && last.BlockID == block.BlockID {
		r.emitEventLocked("content_block.delta", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "delta": delta})
		return
	}
	r.emitEventLocked("content_block.started", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
}

func (r *ChatGenerationRunner) completeReasoningLocked() {
	for _, block := range r.timeline.Snapshot().ContentBlocks {
		if block.Type == "reasoning" && block.Status == asstStarted {
			completed, err := r.timeline.CompleteBlock(block.BlockID)
			if err == nil && completed != nil {
				r.emitEventLocked("content_block.completed", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(completed)})
			}
			return
		}
	}
}

// jsonRawBlock renders a block through map[string]any so map data payloads
// serialize with identical JSON as Node object literals.
func jsonRawBlock(block *assistantBlock) map[string]any {
	raw, err := json.Marshal(block)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (r *ChatGenerationRunner) finalizeTimelineLocked(status string) {
	before := r.timeline.Snapshot()
	after := r.timeline.Finalize(status)
	for _, block := range after.ContentBlocks {
		if block.Type == "output_text" {
			continue
		}
		var previous *assistantBlock
		for _, candidate := range before.ContentBlocks {
			if candidate.BlockID == block.BlockID {
				previous = candidate
				break
			}
		}
		if previous == nil || previous.Type == "output_text" || previous.Status == block.Status {
			continue
		}
		if block.Status == asstCompleted {
			r.emitEventLocked("content_block.completed", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
		} else {
			r.emitEventLocked("content_block.updated", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "patch": map[string]any{"status": block.Status}})
		}
	}
}

// snapshotAssistantLocked mirrors snapshotAssistant.
func (r *ChatGenerationRunner) snapshotAssistantLocked() map[string]any {
	snapshot := r.timeline.Snapshot()
	reasoningText := ""
	for _, block := range snapshot.ContentBlocks {
		if block.Type == "reasoning" {
			reasoningText += block.Text
		}
	}
	assistantStatus := "streaming"
	if isTerminalProcessStatus(r.currentState) {
		assistantStatus = r.currentState
	}
	return map[string]any{
		"id":            r.Identity.AssistantMessageID,
		"status":        assistantStatus,
		"contentText":   snapshot.ContentText,
		"reasoningText": reasoningText,
		"contentBlocks": rawBlocksSlice(snapshot.ContentBlocks),
	}
}

func rawBlocksSlice(blocks []*assistantBlock) []any {
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, jsonRawBlock(block))
	}
	return out
}

func lastOf(blocks []*assistantBlock) *assistantBlock {
	if len(blocks) == 0 {
		return nil
	}
	return blocks[len(blocks)-1]
}

func appendBlockCopy(blocks []*assistantBlock, block *assistantBlock) []*assistantBlock {
	out := make([]*assistantBlock, 0, len(blocks)+1)
	out = append(out, blocks...)
	out = append(out, block)
	return out
}

func canAppendBlock(blocks []*assistantBlock, block *assistantBlock) bool {
	return jsonBytesOf(rawBlocksSlice(appendBlockCopy(blocks, block))) <= maxTimelineTotalBytes
}

func jsonBytesOf(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return math.MaxInt32
	}
	return len(raw)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

// terminalizeAssistantBlocks mirrors terminalizeChatContentBlocksForPersistence
// over assistant timeline blocks: active statuses collapse to the terminal
// status and oversized payloads drop to [].
func terminalizeAssistantBlocks(blocks []*assistantBlock, status string) json.RawMessage {
	normalized := make([]*assistantBlock, 0, len(blocks))
	for _, block := range blocks {
		cloned := cloneAssistantBlock(block)
		if cloned.Type == "reasoning" || cloned.Type == "tool_call" || cloned.Type == "output_image" || cloned.Type == "output_audio" {
			if cloned.Status == asstStarted || cloned.Status == asstUpdated {
				cloned.Status = status
			}
		}
		// output_media_task 的任务状态跨越轮次存续（异步任务在轮次结束后仍由
		// media-tasks 结算推进），终态化不改写其状态。
		// 子代理过程增量（item.progress）为限长展示数据，终态落库时保留
		// 最后一次快照（契约 §10.3），供历史回看重建子代理过程区。
		normalized = append(normalized, cloned)
	}
	payload, err := json.Marshal(rawBlocksSlice(normalized))
	if err != nil || len(payload) > maxPersistedChatContentBlocksBytes {
		return json.RawMessage("[]")
	}
	return json.RawMessage(payload)
}

const maxPersistedChatContentBlocksBytes = 192 * 1024
