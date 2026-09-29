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
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// chatGenerationHubDrainTimeout 是停机时生成排空的有界等待（对齐 Node
// shutdownChatGenerationRegistry 的 abort + 有界等待语义）：Abort 全部 runner
// 后最多等这么久让轮次收敛终态，超时强制摘除，不阻塞其余停机步骤。
const chatGenerationHubDrainTimeout = 8 * time.Second

// composeChatFamily builds the chat Deps over the chat database handle and
// the assembled /v1 chain, and registers the my-chat route family on the
// kernel. It fails fast naming the missing chat database handle.
// accountLookup 是会话绑定账户的解析校验端口（生产组合根传 accounts.Store；
// nil 让绑定校验返回显式错误）。accountOptionsLookup 是 GET /my-chat/accounts
// 的账户列表查询端口（同一 accounts.Store；nil 让端点返回显式错误）。
func composeChatFamily(composed *composition, cfg runtimeConfig, chatDB *sql.DB, services *chainRuntimeServices, chain *gatewayChain, accountLookup chat.ChatAccountLookup, accountOptionsLookup chat.ChatAccountOptionsLookup) (*chat.Deps, error) {
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
		return nil, fmt.Errorf("my-chat 组合缺少网关链 runtime")
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
		Store:          store,
		RequireSession: composed.authDeps.RequireSession(true),
		Hub:            hub,
		Generations:    hub,
		AttachStream:   chatAttachStreamHandler(hub),
		Executor:       executor,
		ModelCatalog:   chatModelCatalog{cache: services.Cache},
		ChatKeys:       newChatAPIKeyProvider(composed.db, composed.pgDialect, cfg.Secret),
		GatewayKeys:    chatGatewayKeyValidator{cache: services.Cache},
		// AI 问答会话账户绑定的解析校验端口（accounts.Store 只读查询
		// FindChatAccount）：账户选择/切换与发送前置校验的存在性 + 启用口径。
		AccountLookup: accountLookup,
		// GET /my-chat/accounts 的账户列表查询端口（同一 accounts.Store 的
		// ListChatAccountOptions）。
		AccountOptionsLookup: accountOptionsLookup,
		ObjectStore:          objectStore,
		// 生图 URL 下载按绑定账户的 proxy_profile 出站（BUG-0232 关联：
		// grok /v1/images/edits 只回 imgen.x.ai 临时链接）；解析失败回落直连。
		ImageDownloadProxy:      newChatImageDownloadProxy(composed.db, composed.pgDialect, cfg.Secret, func(message string) { slog.Warn(message) }),
		ImageProcessor:          newChatImageProcessor(),
		ImageObservation:        newChatImageObservations(chatDB, composed.pgDialect, objectStore, executor),
		Compactions:             compactions,
		TokenCount:              tokenCount,
		MaxTurnsPerConversation: cfg.ChatMaxTurnsPerConversation,
		RetentionDays:           cfg.ChatRetentionDays,
		DiagnosticToolEnabled:   cfg.ChatDiagnosticToolEnabled,
		ToolEnvironment:         cfg.ChatToolEnvironment,
	}
	// AI 问答调度覆盖通道（设计 §6）：account 模式承载分组解析端口，域 A 同
	// 口径（FindChatAccount 的 EnabledGroupIDs，enabled=1 分组绑定）。生产组合
	// 根装配期置位一次，此后只读（进程级槽，chain_obs_wiring.go 同模式）。
	setChainChatDispatchAccountGroups(func(accountID string) ([]string, bool) {
		if accountLookup == nil {
			return nil, false
		}
		// 进程级系统上下文（无请求用户），绑定授权已在创建与发送校验收敛；
		// 按 admin 范围（全量号池口径）解析账户启用分组。
		ref, err := accountLookup.FindChatAccount(chat.ChatBindScope{IsAdmin: true}, accountID)
		if err != nil || ref == nil {
			return nil, false
		}
		return ref.EnabledGroupIDs, true
	})
	// 可靠性批次2（缺陷1）：把生成排空接进 composed.shutdowns。shutdowns 是
	// LIFO（后注册先执行）：本注册晚于 chainShutdown / chainServices.Close，
	// 停机时 hub 排空先于网关链关闭——runner 的收尾派发（轮次落 canceled 终态）
	// 仍可使用在途链；compose.go Shutdown 注释宣称的 "chat generation hub
	// drain first" 此前从未接线，SIGTERM 会直接丢弃全部在途轮次。
	composed.shutdowns = append(composed.shutdowns, func() { hub.Shutdown(chatGenerationHubDrainTimeout) })
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

		// writeEvent 的 ended 守卫与 data==nil 兜底已删（w2 登记）：唯一调用点
		// 恒传非 nil data；终态事件与失败路径置位后立即同步退出循环，事件循环
		// 单线程，ended 读取时恒为 false。
		writeEvent := func(eventType string, data map[string]any) bool {
			payload, err := json.Marshal(data)
			if err != nil {
				return false
			}
			chunk := "event: " + eventType + "\ndata: " + string(payload) + "\n\n"
			if _, err := w.Write([]byte(chunk)); err != nil {
				return false
			}
			flush()
			return true
		}

		heartbeat := time.NewTicker(5 * time.Second)
		defer heartbeat.Stop()
		ctx := r.Context()
		for {
			select {
			case event, ok := <-events:
				if !ok {
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
					return true
				}
			case <-heartbeat.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return true
				}
				flush()
			case <-ctx.Done():
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
		handle, err := postgresPools.Acquire(cfg.BusinessPostgresURL, "gateway-chat",
			gatewayPostgresPoolMaxOpen, gatewayPostgresPoolMaxIdle)
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
