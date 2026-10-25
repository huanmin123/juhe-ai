package main

// G20 phase-3 my-chat route family assembly: the composition root wires the
// chat database owner (the dedicated chat database / juhe_chat schema), the
// generation-wave ports (chain_chat.go executor + 内部身份通道
// chain_chat_identity.go + chain_chat_images.go image pipeline +
// chain_chat_observation.go observation scheduler) and mounts Deps at
// ${systemApiPrefix}/my-chat (Node chat.routes.ts router mount).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
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
	store, err := chat.NewStore(chatDB, time.Now, nil)
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
		Store:               store,
		RequireSession:      composed.authDeps.RequireSession(true),
		Hub:                 hub,
		Generations:         hub,
		AttachStream:        chatAttachStreamHandler(hub),
		Executor:            executor,
		ModelCatalog:        chatModelCatalog{cache: services.Cache},
		AccountModelCatalog: chatModelCatalog{cache: services.Cache},
		// 归属系统账户开关端口（生图开关载体，随 chat Key 退场从 gateway key
		// 视图迁到 system_accounts 直读，保持原开关语义）。
		SystemAccountFlags: chatSystemAccountFlags{db: composed.db},
		// AI 问答会话账户绑定的解析校验端口（accounts.Store 只读查询
		// FindChatAccount）：账户选择/切换与发送前置校验的存在性 + 启用口径。
		AccountLookup: accountLookup,
		// GET /my-chat/accounts 的账户列表查询端口（同一 accounts.Store 的
		// ListChatAccountOptions）。
		AccountOptionsLookup: accountOptionsLookup,
		ObjectStore:          objectStore,
		// 生图 URL 下载按绑定账户的 proxy_profile 出站（BUG-0232 关联：
		// grok /v1/images/edits 只回 imgen.x.ai 临时链接）；guard 与 /v1 链
		// 同用部署级 UpstreamURLSecurity 配置；解析失败回落直连。
		ImageDownloadProxy: newChatImageDownloadProxy(composed.db, cfg.Secret, cfg.UpstreamURLSecurity, func(message string) { slog.Warn(message) }),
		ImageProcessor:     newChatImageProcessor(),
		ImageObservation:   newChatImageObservations(chatDB, objectStore, executor),
		// M7 问答音视频工具的 media_jobs 只读端口（问答音视频工具设计 §4：
		// 任务面归属校验与终态快照；gatewaymedia 仓储适配——internal/chat 不
		// 直接依赖 gatewaymedia，沿组合根注入先例）。媒体任务面未装配（组合
		// 测试）保持 nil，media-tasks 端点显式降级。
		MediaJobs:               newChatMediaJobsLookup(chain),
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
	// AI 问答调度覆盖通道（2026-10-10 修订）：chat-pinned 直取端口——经 runtime
	// cache 专用路径按（分组, 账户 ID）直取运行时快照，含禁用/冷却/过期/禁调度
	// 账户；装配期置位一次，此后只读。
	setChainChatDispatchPinnedFetch(services.Cache.ListFreshOpenAIAccountsForChatPinnedAsync)
	// 可靠性批次2（缺陷1）：把生成排空接进 composed.shutdowns。shutdowns 是
	// LIFO（后注册先执行）：本注册晚于 chainShutdown / chainServices.Close，
	// 停机时 hub 排空先于网关链关闭——runner 的收尾派发（轮次落 canceled 终态）
	// 仍可使用在途链；compose.go Shutdown 注释宣称的 "chat generation hub
	// drain first" 此前从未接线，SIGTERM 会直接丢弃全部在途轮次。
	composed.shutdowns = append(composed.shutdowns, func() { hub.Shutdown(chatGenerationHubDrainTimeout) })
	deps.Register(composed.kernel, systemAPIPrefix+"/my-chat")
	return deps, nil
}

// chatSystemAccountFlags 实现 chat.SystemAccountFlagsLookup：直读
// system_accounts.image_generation_enabled（仅 active 账户生效）。免 Key 直调
// 后生图开关从 gateway key 运行时视图迁到归属系统账户直读（与
// chain_chat_image_proxy.go 同一 bind/table 方言处理），保持原开关语义；每轮
// 发送一次直查（发送为低频操作，跟随开关变更，无缓存失效面）。账户不存在/
// 非 active 返回 false（保守：不授予生图工具），查询错误原样上抛。
type chatSystemAccountFlags struct {
	db *sql.DB
}

// ImageGenerationEnabled implements chat.SystemAccountFlagsLookup.
func (f chatSystemAccountFlags) ImageGenerationEnabled(systemAccountID string) (bool, error) {
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return false, nil
	}
	bind := func(query string) string { return accountscore.SQLBind(query) }
	table := "juhe_business.system_accounts"
	var enabled int64
	err := f.db.QueryRow(bind(`SELECT image_generation_enabled FROM `+table+`
		WHERE id = ? AND status = 'active' LIMIT 1`), systemAccountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

// chatMediaJobsLookupAdapter 把 gatewaymedia.MediaJobsRepo 适配为 chat 侧的
// media_jobs 只读端口（FindChatMediaJob；归属校验语义原样透传——未命中/非
// 本归属任务统一 not found，归属信息不泄露）。
type chatMediaJobsLookupAdapter struct {
	repo *gatewaymedia.MediaJobsRepo
}

// newChatMediaJobsLookup 从链上的媒体任务面运行时解析端口；任务面未装配
// （组合测试）返回 nil。
func newChatMediaJobsLookup(chain *gatewayChain) chat.ChatMediaJobsLookup {
	if chain == nil || chain.mediaJobs == nil || chain.mediaJobs.repo == nil {
		return nil
	}
	return &chatMediaJobsLookupAdapter{repo: chain.mediaJobs.repo}
}

// FindChatMediaJob implements chat.ChatMediaJobsLookup. ownerSystemAccountID
// 是归属系统账户：repo 归属过滤已切到 owner_system_account_id 列（AI 问答免
// Key 直调计划 §3.3），不经 api_keys join 推导。
func (a *chatMediaJobsLookupAdapter) FindChatMediaJob(jobID, ownerSystemAccountID string) (chat.ChatMediaJobSnapshot, bool, error) {
	record, err := a.repo.GetByIDAndOwner(context.Background(), jobID, ownerSystemAccountID)
	if err != nil {
		if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
			return chat.ChatMediaJobSnapshot{}, false, nil
		}
		return chat.ChatMediaJobSnapshot{}, false, err
	}
	if record == nil {
		return chat.ChatMediaJobSnapshot{}, false, nil
	}
	return chat.ChatMediaJobSnapshot{
		ID:            record.ID,
		Kind:          string(record.Kind),
		Status:        string(record.Status),
		UpstreamJobID: record.UpstreamJobID,
	}, true, nil
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
// PostgreSQL aliases a shared pool handle (juhe_chat schema qualification)
// and closes nothing here. 2026-10-24 存储形态收口：sqlite 专库文件臂删除。
func openChatDatabase(cfg runtimeConfig, postgresPools *pgpool.Registry) (*sql.DB, error) {
	handle, err := postgresPools.Acquire(cfg.BusinessPostgresURL, "gateway-chat",
		gatewayPostgresPoolMaxOpen, gatewayPostgresPoolMaxIdle)
	if err != nil {
		return nil, fmt.Errorf("open chat postgres pool: %w", err)
	}
	return handle.DB(), nil
}
