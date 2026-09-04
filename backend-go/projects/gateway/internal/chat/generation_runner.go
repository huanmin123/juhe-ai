package chat

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"
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
// object literals so persisted JSON keeps the same shape.
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

func findToolBlock(blocks []*assistantBlock, callID string) *assistantBlock {
	for _, block := range blocks {
		if block.Type == "tool_call" && block.CallID == callID {
			return block
		}
	}
	return nil
}

func findImageBlock(blocks []*assistantBlock, assetID string) *assistantBlock {
	for _, block := range blocks {
		if block.Type == "output_image" && block.AssetID == assetID {
			return block
		}
	}
	return nil
}

// StartTool mirrors startTool.
func (t *assistantTimeline) StartTool(callID, toolType string, item map[string]any) (*assistantBlock, error) {
	if err := t.ensureMutable(); err != nil {
		return nil, err
	}
	if trimSpace(callID) == "" {
		return nil, errors.New("助手工具 callId 不能为空")
	}
	if trimSpace(toolType) == "" {
		return nil, errors.New("助手工具 toolType 不能为空")
	}
	nextItem := cloneJSONMap(item)
	if existing := findToolBlock(t.blocks, callID); existing != nil {
		if existing.ToolType != toolType {
			return nil, errors.New("助手工具类型不一致: " + callID)
		}
		if !isTerminalProcessStatus(existing.Status) && nextItem != nil && existing.Item == nil {
			existing.Item = nextItem
		}
		return cloneAssistantBlock(existing), nil
	}
	block := &assistantBlock{Type: "tool_call", BlockID: t.nextBlockID(), Order: int64(len(t.blocks) + 1), CallID: callID, ToolType: toolType, Status: asstStarted, Item: nextItem}
	t.blocks = append(t.blocks, block)
	return cloneAssistantBlock(block), nil
}

// UpdateTool mirrors updateTool.
func (t *assistantTimeline) UpdateTool(callID, status string, item map[string]any) (*assistantBlock, error) {
	if err := t.ensureMutable(); err != nil {
		return nil, err
	}
	block := findToolBlock(t.blocks, callID)
	if block == nil {
		return nil, errors.New("未知的助手工具调用: " + callID)
	}
	nextItem := cloneJSONMap(item)
	if isTerminalProcessStatus(block.Status) {
		return cloneAssistantBlock(block), nil
	}
	if status != asstStarted || block.Status == asstStarted {
		block.Status = status
	}
	if nextItem != nil {
		block.Item = nextItem
	}
	return cloneAssistantBlock(block), nil
}

// StartImageInput mirrors AssistantOutputImageStartInput.
type StartImageInput struct {
	AssetID       string
	MimeType      string
	Width         *int64
	Height        *int64
	RevisedPrompt string
}

// StartImage mirrors startImage.
func (t *assistantTimeline) StartImage(input StartImageInput) *assistantBlock {
	if err := t.ensureMutable(); err != nil {
		return nil
	}
	if trimSpace(input.AssetID) == "" {
		return nil
	}
	if existing := findImageBlock(t.blocks, input.AssetID); existing != nil {
		return cloneAssistantBlock(existing)
	}
	block := &assistantBlock{Type: "output_image", BlockID: t.nextBlockID(), Order: int64(len(t.blocks) + 1), AssetID: input.AssetID, Status: asstStarted}
	if input.MimeType != "" {
		block.MimeType = input.MimeType
	}
	if input.Width != nil {
		block.Width = input.Width
	}
	if input.Height != nil {
		block.Height = input.Height
	}
	if input.RevisedPrompt != "" {
		block.RevisedPrompt = input.RevisedPrompt
	}
	t.blocks = append(t.blocks, block)
	return cloneAssistantBlock(block)
}

