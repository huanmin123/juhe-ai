package chat

import (
	"sync"
	"time"
)

// GenerationHub mirrors ChatGenerationRegistry (chat-generation-registry.ts):
// one runner per conversation, terminal snapshots remembered for
// submissions/attach resolution, cooperative shutdown.

type GenerationHub struct {
	mu                    sync.Mutex
	runners               map[string]*ChatGenerationRunner
	terminalSnapshots     map[string]ChatGenerationStatusSnapshot
	terminalSnapshotKeys  []string
	terminalSnapshotLimit int
	shuttingDown          bool
	now                   func() string
}

// NewGenerationHub builds the registry with the Node default terminal
// snapshot limit (512).
func NewGenerationHub(now func() string) *GenerationHub {
	if now == nil {
		now = func() string { return isoMillis(time.Now()) }
	}
	return &GenerationHub{
		runners:               map[string]*ChatGenerationRunner{},
		terminalSnapshots:     map[string]ChatGenerationStatusSnapshot{},
		terminalSnapshotLimit: 512,
		now:                   now,
	}
}

func hubIdentityKey(identity GenerationIdentity) string {
	return identity.OwnerID + "\x00" + identity.ConversationID + "\x00" + identity.TurnID
}

// Start mirrors start; only one runner per conversation id.
func (h *GenerationHub) Start(runner *ChatGenerationRunner) bool {
	if !h.Register(runner) {
		return false
	}
	return h.Launch(runner)
}

// Register claims the conversation slot for the runner (the map-registration
// half of Node start()) without launching the generation goroutine. The
// stream route registers first, attaches the SSE subscriber next and only
// then Launches: Node's single-threaded event loop guarantees the subscribe
// happens before the runner publishes anything, and Go needs the explicit
// ordering to keep that observable contract (a subscriber never misses
// content_block events that precede its attachment).
func (h *GenerationHub) Register(runner *ChatGenerationRunner) bool {
	h.mu.Lock()
	if h.shuttingDown {
		h.mu.Unlock()
		return false
	}
	if _, exists := h.runners[runner.Identity.ConversationID]; exists {
		h.mu.Unlock()
		return false
	}
	key := hubIdentityKey(GenerationIdentity{OwnerID: runner.Identity.OwnerID, ConversationID: runner.Identity.ConversationID, TurnID: runner.Identity.TurnID})
	delete(h.terminalSnapshots, key)
	h.removeTerminalKeyLocked(key)
	h.runners[runner.Identity.ConversationID] = runner
	h.mu.Unlock()
	return true
}

// Launch starts the registered runner's generation goroutine (the second
// half of Node start()); a runner that cannot start releases the slot again.
func (h *GenerationHub) Launch(runner *ChatGenerationRunner) bool {
	started := runner.Start(func() {
		h.rememberTerminalSnapshot(runner)
		h.deleteIfMatches(runner)
	})
	if !started {
		h.deleteIfMatches(runner)
		return false
	}
	return true
}

func (h *GenerationHub) removeTerminalKeyLocked(key string) {
	for index, candidate := range h.terminalSnapshotKeys {
		if candidate == key {
			h.terminalSnapshotKeys = append(h.terminalSnapshotKeys[:index], h.terminalSnapshotKeys[index+1:]...)
			return
		}
	}
}

func (h *GenerationHub) deleteIfMatches(runner *ChatGenerationRunner) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.runners[runner.Identity.ConversationID]; !ok || current != runner {
		return false
	}
	delete(h.runners, runner.Identity.ConversationID)
	return true
}

func (h *GenerationHub) rememberTerminalSnapshot(runner *ChatGenerationRunner) {
	runner.mu.Lock()
	terminal := isTerminalProcessStatus(runner.currentState)
	authoritative := runner.authoritativeTerm
	snapshot := ChatGenerationStatusSnapshot{
		State:                  "running",
		EventVersion:           runner.eventVersion,
		LastSemanticActivityAt: runner.lastSemanticActAt,
		AssistantMessageID:     runner.Identity.AssistantMessageID,
	}
	if terminal {
		snapshot.State = "terminal"
	}
	runner.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shuttingDown || !terminal || !authoritative || h.terminalSnapshotLimit == 0 {
		return
	}
	key := hubIdentityKey(GenerationIdentity{OwnerID: runner.Identity.OwnerID, ConversationID: runner.Identity.ConversationID, TurnID: runner.Identity.TurnID})
	delete(h.terminalSnapshots, key)
	h.removeTerminalKeyLocked(key)
	h.terminalSnapshots[key] = snapshot
	h.terminalSnapshotKeys = append(h.terminalSnapshotKeys, key)
	for len(h.terminalSnapshots) > h.terminalSnapshotLimit {
		oldest := h.terminalSnapshotKeys[0]
		h.terminalSnapshotKeys = h.terminalSnapshotKeys[1:]
		delete(h.terminalSnapshots, oldest)
	}
}

