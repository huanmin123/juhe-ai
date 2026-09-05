package main

// G20 phase-3 my-chat route family assembly: the composition root wires the
// chat database owner (the dedicated chat database / juhe_chat schema), the
// generation-wave ports (chain_chat.go executor + chain_chat_keys.go key
// provider + chain_chat_images.go image pipeline + chain_chat_observation.go
// observation scheduler) and mounts Deps at ${systemApiPrefix}/my-chat
// (Node chat.routes.ts router mount).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// composeChatFamily builds the chat Deps over the chat database handle and
// the assembled /v1 chain, and registers the my-chat route family on the
// kernel. It fails fast naming the missing chat database handle.
func composeChatFamily(composed *composition, cfg runtimeConfig, chatDB *sql.DB, services *chainRuntimeServices, chain *gatewayChain) (*chat.Deps, error) {
	if composed == nil {
		return nil, fmt.Errorf("my-chat 组合缺少 composition")
	}
	if chatDB == nil {
		return nil, fmt.Errorf("my-chat 组合缺少聊天数据库句柄")
	}
	if services == nil || services.Cache == nil {
		return nil, fmt.Errorf("my-chat 组合缺少网关链 runtime cache")
	}
	if chain == nil {
		return nil, fmt.Errorf("my-chat 组合缺少网关链（JUHE_AI_GATEWAY_CHAIN_ENABLED）")
	}
	chatNow := func() string { return time.Now().UTC().Format(chainTimeLayout) }
	store, err := chat.NewStore(chatDB, composed.pgDialect, time.Now, nil)
	if err != nil {
		return nil, fmt.Errorf("create chat store: %w", err)
	}
	hub := chat.NewGenerationHub(chatNow)
	executor := newChatGatewayExecutor(chain)
	tokenCount, tokenErr := newChatTokenCount()
	if tokenErr != nil {
		return nil, tokenErr
	}
	objectStore, objectErr := newChatAssetObjectStore(cfg.ChatAssetsRoot)
	if objectErr != nil {
		return nil, objectErr
	}
	compactions := chat.NewCompactionService(store, executor, tokenCount, chatNow)
	deps := &chat.Deps{
		Store:                   store,
		RequireSession:          composed.authDeps.RequireSession(true),
		Hub:                     hub,
		Generations:             hub,
		AttachStream:            chatAttachStreamHandler(hub),
		Executor:                executor,
		ModelCatalog:            chatModelCatalog{cache: services.Cache},
		ChatKeys:                newChatAPIKeyProvider(composed.db, composed.pgDialect, cfg.Secret),
		GatewayKeys:             chatGatewayKeyValidator{cache: services.Cache},
		ObjectStore:             objectStore,
		ImageProcessor:          newChatImageProcessor(),
		ImageObservation:        newChatImageObservations(chatDB, composed.pgDialect, objectStore, executor),
		Compactions:             compactions,
		TokenCount:              tokenCount,
		MaxTurnsPerConversation: cfg.ChatMaxTurnsPerConversation,
		RetentionDays:           cfg.ChatRetentionDays,
		DiagnosticToolEnabled:   cfg.ChatDiagnosticToolEnabled,
		ToolEnvironment:         cfg.ChatToolEnvironment,
	}
	deps.Register(composed.kernel, systemAPIPrefix+"/my-chat")
	return deps, nil
}

// chatAttachStreamHandler builds the Node responseSubscriber equivalent over
// the generation hub (the chat package keeps its SSE writer unexported, so
// the composition mirrors chat/sse_write.go): SSE headers, the 5s heartbeat,
// terminal-event end + detach, request-context teardown. The handler blocks
// until the stream ends so net/http keeps the response open (Node res.end).
func chatAttachStreamHandler(hub *chat.GenerationHub) chat.AttachStreamHandler {
	return func(w http.ResponseWriter, r *http.Request, identity chat.GenerationIdentity) bool {
		events := make(chan chat.ChatGenerationEvent, 256)
		live := &chatAttachSubscriber{events: events}
		if !hub.Subscribe(identity, live) {
			return false
		}
		defer hub.Unsubscribe(identity, live)

		header := w.Header()
		header.Set("Content-Type", "text/event-stream; charset=utf-8")
		header.Set("Cache-Control", "no-store")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}

		var (
			writeMu sync.Mutex
			ended   bool
		)
		writeEvent := func(eventType string, data map[string]any) bool {
			writeMu.Lock()
			defer writeMu.Unlock()
			if ended {
				return false
			}
			if data == nil {
				data = map[string]any{}
			}
			payload, err := json.Marshal(data)
			if err != nil {
				ended = true
				return false
			}
			chunk := "event: " + eventType + "\ndata: " + string(payload) + "\n\n"
			if _, err := w.Write([]byte(chunk)); err != nil {
				ended = true
				return false
			}
			flush()
			return true
		}
		end := func() {
			writeMu.Lock()
			ended = true
			writeMu.Unlock()
		}

		heartbeat := time.NewTicker(5 * time.Second)
		defer heartbeat.Stop()
		ctx := r.Context()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					end()
					return true
				}
				data := map[string]any{}
				for key, value := range event.Data {
					data[key] = value
				}
				data["eventVersion"] = event.EventVersion
				if !writeEvent(event.Type, data) {
					return true
				}
				if event.Type == "message.completed" || event.Type == "message.failed" || event.Type == "message.canceled" {
					end()
					return true
				}
			case <-heartbeat.C:
				writeMu.Lock()
				active := !ended
				writeMu.Unlock()
				if !active {
					return true
				}
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					end()
					return true
				}
				flush()
			case <-ctx.Done():
				end()
				return true
			}
		}
	}
}

// chatAttachSubscriber pumps hub events into the attach stream channel
// (TrySend never blocks: a full buffer drops the subscriber like a dead
// downstream, matching the Node destroy-on-backpressure contract).
type chatAttachSubscriber struct {
	events  chan chat.ChatGenerationEvent
	once    sync.Once
	dropped bool
}

// TrySend implements chat.ChatGenerationSubscriber.
func (s *chatAttachSubscriber) TrySend(event chat.ChatGenerationEvent) bool {
	if s.dropped {
		return false
	}
	select {
	case s.events <- event:
		return true
	default:
		s.dropped = true
		s.once.Do(func() { close(s.events) })
		return false
	}
}

// openChatDatabase opens the chat database handle the chat store owns:
// SQLite opens the dedicated chat file (schema ensured by the startup
// preflight), PostgreSQL aliases the shared pool handle (juhe_chat schema
// qualification) and closes nothing here.
func openChatDatabase(cfg runtimeConfig, postgresPools *pgpool.Registry, businessDB *sql.DB, pgDialect bool) (*sql.DB, bool, error) {
	if pgDialect {
		handle, err := postgresPools.Acquire(cfg.BusinessPostgresURL, "gateway-chat", 0, 0)
		if err != nil {
			return nil, false, fmt.Errorf("open chat postgres pool: %w", err)
		}
		return handle.DB(), false, nil
	}
	if strings.TrimSpace(cfg.ChatDatabasePath) == "" {
		return nil, false, fmt.Errorf("sqlite 模式缺少 JUHE_AI_CHAT_DATABASE_PATH，无法打开 chat 数据库")
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(cfg.ChatDatabasePath))
	if err != nil {
		return nil, false, fmt.Errorf("open chat sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := configureSQLiteConnection(db); err != nil {
		_ = db.Close()
		return nil, false, fmt.Errorf("configure chat sqlite database: %w", err)
	}
	return db, true, nil
}