// UpdateImage mirrors updateImage.
func (t *assistantTimeline) UpdateImage(input StartImageInput, status string) *assistantBlock {
	if err := t.ensureMutable(); err != nil {
		return nil
	}
	block := findImageBlock(t.blocks, input.AssetID)
	if block == nil {
		created := t.StartImage(input)
		if created == nil {
			return nil
		}
		if status == asstStarted {
			return created
		}
		return t.UpdateImage(input, status)
	}
	if isTerminalProcessStatus(block.Status) {
		return cloneAssistantBlock(block)
	}
	block.Status = status
	if input.MimeType != "" {
		block.MimeType = input.MimeType
	}
	if input.Width != nil {
		block.Width = input.Width
	}
	if input.Height != nil {
		block.Height = input.Height
	}
	if input.RevisedPrompt != "" {
		block.RevisedPrompt = input.RevisedPrompt
	}
	return cloneAssistantBlock(block)
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
		if block.Type == "tool_call" && (block.Status == asstStarted || block.Status == asstUpdated) {
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
		if block.Type == "output_image" && block.Status == asstStarted {
			block.Status = status
		}
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

// ChatGenerationToolEvent mirrors ChatGenerationToolEvent.
type ChatGenerationToolEvent struct {
	ID       string
	ToolType string
	Status   string // started|updated|completed|failed|canceled
	Item     map[string]any
}

// ChatGenerationImageEvent mirrors ChatGenerationImageEvent.
type ChatGenerationImageEvent struct {
	ID     string
	Status string
	Item   map[string]any
}

// ChatGenerationProjectionUpdate mirrors ChatGenerationProjectionUpdate.
type ChatGenerationProjectionUpdate struct {
	ContentTextDelta   *string
	ReasoningTextDelta *string
	ReasoningCompleted bool
	ToolEvent          *ChatGenerationToolEvent
	ImageEvent         *ChatGenerationImageEvent
}

func (u ChatGenerationProjectionUpdate) hasUpdate() bool {
	return (u.ContentTextDelta != nil && *u.ContentTextDelta != "") ||
		(u.ReasoningTextDelta != nil && *u.ReasoningTextDelta != "") ||
		u.ReasoningCompleted || u.ToolEvent != nil || u.ImageEvent != nil
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
func NewChatGenerationRunner(options ChatGenerationRunnerOptions, cancel contextCancelFunc, cancelled contextCancelledFunc) *ChatGenerationRunner {
	now := options.Now
	if now == nil {
		now = func() string { return isoMillis(time.Now()) }
	}
	return &ChatGenerationRunner{
		Identity:               options.Identity,
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
	if r.currentState != "running" {
		r.mu.Unlock()
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

// Unsubscribe mirrors unsubscribe.
func (r *ChatGenerationRunner) Unsubscribe(subscriber ChatGenerationSubscriber) bool {
	r.mu.Lock()
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
	result, err := r.execute(&ChatGenerationExecutionContext{
		Aborted:        r.aborted,
		Publish:        r.Publish,
		SnapshotBlocks: r.SnapshotContentBlocks,
	})
	r.mu.Lock()
	if isTerminalProcessStatus(r.currentState) {
		r.mu.Unlock()
		r.finish(onSettled)
		return
	}
	if err == nil {
		r.finalizeTimelineLocked(result.Status)
		r.currentState = result.Status
		r.authoritativeTerm = true
		r.emitEventLocked("message."+result.Status, result.Data)
		r.mu.Unlock()
		r.finish(onSettled)
		return
	}
	publicError := ClassifyUnknownChatGenerationError(err)
	authoritativeFailure := false
	if r.onUnexpectedError != nil {
		r.mu.Unlock()
		finalizerErr := r.onUnexpectedError(publicError)
		r.mu.Lock()
		if finalizerErr == nil {
			authoritativeFailure = true
		}
	}
	if !isTerminalProcessStatus(r.currentState) {
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
		} else {
			r.timeline.Finalize(asstFailed)
			r.currentState = asstFailed
		}
	}
	r.mu.Unlock()
	r.finish(onSettled)
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
	if update.ToolEvent != nil {
		r.applyToolEventLocked(update.ToolEvent)
	}
	if update.ImageEvent != nil {
		r.applyImageEventLocked(update.ImageEvent)
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

func (r *ChatGenerationRunner) applyToolEventLocked(input *ChatGenerationToolEvent) {
	event := sanitizeToolEvent(input)
	snapshot := r.timeline.Snapshot()
	existing := findToolBlock(snapshot.ContentBlocks, event.ID)
	if existing == nil {
		item := toolItemWithinBudget(snapshot.ContentBlocks, &assistantBlock{
			Type: "tool_call", CallID: event.ID, ToolType: event.ToolType, Status: asstStarted, Item: event.Item,
		})
		candidate := &assistantBlock{
			Type: "tool_call", BlockID: "assistant_block_" + itoa(len(snapshot.ContentBlocks)+1), Order: int64(len(snapshot.ContentBlocks) + 1),
			CallID: event.ID, ToolType: event.ToolType, Status: asstStarted, Item: item,
		}
		if !canAppendBlock(snapshot.ContentBlocks, candidate) || !toolBlocksWithinBudget(appendBlockCopy(snapshot.ContentBlocks, candidate)) {
			return
		}
		started, err := r.timeline.StartTool(event.ID, event.ToolType, item)
		if err != nil {
			return
		}
		r.emitEventLocked("content_block.started", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(started)})
		if event.Status != asstStarted {
			r.updateToolLocked(started, event.Status, event.Item)
		}
		return
	}
	if existing.ToolType != event.ToolType {
		// Node throws inside publish; mirror the no-op contract here.
		return
	}
	if event.Status == asstStarted {
		if existing.Item != nil {
			return
		}
		item := toolItemWithinBudget(snapshot.ContentBlocks, &assistantBlock{Type: "tool_call", CallID: existing.CallID, ToolType: existing.ToolType, Status: existing.Status, Item: event.Item})
		if item == nil {
			return
		}
		block, err := r.timeline.StartTool(event.ID, event.ToolType, item)
		if err != nil {
			return
		}
		r.emitEventLocked("content_block.updated", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "patch": map[string]any{"item": jsonRawMap(block.Item)}})
		return
	}
	r.updateToolLocked(existing, event.Status, event.Item)
}

func jsonRawMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return cloneJSONMap(value)
}

func (r *ChatGenerationRunner) applyImageEventLocked(input *ChatGenerationImageEvent) {
	event := sanitizeImageEvent(input)
	assetID := ""
	if event.Item != nil {
		if value, ok := event.Item["assetId"].(string); ok {
			assetID = value
		}
	}
	if assetID == "" {
		return
	}
	snapshot := r.timeline.Snapshot()
	existing := findImageBlock(snapshot.ContentBlocks, assetID)
	if existing == nil {
		if event.Status == asstFailed || event.Status == asstCanceled {
			return
		}
		block := r.timeline.StartImage(StartImageInput{
			AssetID:       assetID,
			MimeType:      stringItem(event.Item, "mimeType"),
			Width:         positiveIntegerItem(event.Item, "width"),
			Height:        positiveIntegerItem(event.Item, "height"),
			RevisedPrompt: stringItem(event.Item, "revisedPrompt"),
		})
		if block == nil {
			return
		}
		r.emitEventLocked("content_block.started", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
		if event.Status != asstStarted {
			r.updateImageLocked(block, event.Status, event.Item)
		}
		return
	}
	if event.Status == asstStarted {
		return
	}
	r.updateImageLocked(existing, event.Status, event.Item)
}

func (r *ChatGenerationRunner) updateImageLocked(existing *assistantBlock, status string, item map[string]any) {
	normalized := status
	if normalized == asstUpdated {
		normalized = asstStarted
	}
	block := r.timeline.UpdateImage(StartImageInput{
		AssetID:       existing.AssetID,
		MimeType:      stringItem(item, "mimeType"),
		Width:         positiveIntegerItem(item, "width"),
		Height:        positiveIntegerItem(item, "height"),
		RevisedPrompt: stringItem(item, "revisedPrompt"),
	}, normalized)
	if block == nil {
		return
	}
	if block.Status == existing.Status && block.MimeType == existing.MimeType &&
		intPtrEqual(block.Width, existing.Width) && intPtrEqual(block.Height, existing.Height) &&
		block.RevisedPrompt == existing.RevisedPrompt {
		return
	}
	if block.Status == asstCompleted {
		r.emitEventLocked("content_block.completed", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
		return
	}
	patch := map[string]any{"status": block.Status}
	if block.MimeType != "" {
		patch["mimeType"] = block.MimeType
	}
	if block.Width != nil {
		patch["width"] = *block.Width
	}
	if block.Height != nil {
		patch["height"] = *block.Height
	}
	if block.RevisedPrompt != "" {
		patch["revisedPrompt"] = block.RevisedPrompt
	}
	r.emitEventLocked("content_block.updated", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "patch": patch})
}

func (r *ChatGenerationRunner) updateToolLocked(existing *assistantBlock, status string, inputItem map[string]any) {
	normalized := status
	if normalized == asstUpdated {
		normalized = asstStarted
	}
	var item map[string]any
	if inputItem != nil {
		item = toolItemWithinBudget(r.timeline.Snapshot().ContentBlocks, &assistantBlock{Type: "tool_call", CallID: existing.CallID, ToolType: existing.ToolType, Status: normalized, Item: inputItem})
	}
	block, err := r.timeline.UpdateTool(existing.CallID, normalized, item)
	if err != nil {
		return
	}
	if block.Status == existing.Status && jsonMapEqual(block.Item, existing.Item) {
		return
	}
	if block.Status == asstCompleted {
		r.emitEventLocked("content_block.completed", map[string]any{"messageId": r.Identity.AssistantMessageID, "block": jsonRawBlock(block)})
		return
	}
	patch := map[string]any{"status": block.Status}
	if item != nil {
		patch["item"] = jsonRawMap(block.Item)
	}
	r.emitEventLocked("content_block.updated", map[string]any{"messageId": r.Identity.AssistantMessageID, "blockId": block.BlockID, "patch": patch})
}

// finalizeTimelineLocked mirrors finalizeTimeline.
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
	toolEvents := []any{}
	for _, block := range snapshot.ContentBlocks {
		if block.Type != "tool_call" {
			continue
		}
		event := map[string]any{"id": block.CallID, "type": block.ToolType, "status": block.Status}
		if block.Item != nil {
			event["item"] = jsonRawMap(block.Item)
		}
		toolEvents = append(toolEvents, event)
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
		"toolEvents":    toolEvents,
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

func toolItemWithinBudget(blocks []*assistantBlock, candidate *assistantBlock) map[string]any {
	if candidate.Item == nil {
		return nil
	}
	next := make([]*assistantBlock, 0, len(blocks)+1)
	replaced := false
	for _, block := range blocks {
		if block.Type == "tool_call" && block.CallID == candidate.CallID {
			next = append(next, candidate)
			replaced = true
			continue
		}
		next = append(next, block)
	}
	if !replaced {
		next = append(next, candidate)
	}
	if !toolBlocksWithinBudget(next) {
		return nil
	}
	return candidate.Item
}

func toolBlocksWithinBudget(blocks []*assistantBlock) bool {
	toolBlocks := []*assistantBlock{}
	for _, block := range blocks {
		if block.Type == "tool_call" {
			toolBlocks = append(toolBlocks, block)
		}
	}
	return jsonBytesOf(rawBlocksSlice(toolBlocks)) <= chatGenerationToolJSONMaxBytes
}

func jsonBytesOf(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return math.MaxInt32
	}
	return len(raw)
}

func jsonMapEqual(left, right map[string]any) bool {
	if left == nil && right == nil {
		return true
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func sanitizeToolEvent(input *ChatGenerationToolEvent) *ChatGenerationToolEvent {
	base := &ChatGenerationToolEvent{
		ID:       truncateUTF8(input.ID, 512),
		ToolType: truncateUTF8(input.ToolType, 512),
		Status:   input.Status,
	}
	if input.Item == nil {
		return base
	}
	item := cloneJSONMap(input.Item)
	if item == nil {
		return base
	}
	withItem := &ChatGenerationToolEvent{ID: base.ID, ToolType: base.ToolType, Status: base.Status, Item: item}
	if jsonBytesOf(item) <= chatGenerationToolJSONMaxBytes {
		return withItem
	}
	return base
}

var imageEventRedactKeys = []string{"result", "b64_json", "partial_image", "partial_image_b64", "image"}

func sanitizeImageEvent(input *ChatGenerationImageEvent) *ChatGenerationImageEvent {
	var item map[string]any
	if input.Item != nil {
		item = cloneJSONMap(input.Item)
		for _, key := range imageEventRedactKeys {
			delete(item, key)
		}
	}
	return &ChatGenerationImageEvent{ID: truncateUTF8(input.ID, 512), Status: input.Status, Item: item}
}

func stringItem(item map[string]any, key string) string {
	if item == nil {
		return ""
	}
	if value, ok := item[key].(string); ok && trimSpace(value) != "" {
		return trimSpace(value)
	}
	return ""
}

func positiveIntegerItem(item map[string]any, key string) *int64 {
	if item == nil {
		return nil
	}
	number, ok := numericValue(item[key])
	if !ok || number <= 0 || number != truncF(number) || number > 1<<53 {
		return nil
	}
	value := int64(number)
	return &value
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

func intPtrEqual(left, right *int64) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return *left == *right
}

// terminalizeAssistantBlocks mirrors terminalizeChatContentBlocksForPersistence
// over assistant timeline blocks: active statuses collapse to the terminal
// status and oversized payloads drop to [].
func terminalizeAssistantBlocks(blocks []*assistantBlock, status string) json.RawMessage {
	normalized := make([]*assistantBlock, 0, len(blocks))
	for _, block := range blocks {
		cloned := cloneAssistantBlock(block)
		if cloned.Type == "reasoning" || cloned.Type == "tool_call" || cloned.Type == "output_image" {
			if cloned.Status == asstStarted || cloned.Status == asstUpdated {
				cloned.Status = status
			}
		}
		normalized = append(normalized, cloned)
	}
	payload, err := json.Marshal(rawBlocksSlice(normalized))
	if err != nil || len(payload) > maxPersistedChatContentBlocksBytes {
		return json.RawMessage("[]")
	}
	return json.RawMessage(payload)
}

const maxPersistedChatContentBlocksBytes = 192 * 1024