func (h *GenerationHub) get(identity GenerationIdentity) *ChatGenerationRunner {
	h.mu.Lock()
	defer h.mu.Unlock()
	runner := h.runners[identity.ConversationID]
	if runner == nil {
		return nil
	}
	if runner.Identity.OwnerID != identity.OwnerID || runner.Identity.TurnID != identity.TurnID {
		return nil
	}
	return runner
}

// GetRunner exposes the active runner (port for the routes layer).
func (h *GenerationHub) GetRunner(identity GenerationIdentity) (*ChatGenerationRunner, bool) {
	runner := h.get(identity)
	return runner, runner != nil
}

// Snapshot implements the routes GenerationRegistry port.
func (h *GenerationHub) Snapshot(ownerID, conversationID, turnID string) GenerationSnapshot {
	if runner := h.get(GenerationIdentity{OwnerID: ownerID, ConversationID: conversationID, TurnID: turnID}); runner != nil {
		status := runner.StatusSnapshot()
		version := status.EventVersion
		activity := status.LastSemanticActivityAt
		return GenerationSnapshot{
			State:                  status.State,
			AssistantMessageID:     status.AssistantMessageID,
			EventVersion:           &version,
			LastSemanticActivityAt: &activity,
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if snapshot, ok := h.terminalSnapshots[hubIdentityKey(GenerationIdentity{OwnerID: ownerID, ConversationID: conversationID, TurnID: turnID})]; ok {
		version := snapshot.EventVersion
		activity := snapshot.LastSemanticActivityAt
		return GenerationSnapshot{
			State:                  snapshot.State,
			AssistantMessageID:     snapshot.AssistantMessageID,
			EventVersion:           &version,
			LastSemanticActivityAt: &activity,
		}
	}
	return GenerationSnapshot{State: "missing"}
}

// Get implements the routes GenerationRegistry port.
func (h *GenerationHub) Get(ownerID, conversationID, turnID string) (GenerationRunner, bool) {
	runner := h.get(GenerationIdentity{OwnerID: ownerID, ConversationID: conversationID, TurnID: turnID})
	if runner == nil {
		return nil, false
	}
	return runner, true
}

// Subscribe mirrors subscribe.
func (h *GenerationHub) Subscribe(identity GenerationIdentity, subscriber ChatGenerationSubscriber) bool {
	if h.shuttingDownState() {
		return false
	}
	runner := h.get(identity)
	if runner == nil {
		return false
	}
	return runner.Subscribe(subscriber)
}

// Unsubscribe mirrors unsubscribe.
func (h *GenerationHub) Unsubscribe(identity GenerationIdentity, subscriber ChatGenerationSubscriber) bool {
	runner := h.get(identity)
	if runner == nil {
		return false
	}
	return runner.Unsubscribe(subscriber)
}

func (h *GenerationHub) shuttingDownState() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shuttingDown
}

// Stop aborts the active runner for an identity.
func (h *GenerationHub) Stop(identity GenerationIdentity) bool {
	runner := h.get(identity)
	return runner != nil && runner.Abort()
}

// Shutdown mirrors shutdown: abort everything, wait up to timeout, then drop
// all runners.
func (h *GenerationHub) Shutdown(timeout time.Duration) {
	h.mu.Lock()
	h.shuttingDown = true
	h.terminalSnapshots = map[string]ChatGenerationStatusSnapshot{}
	h.terminalSnapshotKeys = nil
	runners := make([]*ChatGenerationRunner, 0, len(h.runners))
	for _, runner := range h.runners {
		runners = append(runners, runner)
	}
	h.mu.Unlock()
	for _, runner := range runners {
		runner.Abort()
	}
	if len(runners) == 0 {
		return
	}
	deadline := time.After(timeout)
	closed := make(chan struct{}, len(runners))
	for _, runner := range runners {
		go func(r *ChatGenerationRunner) {
			<-r.Completion()
			closed <- struct{}{}
		}(runner)
	}
	for range runners {
		select {
		case <-closed:
		case <-deadline:
			goto drained
		}
	}
drained:
	for _, runner := range runners {
		h.deleteIfMatches(runner)
	}
}
