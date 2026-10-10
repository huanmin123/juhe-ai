package gatewaypreauth

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of request/preflight.ts: the prepareOpenAIGatewayDispatchContext
// orchestration. Branch order, quota/auth rejections, response payloads and
// the DispatchContext / RouteAction contracts mirror the Node source. Steps
// owned by later slices run through the ports; routing resolution, quota,
// runtime cache reads, body state and the gemini interaction affinity reuse
// the existing Go packages directly.

// PreflightOptions mirrors OpenAIGatewayRequestPreflightOptions.
type PreflightOptions struct {
	Identity                        *OpenAIGatewayRequestIdentity
	APIKeyRecord                    *gatewayruntimecache.GatewayAPIKeyRow
	GroupFallbackAPIKeyRecord       *gatewayruntimecache.GatewayAPIKeyRow
	CandidateAccounts               []gatewayruntimecache.OpenAIAccountSecret
	RecoverableCandidateScope       *CandidateRecoverableScope
	ResponseInspectionPolicies      []gatewayruntimecache.ResponseInspectionPolicySummary
	DisableSessionAffinity          bool
	TrafficSource                   string
	SettingsOverride                *gatewayruntimecache.GatewaySettings
	RequestLane                     gatewayproto.RequestLane
	IgnoreAccountRuntimeSuppression bool
	ForwardModelsRequestToUpstream  bool
	AccountProbeModel               string
	ServerRetryBudget               *ServerRetryBudget
	GatewayRequestWallBudget        *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinationBudget         *gatewayrouting.RouteCoordinationBudget
	RequestAttemptTracker           *gatewayrouting.GatewayRequestAttemptTracker
	DownstreamCommitState           *DownstreamCommitState
	NormalRouteFirstByteConfig      *NormalRouteFirstByteRuntimeConfig
	RoutePlanSnapshot               *gatewayrouting.RoutePlanSnapshot[string]
}

// CandidateRecoverableScope 是 provided 候选（chat 调度覆盖注入）的可恢复等待
// 作用域：GroupIDs 是绑定作用域组集合（group 模式=绑定分组；account 模式=账户
// 启用分组全集），AccountID 是 account 模式的目标账户（空=不按账户收敛）。
// 注入候选全部临时不可用（熔断冷却）时，可恢复等待按该作用域重读发现恢复，
// 绝不按 Key 策略窗口组重读（否则会逃逸绑定作用域）。nil（外部 /v1、API Key
// 分组回退）保持既有按窗口组读取的语义。
type CandidateRecoverableScope struct {
	GroupIDs  []string
	AccountID string
}

// PreflightInput mirrors PrepareOpenAIGatewayDispatchContextInput.
type PreflightInput struct {
	Req             *GatewayRequest
	Res             GatewayResponseWriter
	AuditCapture    AuditCaptureContext
	Options         *PreflightOptions
	StartedAt       int64
	TraceID         string
	ClientIP        string
	Endpoint        string
	RequestSnapshot UsageRequestSnapshot
	Signal          context.Context
}

// DispatchContext mirrors OpenAIGatewayDispatchContext.
type DispatchContext struct {
	ActiveGatewaySettings                    gatewayruntimecache.GatewaySettings
	UsageContext                             GatewayFailureUsageContext
	Accounts                                 []gatewayruntimecache.OpenAIAccountSecret
	SessionIdentity                          SessionIdentity
	SessionAffinityKey                       string
	ClientStrategy                           ClientStrategyContext
	ClientIPAccountAvoidance                 ClientIPAccountAvoidanceTracker
	ModelPriority                            *gatewayrouting.GatewayAccountModelPriority
	RequestLane                              gatewayproto.RequestLane
	GroupSchedulingPolicy                    *gatewayruntimecache.GroupSchedulingPolicy
	NormalRouteFirstByteConfig               *NormalRouteFirstByteRuntimeConfig
	NormalRouteSpeedFirstConfig              *NormalRouteSpeedFirstRuntimeConfig
	ResponseInspectionPolicies               []gatewayruntimecache.ResponseInspectionPolicySummary
	APIKeyRecord                             *gatewayruntimecache.GatewayAPIKeyRow
	GroupFallbackAPIKeyRecord                *gatewayruntimecache.GatewayAPIKeyRow
	SchedulingExclusions                     *SchedulingExclusions
	DispatchSegments                         []DispatchSegment
	PrecheckHalfOpenEligible                 bool
	ServerRetryBudget                        *ServerRetryBudget
	GatewayRequestWallBudget                 *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinationBudget                  *gatewayrouting.RouteCoordinationBudget
	RequestAttemptTracker                    *gatewayrouting.GatewayRequestAttemptTracker
	DownstreamCommitState                    *DownstreamCommitState
	RoutePlanSnapshot                        gatewayrouting.RoutePlanSnapshot[string]
	InteractionResourceAffinity              *gatewaygemini.AffinityBinding
	HotQualityExplorationReservation         *HotQualityExplorationReservation
	SettleHotQualityExplorationAfterDispatch func(outcome string) error
	ReleaseClientIPConcurrency               func()
}

// RouteAction mirrors OpenAIGatewayRouteAction.
type RouteAction struct {
	Coordination                RouteActionCoordination
	Failure                     *gatewayrouting.GatewayRouteFinalFailure
	UsageContext                GatewayFailureUsageContext
	APIKeyRecord                *gatewayruntimecache.GatewayAPIKeyRow
	GroupFallbackAPIKeyRecord   *gatewayruntimecache.GatewayAPIKeyRow
	RequestLane                 gatewayproto.RequestLane
	ClientStrategy              ClientStrategyContext
	SessionIdentity             SessionIdentity
	ServerRetryBudget           *ServerRetryBudget
	GatewayRequestWallBudget    *gatewayrouting.GatewayRequestWallBudget
	RouteCoordinationBudget     *gatewayrouting.RouteCoordinationBudget
	RequestAttemptTracker       *gatewayrouting.GatewayRequestAttemptTracker
	DownstreamCommitState       *DownstreamCommitState
	RoutePlanSnapshot           gatewayrouting.RoutePlanSnapshot[string]
	InteractionResourceAffinity *gatewaygemini.AffinityBinding
	NormalRouteFirstByteConfig  *NormalRouteFirstByteRuntimeConfig
}

// RouteActionCoordination mirrors the Exclude<RouteCoordinationResult, dispatchable>
// payload the route loop consumes.
type RouteActionCoordination struct {
	Outcome                  string // 'temporarily_blocked' | 'hard_exhausted'
	Reason                   string
	EarliestRetryAtMs        *int64
	ConfirmationInFlight     bool
	BlockedAccountIDs        []string
	WaitableByCurrentRequest bool
	ForeignLeaseInFlight     bool
}

// PreflightResult mirrors the DispatchContext | RouteAction | undefined
// return union.
type PreflightResult struct {
	DispatchContext *DispatchContext
	RouteAction     *RouteAction
}

// IsRouteAction mirrors isOpenAIGatewayRouteAction.
func (r PreflightResult) IsRouteAction() bool { return r.RouteAction != nil }

// PrepareOpenAIGatewayDispatchContext mirrors
// prepareOpenAIGatewayDispatchContext. A nil error with a zero PreflightResult
// mirrors the Node undefined return (the request completed inside a
// preflight step).
func (s *Service) PrepareOpenAIGatewayDispatchContext(ctx context.Context, input PreflightInput) (PreflightResult, error) {
	req, res, auditCapture, options := input.Req, input.Res, input.AuditCapture, input.Options
	var err error
	var (
		gatewaySettings                   *gatewayruntimecache.GatewaySettings
		apiKeyRecord                      = options.APIKeyRecord
		groupFallbackAPIKeyRecord         = options.GroupFallbackAPIKeyRecord
		runtimeGroupAccess                *gatewayruntimecache.GroupUsageAccessMetadata
		runtimeAccounts                   []gatewayruntimecache.OpenAIAccountSecret
		runtimeAccountDispatchDiagnostics *gatewayruntimecache.OpenAIAccountsForGroupDiagnostics
	)
	if groupFallbackAPIKeyRecord == nil {
		groupFallbackAPIKeyRecord = asRecordAlias(apiKeyRecord)
	}
	initialModelsResponseProtocol, hasInitialModelsResponseProtocol := ResolveGatewayModelsResponseProtocol(req)

	if hasInitialModelsResponseProtocol && options.Identity == nil && !options.ForwardModelsRequestToUpstream {
		completed, err := s.handleGatewayModelsRequestBeforeRequiredAuth(ctx, modelsBeforeAuthInput{
			req: req, res: res, auditCapture: auditCapture,
			protocol:  initialModelsResponseProtocol,
			startedAt: input.StartedAt, clientIP: input.ClientIP,
			traceID: input.TraceID, endpoint: input.Endpoint,
		})
		if err != nil {
			return PreflightResult{}, err
		}
		if completed {
			return PreflightResult{}, nil
		}
	}

	if isDirectLoopbackDeploymentSmoke(req) {
		responsePayload := GatewayErrorPayloadOf("部署 smoke 已在网关本地完成，未派发上游", "invalid_request_error", "deployment_smoke_no_upstream")
		s.Responses.SendGatewayFailureResponse(FailureResponseInput{
			Req: req, Res: res, AuditCapture: auditCapture,
			UsageContext: s.BuildGatewayUsageContext(usageContextInput{
				traceID: input.TraceID, clientIP: input.ClientIP,
				identity:        OpenAIGatewayRequestIdentity{SystemAccountID: "deployment-smoke", GroupID: "deployment-smoke"},
				trafficSource:   defaultTrafficSource(options.TrafficSource),
				endpoint:        input.Endpoint,
				requestSnapshot: input.RequestSnapshot,
			}),
			StartedAt:       input.StartedAt,
			StatusCode:      400,
			ResponsePayload: responsePayload,
			RecordUsage:     boolPtr(false),
			Audit: FailureAudit{
				Outcome:      AuditOutcomeGatewayFailed,
				ErrorPhase:   "request_validation",
				ErrorCode:    "deployment_smoke_no_upstream",
				ErrorMessage: responsePayload.Error.Message,
			},
		})
		return PreflightResult{}, nil
	}

	var identity *OpenAIGatewayRequestIdentity
	if options.Identity != nil {
		identity = options.Identity
	} else {
		runtime, err := s.ResolveGatewayRuntimeAsync(ctx, res, req, ResolveGatewayRuntimeOptions{})
		if err != nil {
			return PreflightResult{}, err
		}
		if runtime == nil || runtime.APIKey == nil {
			s.Responses.FinalizeGatewayAuthFailureAudit(req, res, auditCapture)
			return PreflightResult{}, nil
		}
		settings := runtime.Settings
		gatewaySettings = &settings
		apiKeyRecord = runtime.APIKey
		if groupFallbackAPIKeyRecord == nil {
			groupFallbackAPIKeyRecord = asRecordAlias(runtime.APIKey)
		}
		runtimeGroupAccess = runtime.GroupAccess
		runtimeAccounts = runtime.Accounts
		runtimeAccountDispatchDiagnostics = runtime.AccountDispatchDiagnostics
		identity = &OpenAIGatewayRequestIdentity{
			SystemAccountID: runtime.APIKey.SystemAccountID,
			APIKeyID:        runtime.APIKey.ID,
			GroupID:         runtime.APIKey.SelectedGroupID,
		}
	}
	if identity == nil {
		return PreflightResult{}, nil
	}

	trafficSource := defaultTrafficSource(options.TrafficSource)
	gatewayClientIP := ""
	if trafficSource == TrafficSourceGateway {
		gatewayClientIP = input.ClientIP
	}
	systemAccountID, apiKeyID, groupID := identity.SystemAccountID, identity.APIKeyID, identity.GroupID
	apiKeyUnavailable := apiKeyID != "" && apiKeyRecord == nil
	auditCapture.BindContext(AuditGatewayContext{
		SystemAccountID: systemAccountID,
		APIKeyID:        apiKeyID,
		GroupID:         groupID,
		ProviderCode:    ProtocolCodeOpenAI,
		TrafficSource:   trafficSource,
	})

	baseUsageContext := s.BuildGatewayUsageContext(usageContextInput{
		traceID: input.TraceID, clientIP: input.ClientIP, identity: *identity,
		trafficSource: trafficSource, endpoint: input.Endpoint,
		requestSnapshot: input.RequestSnapshot,
	})
	activeSettings := gatewayruntimecache.GatewaySettings{}
	if gatewaySettings != nil {
		activeSettings = *gatewaySettings
	} else {
		activeSettings, err = s.RuntimeCache.ReadCachedGatewaySettingsAsync(ctx)
		if err != nil {
			return PreflightResult{}, err
		}
	}
	activeGatewaySettings, err := mergeGatewaySettings(activeSettings, options.SettingsOverride)
	if err != nil {
		return PreflightResult{}, err
	}
	compactionTimeoutsDisabled := s.Codex.CompactionExpectedForRequest(req)
	requestLane := options.RequestLane
	if requestLane == "" {
		requestLane = gatewayproto.LaneText
	}
	serverRetryBudget := options.ServerRetryBudget
	if serverRetryBudget == nil {
		serverRetryBudget = NewServerRetryBudget(activeGatewaySettings.NoAvailableAccountWaitTimeoutSeconds*1000, s.Clock)
	}
	options.ServerRetryBudget = serverRetryBudget
	gatewayRequestWallBudget := options.GatewayRequestWallBudget
	if gatewayRequestWallBudget == nil {
		var budgetMs *int64
		if requestLane == gatewayproto.LaneImage {
			budgetMs = int64Ptr(activeGatewaySettings.ImageRequestWallTimeoutSeconds * 1000)
		}
		// G19 观测接线收尾：budget precommit_clipped 裁剪观察（进程级槽，
		// 由 cmd/juhe-ai-gateway wireGatewayObservabilityArms 在 chain 装配
		// 时置位；见 wall_budget_observer.go 的时序说明——本构造点逐请求
		// 执行，读槽即取当前装配值，未装配时 nil 保持无观察语义）。
		gatewayRequestWallBudget, err = gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
			RequestAcceptedAtMs: input.StartedAt,
			Unbounded:           compactionTimeoutsDisabled,
			BudgetMs:            budgetMs,
		}, RoutingWallBudgetObserverOf())
		if err != nil {
			return PreflightResult{}, err
		}
	}
	if compactionTimeoutsDisabled {
		gatewayRequestWallBudget = gatewayRequestWallBudget.WithoutLimit()
	}
	if requestLane == gatewayproto.LaneImage {
		gatewayRequestWallBudget, err = gatewayRequestWallBudget.WithMinimumBudgetMs(activeGatewaySettings.ImageRequestWallTimeoutSeconds * 1000)
		if err != nil {
			return PreflightResult{}, err
		}
	}
	options.GatewayRequestWallBudget = gatewayRequestWallBudget
	routeCoordinationBudget := options.RouteCoordinationBudget
	if routeCoordinationBudget == nil {
		routeCoordinationBudget, err = gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{RequestID: input.TraceID})
		if err != nil {
			return PreflightResult{}, err
		}
	}
	options.RouteCoordinationBudget = routeCoordinationBudget
	requestAttemptTracker := options.RequestAttemptTracker
	if requestAttemptTracker == nil {
		requestAttemptTracker, err = gatewayrouting.NewGatewayRequestAttemptTracker(nil)
		if err != nil {
			return PreflightResult{}, err
		}
	}
	options.RequestAttemptTracker = requestAttemptTracker
	downstreamCommitState := options.DownstreamCommitState
	if downstreamCommitState == nil {
		downstreamCommitState = &DownstreamCommitState{}
	}
	options.DownstreamCommitState = downstreamCommitState

	currentGroupUsageContext := func(groupIdOverride string, groupAccess *gatewayruntimecache.GroupUsageAccessMetadata) GatewayFailureUsageContext {
		effectiveGroup := groupID
		if groupIdOverride != "" {
			effectiveGroup = groupIdOverride
		}
		return s.BuildGatewayUsageContext(usageContextInput{
			traceID: input.TraceID, clientIP: input.ClientIP,
			identity:         OpenAIGatewayRequestIdentity{SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: effectiveGroup},
			trafficSource:    trafficSource,
			groupUsageFields: groupUsageFieldsOr(groupAccess, runtimeGroupAccess),
			endpoint:         input.Endpoint,
			requestSnapshot:  input.RequestSnapshot,
		})
	}

	clientIPErrorCircuit, err := s.Circuits.InspectClientIPErrorCircuit(ctx, ClientIPErrorCircuitInput{
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		ClientIP: gatewayClientIP, Endpoint: input.Endpoint,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if s.sendClientIPErrorCircuitGatewayResponse(ctx, clientIPResponseInput{
		req: req, res: res, auditCapture: auditCapture,
		usageContext: currentGroupUsageContext("", nil), startedAt: input.StartedAt,
		circuit: clientIPErrorCircuit, systemAccountID: systemAccountID,
		apiKeyID: apiKeyID, groupID: groupID, clientIP: gatewayClientIP,
	}) {
		return PreflightResult{}, nil
	}
	if s.Responses != nil && s.RejectUnavailableGatewayAPIKey(UnavailableAPIKeyInput{
		Req: req, Res: res, AuditCapture: auditCapture,
		UsageContext: currentGroupUsageContext("", nil), StartedAt: input.StartedAt,
		APIKeyUnavailable: apiKeyUnavailable,
	}) {
		return PreflightResult{}, nil
	}
	if initialBodyState := req.BodyState(); initialBodyState != nil && initialBodyState.JSONParseStatus == gatewaybody.JSONParseStatusInvalidJSON {
		s.SendInvalidJSONGatewayResponse(ctx, InvalidJSONResponseInput{
			Req: req, Res: res, AuditCapture: auditCapture,
			UsageContext: currentGroupUsageContext("", nil), StartedAt: input.StartedAt,
			SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
			ClientIP: gatewayClientIP, Endpoint: input.Endpoint,
		})
		return PreflightResult{}, nil
	}

	var interactionResourceAffinity *gatewaygemini.AffinityBinding
	interactionResourceRequest := req.HTTP != nil && gatewaygemini.IsInteractionResourceRequest(req.HTTP)
	interactionResourceID := ""
	if req.HTTP != nil {
		interactionResourceID = gatewaygemini.ResourceIDFromRequest(req.HTTP)
	}
	if interactionResourceRequest && interactionResourceID == "" {
		s.sendInteractionAffinityFailure(ctx, interactionFailureInput{
			req: req, res: res, auditCapture: auditCapture,
			usageContext: currentGroupUsageContext("", nil), startedAt: input.StartedAt,
			statusCode: 400, code: "interaction_id_invalid",
			message: "Interaction 资源 ID 无效",
		})
		return PreflightResult{}, nil
	}
	if interactionResourceID != "" {
		affinity, found, affinityErr := s.Affinity.Resolve(ctx, req.HTTP, gatewaygemini.AffinityScope{
			SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		})
		if affinityErr != nil {
			s.Observability.Logger().Warn("gateway_gemini_interaction_affinity_lookup_failed", map[string]any{
				"interactionId": interactionResourceID,
				"errorMessage":  affinityErr.Error(),
			}, "Gemini Interaction 账号亲和状态读取失败")
			s.sendInteractionAffinityFailure(ctx, interactionFailureInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: currentGroupUsageContext("", nil), startedAt: input.StartedAt,
				statusCode: 503, code: "interaction_affinity_unavailable",
				message: "Interaction 资源路由状态暂不可用，请稍后重试",
			})
			return PreflightResult{}, nil
		}
		if !found {
			s.sendInteractionAffinityFailure(ctx, interactionFailureInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: currentGroupUsageContext("", nil), startedAt: input.StartedAt,
				statusCode: 404, code: "interaction_affinity_not_found",
				message: "未找到该 Interaction 的本地账号路由记录",
			})
			return PreflightResult{}, nil
		}
		affinityGroupStillBound := apiKeyID == "" || apiKeyRecord == nil || apiKeyHasActiveBindingForGroup(*apiKeyRecord, affinity.GroupID)
		if !affinityGroupStillBound {
			s.sendInteractionAffinityFailure(ctx, interactionFailureInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: currentGroupUsageContext("", nil), startedAt: input.StartedAt,
				statusCode: 404, code: "interaction_affinity_not_found",
				message: "该 Interaction 所属分组已不再绑定当前 API Key",
			})
			return PreflightResult{}, nil
		}
		groupID = affinity.GroupID
		identity = &OpenAIGatewayRequestIdentity{SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID}
		runtimeGroupAccess, err = s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
		if err != nil {
			return PreflightResult{}, err
		}
		mergeStrategy := isMergeRouteStrategy(apiKeyRecord)
		affinityCandidates := options.CandidateAccounts
		if affinityCandidates == nil {
			if mergeStrategy {
				// B20（合并路由设计“交互资源亲和”）：亲和账号可能来自任一
				// 绑定分组——过滤在合并池进行（按绑定扇出列出 + 组标；模型
				// 过滤语义与单组亲和路径一致：不按请求模型预过滤，下游
				// FilterCandidates 对亲和请求 BypassModelFilter）。
				affinityCandidates, err = s.listMergeAffinityCandidatePool(ctx, apiKeyRecord, systemAccountID)
			} else {
				affinityCandidates, err = s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{})
			}
			if err != nil {
				return PreflightResult{}, err
			}
		}
		filtered := make([]gatewayruntimecache.OpenAIAccountSecret, 0)
		for _, account := range affinityCandidates {
			if account.ID == affinity.AccountID &&
				account.ProviderCode == affinity.ProviderCode &&
				(affinity.ProviderProtocolProfileID == "" || account.ProviderProtocolProfileID == affinity.ProviderProtocolProfileID) {
				filtered = append(filtered, account)
			}
		}
		runtimeAccounts = filtered
		runtimeAccountDispatchDiagnostics = nil
		if len(runtimeAccounts) != 1 {
			s.sendInteractionAffinityFailure(ctx, interactionFailureInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: currentGroupUsageContext(groupID, runtimeGroupAccess), startedAt: input.StartedAt,
				statusCode: 409, code: "interaction_affinity_account_unavailable",
				message: "创建该 Interaction 的上游账号当前不可用",
			})
			return PreflightResult{}, nil
		}
		if mergeStrategy {
			// B20：命中后窗口身份取该账号所在片段组（请求级身份与账号组一致）。
			if bound := filtered[0].BoundGroupID; bound != nil && *bound != "" && *bound != groupID {
				groupID = *bound
				identity = &OpenAIGatewayRequestIdentity{SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID}
				runtimeGroupAccess, err = s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
				if err != nil {
					return PreflightResult{}, err
				}
			}
		}
		auditCapture.BindContext(AuditGatewayContext{GroupID: groupID, ProviderCode: affinity.ProviderCode})
		auditCapture.AddGatewayMetadata("gemini_interaction_account_affinity", map[string]any{
			"interactionId": interactionResourceID,
			"groupId":       groupID,
			"accountId":     affinity.AccountID,
		})
		interactionResourceAffinity = &affinity
	}

	initialClientStrategy := s.ClientStrategy.Resolve(req, ClientStrategyInput{
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		Endpoint: input.Endpoint, ClientIP: gatewayClientIP,
	})
	sessionIdentity := resolveSessionIdentityFromStrategy(s, req, initialClientStrategy, systemAccountID, apiKeyID)
	bindAuditSessionIdentity(auditCapture, sessionIdentity, initialClientStrategy.ClientProfile)

	// 可靠性批次2（缺陷3）：options.CandidateAccounts 非 nil 且 Identity 为空
	// 只出现在 chat 调度覆盖（进程内绑定作用域候选注入）——该场景候选已由绑定
	// 作用域收敛（usage 按 BoundGroupID 记账），跳过 Key 策略面的 normal 路由
	// 判定，避免多 provider 分组的专用 chat Key 策略把绑定会话请求按 Key 绑定
	// 面判成 400（model_target_group_not_bound 等）。API Key 分组回退注入的
	// 候选恒带 Identity，外部 /v1 直连不注入候选，两者 normal 路由语义不变。
	if interactionResourceAffinity == nil && !hasInitialModelsResponseProtocol && options.Identity == nil &&
		options.CandidateAccounts == nil &&
		trafficSource == TrafficSourceGateway && apiKeyRecord != nil {
		previousGroupID := groupID
		previousBindingCount := len(apiKeyRecord.GroupBindings)
		normalRoute, err := s.RouteResolver.ResolveNormalGatewayModelRoute(ctx, NormalRouteInput{
			Req: req, APIKeyRecord: apiKeyRecord,
			RequestClientCompatibility: initialClientStrategy.RequestClientCompatibility,
		})
		if err != nil {
			return PreflightResult{}, err
		}
		if normalRoute.Outcome == NormalRouteOutcomeSelected {
			if groupFallbackAPIKeyRecord == nil {
				groupFallbackAPIKeyRecord = asRecordAlias(apiKeyRecord)
			}
			apiKeyRecord = normalRoute.APIKeyRecord
			groupID = normalRoute.GroupID
			identity = &OpenAIGatewayRequestIdentity{SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID}
			runtimeGroupAccess = normalRoute.GroupAccess
			runtimeAccounts = normalRoute.Accounts
			runtimeAccountDispatchDiagnostics = nil
			// 3.4 窗口身份：merge 的窗口组（首个非空片段组）可不同于加载时的
			// SelectedGroupID（首个绑定组）——请求级审计上下文同步取窗口组。
			if isMergeRouteStrategy(apiKeyRecord) {
				auditCapture.BindContext(AuditGatewayContext{GroupID: groupID})
			}
			auditCapture.AddGatewayMetadata("normal_model_route", map[string]any{
				"requestedModel":        normalRoute.RequestedModel,
				"fromGroupId":           previousGroupID,
				"toGroupId":             normalRoute.GroupID,
				"routeSource":           normalRoute.RouteSource,
				"matchedProviderCode":   normalRoute.MatchedProviderCode,
				"sourceBindingCount":    previousBindingCount,
				"candidateBindingCount": recordBindingCount(normalRoute.APIKeyRecord),
			})
			targetCircuit, err := s.Circuits.InspectClientIPErrorCircuit(ctx, ClientIPErrorCircuitInput{
				SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
				ClientIP: gatewayClientIP, Endpoint: input.Endpoint,
			})
			if err != nil {
				return PreflightResult{}, err
			}
			if s.sendClientIPErrorCircuitGatewayResponse(ctx, clientIPResponseInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: currentGroupUsageContext(groupID, runtimeGroupAccess), startedAt: input.StartedAt,
				circuit: targetCircuit, systemAccountID: systemAccountID,
				apiKeyID: apiKeyID, groupID: groupID, clientIP: gatewayClientIP,
			}) {
				return PreflightResult{}, nil
			}
		}
		if normalRoute.Outcome == NormalRouteOutcomeFailed {
			auditCapture.AddGatewayMetadata("normal_model_route_failed", map[string]any{
				"requestedModel":       normalRoute.RequestedModel,
				"reason":               normalRoute.Code,
				"matchedProviderCodes": normalRoute.MatchedProviderCodes,
				"sourceBindingCount":   previousBindingCount,
			})
			responsePayload := GatewayErrorPayloadOf(normalRoute.Message, normalRoute.Type, normalRoute.Code)
			errorPhase := "request_validation"
			if normalRoute.StatusCode >= 500 {
				errorPhase = "dispatch"
			}
			s.Responses.SendGatewayFailureResponse(FailureResponseInput{
				Req: req, Res: res, AuditCapture: auditCapture,
				UsageContext: currentGroupUsageContext("", nil), StartedAt: input.StartedAt,
				StatusCode: normalRoute.StatusCode, ResponsePayload: responsePayload,
				Audit: FailureAudit{
					Outcome: AuditOutcomeGatewayFailed, ErrorPhase: errorPhase,
					ErrorCode: normalRoute.Code, ErrorMessage: normalRoute.Message,
				},
			})
			return PreflightResult{}, nil
		}
	}

	groupAccess := runtimeGroupAccess
	if groupAccess == nil {
		groupAccess, err = s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
		if err != nil {
			return PreflightResult{}, err
		}
	}
	if groupAccess != nil {
		auditCapture.BindContext(AuditGatewayContext{ProviderCode: groupAccess.ProviderCode})
	}
	clientStrategy := s.ClientStrategy.Resolve(req, ClientStrategyInput{
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		Endpoint: input.Endpoint, ProviderCode: groupAccessProviderCode(groupAccess),
		ClientIP: gatewayClientIP,
	})
	if clientStrategy.ClientProfile != initialClientStrategy.ClientProfile {
		sessionIdentity = resolveSessionIdentityFromStrategy(s, req, clientStrategy, systemAccountID, apiKeyID)
	}
	bindAuditSessionIdentity(auditCapture, sessionIdentity, clientStrategy.ClientProfile)

	clientIPAccountAvoidanceTracker := s.AccountAvoidance.CreateTracker(ClientIPAccountAvoidanceInput{
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		ClientIP: gatewayClientIP,
	})
	if clientStrategy.ClientProfile == "codex" || clientStrategy.ClientProfile == "claude_code" || clientStrategy.ClientProfile == "gemini_cli" {
		auditCapture.AddGatewayMetadata("client_strategy", s.ClientStrategy.AuditMetadata(clientStrategy))
	}
	if groupAccess == nil {
		if s.Responses != nil {
			s.RejectMissingGatewayGroupAccess(MissingGroupAccessInput{
				Req: req, Res: res, AuditCapture: auditCapture,
				UsageContext: baseUsageContext, StartedAt: input.StartedAt,
				GroupAccess: nil,
			})
		}
		return PreflightResult{}, nil
	}
	groupUsageFields := GroupUsageMetadata(*groupAccess)
	usageContext := s.BuildGatewayUsageContext(usageContextInput{
		traceID: input.TraceID, clientIP: input.ClientIP, identity: *identity,
		trafficSource: trafficSource, groupUsageFields: &groupUsageFields,
		endpoint: input.Endpoint, requestSnapshot: input.RequestSnapshot,
	})

	if bodyState := req.BodyState(); bodyState != nil && bodyState.JSONParseStatus == gatewaybody.JSONParseStatusInvalidJSON {
		s.SendInvalidJSONGatewayResponse(ctx, InvalidJSONResponseInput{
			Req: req, Res: res, AuditCapture: auditCapture,
			UsageContext: usageContext, StartedAt: input.StartedAt,
			SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
			ClientIP: gatewayClientIP, Endpoint: input.Endpoint,
		})
		return PreflightResult{}, nil
	}

	rejected, err := s.RejectGatewayAPIKeyQuotaIfExceeded(ctx, APIKeyQuotaInput{
		Req: req, Res: res, AuditCapture: auditCapture,
		UsageContext: usageContext, StartedAt: input.StartedAt,
		APIKeyRecord: apiKeyRecord,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if rejected {
		return PreflightResult{}, nil
	}
	rejected, err = s.RejectGatewayAuthorizationQuotaIfExceeded(ctx, AuthorizationQuotaInput{
		Req: req, Res: res, AuditCapture: auditCapture,
		UsageContext: usageContext, StartedAt: input.StartedAt,
		GroupAccess: groupAccess,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if rejected {
		return PreflightResult{}, nil
	}

	modelsResponseProtocol := initialModelsResponseProtocol
	if hasModelsProtocol := hasInitialModelsResponseProtocol; hasModelsProtocol && trafficSource == TrafficSourceGateway && apiKeyID != "" {
		rateLimitDecision, err := s.ModelsRateLimit.Consume(ctx, AuthenticatedModelsRateLimitInput{
			APIKeyID: apiKeyID, ClientIP: gatewayClientIP,
		})
		if err != nil {
			return PreflightResult{}, err
		}
		if !rateLimitDecision.Allowed {
			s.sendAuthenticatedModelsRateLimitFailure(ctx, modelsRateLimitFailureInput{
				req: req, res: res, auditCapture: auditCapture, usageContext: usageContext,
				startedAt: input.StartedAt, decision: rateLimitDecision,
			})
			return PreflightResult{}, nil
		}
	}

	if hasInitialModelsResponseProtocol && !options.ForwardModelsRequestToUpstream {
		if err := s.Circuits.RecordClientIPErrorCircuitSuccess(ctx, ClientIPErrorCircuitInput{
			SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
			ClientIP: gatewayClientIP, Endpoint: input.Endpoint,
		}); err != nil {
			return PreflightResult{}, err
		}
		modelsBindings := gatewayModelsBindings(apiKeyRecord)
		var modelsSendErr error
		switch modelsResponseProtocol {
		case ResponseProtocolAnthropicV:
			modelsSendErr = s.Responses.SendAnthropicModelsGatewayResponse(ModelsResponseInput{
				Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
				Bindings: modelsBindings, Context: ctx, StartedAt: input.StartedAt,
			})
		case ResponseProtocolGeminiV:
			modelsSendErr = s.Responses.SendGeminiModelsGatewayResponse(ModelsResponseInput{
				Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
				Bindings: modelsBindings, Context: ctx, StartedAt: input.StartedAt,
			})
		default:
			modelsSendErr = s.Responses.SendOpenAIModelsGatewayResponse(ModelsResponseInput{
				Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
				Bindings: modelsBindings, Context: ctx, StartedAt: input.StartedAt,
			})
		}
		if modelsSendErr != nil {
			// 装载失败：与 before-auth 分支同一收尾 helper，返回已完成状态，
			// 禁止裸 error 落到外层 generic 500、重复写响应或进入上游派发
			//（设计 4.6.4）。
			s.finishGatewayModelsLoadFailure(modelsLoadFailureInput{
				req: req, res: res, auditCapture: auditCapture,
				usageContext: usageContext, startedAt: input.StartedAt,
				traceID: input.TraceID,
			}, gatewayModelsErrorProtocol(modelsResponseProtocol), modelsSendErr)
		}
		return PreflightResult{}, nil
	}

	rawCandidateAccounts := options.CandidateAccounts
	candidateSource := "cache"
	if rawCandidateAccounts != nil {
		candidateSource = "provided"
	} else if runtimeAccounts != nil {
		rawCandidateAccounts = runtimeAccounts
		candidateSource = "runtime"
	} else {
		rawCandidateAccounts, err = s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{})
		if err != nil {
			return PreflightResult{}, err
		}
	}
	if options.CandidateAccounts == nil && runtimeAccountDispatchDiagnostics != nil {
		diagnostics := map[string]any{
			"returnedCandidateCount": len(rawCandidateAccounts),
		}
		if runtimeAccountDispatchDiagnostics != nil {
			diagnostics["scanLimit"] = runtimeAccountDispatchDiagnostics.ScanLimit
			diagnostics["finalLimit"] = runtimeAccountDispatchDiagnostics.FinalLimit
			diagnostics["candidateRowCount"] = runtimeAccountDispatchDiagnostics.CandidateRowCount
			diagnostics["scannedRowCount"] = runtimeAccountDispatchDiagnostics.ScannedRowCount
			diagnostics["eligibleRowCount"] = runtimeAccountDispatchDiagnostics.EligibleRowCount
			diagnostics["hydrationBatchCount"] = runtimeAccountDispatchDiagnostics.HydrationBatchCount
			diagnostics["hydratedAccountCount"] = runtimeAccountDispatchDiagnostics.HydratedAccountCount
			diagnostics["hydrationDroppedCount"] = runtimeAccountDispatchDiagnostics.HydrationDroppedCount
			diagnostics["finalAccountCount"] = runtimeAccountDispatchDiagnostics.FinalAccountCount
			diagnostics["scanLimitReached"] = runtimeAccountDispatchDiagnostics.ScanLimitReached
		}
		auditCapture.AddGatewayMetadata("account_dispatch_candidate_window", diagnostics)
	}
	sessionAffinityScope := SessionAffinityScope{
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID, GroupID: groupID,
		RouteStrategyID:           recordRouteStrategyID(apiKeyRecord),
		ProviderProtocolProfileID: gatewayAffinityProviderProfilePool(rawCandidateAccounts),
	}
	rawSessionAffinityKey := ""
	if key, ok := s.SessionAffinity.ResolveKeyFromClientSource(clientStrategy.ClientSource, sessionAffinityScope); ok {
		rawSessionAffinityKey = key
	} else if key, ok := s.SessionAffinity.ResolveKey(sessionIdentity, sessionAffinityScope); ok {
		rawSessionAffinityKey = key
	}
	sessionAffinityKey := rawSessionAffinityKey
	if options.DisableSessionAffinity {
		sessionAffinityKey = ""
	}
	s.Observability.LogRequestStage("account.load_candidates", map[string]any{
		"traceId":               input.TraceID,
		"groupId":               groupID,
		"source":                candidateSource,
		"candidateAccountCount": len(rawCandidateAccounts),
	}, "success", s.StartedAt())

	codexCompleted, err := s.Codex.ApplyContextStatePreflight(ctx, CodexContextStateInput{
		Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
		StartedAt: input.StartedAt, SystemAccountID: systemAccountID,
		APIKeyID: apiKeyID, GroupID: groupID, GroupAccess: *groupAccess,
		Signal: input.Signal,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if codexCompleted {
		return PreflightResult{}, nil
	}

	routePlanSnapshot := options.RoutePlanSnapshot
	if routePlanSnapshot == nil {
		snapshot, snapshotErr := s.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
			traceID: input.TraceID, startedAt: input.StartedAt, groupId: groupID,
			apiKeyRecord:               firstNonNilRecord(groupFallbackAPIKeyRecord, apiKeyRecord),
			gatewayRequestWallBudget:   gatewayRequestWallBudget,
			normalRouteFirstByteConfig: s.normalRouteFirstByteConfigForAPIKey(apiKeyRecord, requestLane, compactionTimeoutsDisabled, options.NormalRouteFirstByteConfig),
		})
		if snapshotErr != nil {
			return PreflightResult{}, snapshotErr
		}
		routePlanSnapshot = &snapshot
	}

	pendingRouteReason := ""
	var pendingRouteFailure *gatewayrouting.GatewayRouteFinalFailure
	buildRouteAction := func(reason ...string) RouteAction {
		actionReason := pendingRouteReason
		if len(reason) > 0 && reason[0] != "" {
			actionReason = reason[0]
		} else if actionReason == "" && pendingRouteFailure != nil {
			actionReason = pendingRouteFailure.ErrorCode
		}
		if actionReason == "" {
			actionReason = "route_unavailable"
		}
		coordination := RouteActionCoordination{Outcome: "hard_exhausted", Reason: actionReason, BlockedAccountIDs: []string{}}
		if pendingRouteFailure != nil && isTemporarilyBlockedRouteFailure(pendingRouteFailure) {
			coordination = RouteActionCoordination{
				Outcome: "temporarily_blocked", Reason: actionReason,
				ConfirmationInFlight: false, BlockedAccountIDs: []string{},
				WaitableByCurrentRequest: false, ForeignLeaseInFlight: false,
			}
			if pendingRouteFailure.RetryAfterMs != nil {
				earliest := s.NowMs() + *pendingRouteFailure.RetryAfterMs
				coordination.EarliestRetryAtMs = &earliest
			}
		}
		return RouteAction{
			Coordination: coordination, Failure: pendingRouteFailure,
			UsageContext: usageContext, APIKeyRecord: apiKeyRecord,
			GroupFallbackAPIKeyRecord: groupFallbackAPIKeyRecord,
			RequestLane:               requestLane, ClientStrategy: clientStrategy,
			SessionIdentity: sessionIdentity, ServerRetryBudget: serverRetryBudget,
			GatewayRequestWallBudget:    gatewayRequestWallBudget,
			RouteCoordinationBudget:     routeCoordinationBudget,
			RequestAttemptTracker:       requestAttemptTracker,
			DownstreamCommitState:       downstreamCommitState,
			RoutePlanSnapshot:           *routePlanSnapshot,
			InteractionResourceAffinity: interactionResourceAffinity,
			NormalRouteFirstByteConfig:  s.normalRouteFirstByteConfigForAPIKey(apiKeyRecord, requestLane, compactionTimeoutsDisabled, options.NormalRouteFirstByteConfig),
		}
	}
	routeCoordinator := newPreflightRouteCoordinator(s, ctx, &preflightCoordinatorState{
		interactionResourceAffinity: interactionResourceAffinity,
		apiKeyRecord:                &apiKeyRecord,
		groupID:                     &groupID,
		requestLane:                 &requestLane,
		requestClientCompatibility:  &clientStrategy.RequestClientCompatibility,
		routePlanSnapshot:           &routePlanSnapshot,
		pendingRouteReason:          &pendingRouteReason,
		pendingRouteFailure:         &pendingRouteFailure,
	})

	candidateFilter, err := s.Candidates.FilterCandidates(ctx, CandidateFilterInput{
		Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
		StartedAt: input.StartedAt, RawCandidates: rawCandidateAccounts,
		ClientStrategy: clientStrategy, SystemAccountID: systemAccountID,
		APIKeyID: apiKeyID, GroupID: groupID, ClientIP: gatewayClientIP,
		Endpoint:                        input.Endpoint,
		BypassModelFilter:               interactionResourceAffinity != nil || options.ForwardModelsRequestToUpstream,
		RequestModelOverride:            probeModelOverride(options),
		LoadModelAwareCandidateAccounts: candidateLoader(s, options, interactionResourceAffinity, apiKeyRecord, groupID, systemAccountID),
		RecoverUnavailableCandidateAccounts: recoverableLoader(s, options, interactionResourceAffinity, apiKeyRecord, recoveryInput{
			req: req, auditCapture: auditCapture, systemAccountID: systemAccountID,
			apiKeyID: apiKeyID, groupID: groupID, startedAt: input.StartedAt,
			serverRetryBudget: serverRetryBudget, routeCoordinationBudget: routeCoordinationBudget,
			gatewayRequestWallBudget: gatewayRequestWallBudget, signal: input.Signal,
		}),
		RouteCoordinator: routeCoordinator,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	switch candidateFilter.Outcome {
	case CandidateOutcomeFallback:
		return PreflightResult{RouteAction: func() *RouteAction { action := buildRouteAction(candidateFilter.Reason); return &action }()}, nil
	case CandidateOutcomeCompleted:
		if pendingRouteFailure != nil {
			return PreflightResult{RouteAction: func() *RouteAction { action := buildRouteAction(); return &action }()}, nil
		}
		return PreflightResult{}, nil
	}

	if requestLane != gatewayproto.LaneImage {
		targetsImage, err := s.accountModelsTargetImage(ctx, req, candidateFilter.Accounts, systemAccountID)
		if err != nil {
			return PreflightResult{}, err
		}
		if targetsImage {
			requestLane = gatewayproto.LaneImage
		}
	}
	imagePermissionPreflight, err := s.Images.Apply(ctx, ImagePermissionPreflightInput{
		Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
		StartedAt: input.StartedAt, APIKeyRecord: apiKeyRecord,
		RequestLane: string(requestLane), SystemAccountID: systemAccountID,
		APIKeyID: apiKeyID, GroupID: groupID, ClientIP: gatewayClientIP,
		Endpoint:                         input.Endpoint,
		GatewayTextRawBodyLimitMegabytes: int64Ptr(activeGatewaySettings.GatewayTextRawBodyLimitMegabytes),
		DeferForcedImageGenerationTool:   s.shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(req, candidateFilter.Accounts, clientStrategy.RequestClientCompatibility),
		Signal:                           input.Signal,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if imagePermissionPreflight.Completed {
		return PreflightResult{}, nil
	}
	requestLane = gatewayproto.RequestLane(imagePermissionPreflight.RequestLane)
	if requestLane == gatewayproto.LaneImage {
		gatewayRequestWallBudget, err = gatewayRequestWallBudget.WithMinimumBudgetMs(activeGatewaySettings.ImageRequestWallTimeoutSeconds * 1000)
		if err != nil {
			return PreflightResult{}, err
		}
		options.GatewayRequestWallBudget = gatewayRequestWallBudget
		updated := *routePlanSnapshot
		updated.GatewayRequestWallBudgetMs = gatewayRequestWallBudget.BudgetMs
		updated.FirstByteDeadlineMs = nil
		updated.RequestPrecommitDeadlineAtMs = gatewayRequestWallBudget.DeadlineAtMs
		routePlanSnapshot = &updated
		options.RoutePlanSnapshot = routePlanSnapshot
	}
	normalRouteFirstByteConfig := s.normalRouteFirstByteConfigForAPIKey(apiKeyRecord, requestLane, compactionTimeoutsDisabled, options.NormalRouteFirstByteConfig)
	normalRouteSpeedFirstConfig := s.normalRouteSpeedFirstConfigForAPIKey(apiKeyRecord, requestLane, compactionTimeoutsDisabled)
	if compactionTimeoutsDisabled {
		auditCapture.AddGatewayMetadata("codex_compaction_timeouts_disabled", map[string]any{
			"requestLane":                   string(requestLane),
			"wallBudgetDisabled":            true,
			"firstResponseTimeoutsDisabled": true,
			"firstOutputTimeoutsDisabled":   true,
			"attemptLifetimeDisabled":       true,
			"rawStreamIdleTimeoutRetained":  true,
		})
	}

	dispatchPreparation, err := s.Candidates.PrepareDispatchAccounts(ctx, DispatchPreparationInput{
		Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
		StartedAt: input.StartedAt, CandidateAccounts: candidateFilter.Accounts,
		ModelPriority: candidateFilter.ModelPriority, SessionAffinityKey: sessionAffinityKey,
		// W1b 续：预过滤（能力/模型）跳过明细透传给决策摘要。
		PreFilterSkipped: candidateFilter.PreFilterSkipped,
		GroupAccess:      *groupAccess, SystemAccountID: systemAccountID,
		APIKeyID: apiKeyID, GroupID: groupID,
		RouteStrategyID:             recordRouteStrategyID(apiKeyRecord),
		NormalRouteSpeedFirstConfig: normalRouteSpeedFirstConfig,
		ClientIP:                    gatewayClientIP, ClientStrategy: clientStrategy,
		RequestLane: string(requestLane), ServerRetryBudget: serverRetryBudget,
		RouteCoordinationBudget:         routeCoordinationBudget,
		GatewayRequestWallBudget:        gatewayRequestWallBudget,
		Signal:                          input.Signal,
		IgnoreAccountRuntimeSuppression: options.IgnoreAccountRuntimeSuppression,
		RouteCoordinator:                routeCoordinator,
		// B14/T4：merge 上下文跳过窗口级配额门（解析期逐片段批查是权威门）。
		SkipGroupQuotaWindowCheck: isMergeRouteStrategy(apiKeyRecord),
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if dispatchPreparation.Outcome == CandidateOutcomeFallback {
		return PreflightResult{RouteAction: func() *RouteAction { action := buildRouteAction(dispatchPreparation.Reason); return &action }()}, nil
	}
	if dispatchPreparation.Outcome == CandidateOutcomeCompleted {
		if pendingRouteFailure != nil {
			return PreflightResult{RouteAction: func() *RouteAction { action := buildRouteAction(); return &action }()}, nil
		}
		return PreflightResult{}, nil
	}
	var settleHotQuality func(outcome string) error
	if dispatchPreparation.SettleHotQualityExplorationAfterDispatch != nil {
		settle := onceSettle(dispatchPreparation.SettleHotQualityExplorationAfterDispatch)
		settleHotQuality = settle
	}

	compactPreflight, err := s.Codex.ApplyChatBridgeCompactPreflight(ctx, CodexCompactPreflightInput{
		Req: req, Res: res, AuditCapture: auditCapture, UsageContext: usageContext,
		StartedAt: input.StartedAt, SystemAccountID: systemAccountID,
		APIKeyID: apiKeyID, GroupID: groupID, GroupAccess: *groupAccess,
		RequestClientCompatibility: clientStrategy.RequestClientCompatibility,
		DispatchAccounts:           dispatchPreparation.Accounts,
		ActiveGatewaySettings:      activeGatewaySettings,
		ClientIPAccountAvoidance:   clientIPAccountAvoidanceTracker,
		ModelPriority:              candidateFilter.ModelPriority,
		RequestLane:                string(requestLane),
		GroupSchedulingPolicy:      groupAccess.SchedulingPolicy,
		RequestCoordination: CodexRequestCoordination{
			Scope:                    "gateway_request",
			ServerRetryBudget:        serverRetryBudget,
			GatewayRequestWallBudget: gatewayRequestWallBudget,
			RouteCoordinationBudget:  routeCoordinationBudget,
			RequestAttemptTracker:    requestAttemptTracker,
		},
		OnDispatchedAccount: func(account gatewayruntimecache.OpenAIAccountSecret) {
			if settleHotQuality == nil {
				return
			}
			outcome := "not_dispatched"
			if dispatchPreparation.HotQualityExplorationReservation != nil &&
				dispatchPreparation.HotQualityExplorationReservation.AccountRuntimeKey == gatewayAccountRuntimeKey(account) {
				outcome = "dispatched"
			}
			// 热度探索结算失败不允许静默：探索预约/热度样本因此悬空。真实
			// scopeKey 由 gatewayhotquality 内部构造，本层不可达，线索以
			// groupId + systemAccountId 承载（scope 同源）。
			if err := settleHotQuality(outcome); err != nil {
				slog.Warn("热度质量探索结算失败，探索预约保持未结算",
					"event", "gateway_hot_quality_settlement_failed",
					"accountId", account.ID,
					"groupId", groupID,
					"systemAccountId", systemAccountID,
					"outcome", outcome,
					"error", err.Error())
			}
		},
		Signal: input.Signal,
	})
	if err != nil {
		return PreflightResult{}, err
	}
	if compactPreflight.Completed {
		if settleHotQuality != nil {
			// 结算失败告警口径与 OnDispatchedAccount 侧一致（探索预约悬空）。
			if err := settleHotQuality("not_dispatched"); err != nil {
				slog.Warn("热度质量探索结算失败，探索预约保持未结算",
					"event", "gateway_hot_quality_settlement_failed",
					"groupId", groupID,
					"systemAccountId", systemAccountID,
					"outcome", "not_dispatched",
					"error", err.Error())
			}
		}
		if dispatchPreparation.ReleaseClientIPConcurrency != nil {
			dispatchPreparation.ReleaseClientIPConcurrency()
		}
		return PreflightResult{}, nil
	}
	runtimeResponseInspectionPolicies, err := s.RuntimeCache.ListCachedActiveResponseInspectionPoliciesForAccountsAsync(ctx, compactPreflight.Accounts)
	if err != nil {
		if settleHotQuality != nil {
			// 结算失败告警口径与 OnDispatchedAccount 侧一致（探索预约悬空）；
			// 本分支随后按缓存读失败退出，结算悬空不得无声。
			if err := settleHotQuality("not_dispatched"); err != nil {
				slog.Warn("热度质量探索结算失败，探索预约保持未结算",
					"event", "gateway_hot_quality_settlement_failed",
					"groupId", groupID,
					"systemAccountId", systemAccountID,
					"outcome", "not_dispatched",
					"error", err.Error())
			}
		}
		if dispatchPreparation.ReleaseClientIPConcurrency != nil {
			dispatchPreparation.ReleaseClientIPConcurrency()
		}
		return PreflightResult{}, err
	}
	options.ResponseInspectionPolicies = runtimeResponseInspectionPolicies

	return PreflightResult{DispatchContext: &DispatchContext{
		ActiveGatewaySettings:                    activeGatewaySettings,
		UsageContext:                             usageContext,
		Accounts:                                 compactPreflight.Accounts,
		SessionIdentity:                          sessionIdentity,
		SessionAffinityKey:                       sessionAffinityKey,
		ClientStrategy:                           clientStrategy,
		ClientIPAccountAvoidance:                 clientIPAccountAvoidanceTracker,
		ModelPriority:                            candidateFilter.ModelPriority,
		RequestLane:                              requestLane,
		GroupSchedulingPolicy:                    groupAccess.SchedulingPolicy,
		NormalRouteFirstByteConfig:               normalRouteFirstByteConfig,
		NormalRouteSpeedFirstConfig:              normalRouteSpeedFirstConfig,
		ResponseInspectionPolicies:               orEmptyPolicies(runtimeResponseInspectionPolicies),
		APIKeyRecord:                             apiKeyRecord,
		GroupFallbackAPIKeyRecord:                groupFallbackAPIKeyRecord,
		SchedulingExclusions:                     dispatchPreparation.SchedulingExclusions,
		DispatchSegments:                         dispatchPreparation.DispatchSegments,
		PrecheckHalfOpenEligible:                 dispatchPreparation.PrecheckHalfOpenEligible,
		ServerRetryBudget:                        serverRetryBudget,
		GatewayRequestWallBudget:                 gatewayRequestWallBudget,
		RouteCoordinationBudget:                  routeCoordinationBudget,
		RequestAttemptTracker:                    requestAttemptTracker,
		DownstreamCommitState:                    downstreamCommitState,
		RoutePlanSnapshot:                        *routePlanSnapshot,
		InteractionResourceAffinity:              interactionResourceAffinity,
		HotQualityExplorationReservation:         dispatchPreparation.HotQualityExplorationReservation,
		SettleHotQualityExplorationAfterDispatch: settleHotQuality,
		ReleaseClientIPConcurrency:               dispatchPreparation.ReleaseClientIPConcurrency,
	}}, nil
}

// ---------------------------------------------------------------------------
// inline helpers of preflight.ts
// ---------------------------------------------------------------------------

// isDirectLoopbackDeploymentSmoke mirrors isDirectLoopbackDeploymentSmoke.
func isDirectLoopbackDeploymentSmoke(req *GatewayRequest) bool {
	if req.Header("x-juhe-deployment-smoke") != "no-upstream" || req.Header("x-forwarded-for") != "" {
		return false
	}
	remoteAddress := req.RemoteAddr
	host := remoteAddress
	if index := strings.LastIndex(remoteAddress, ":"); index > strings.LastIndex(remoteAddress, "]") {
		host = remoteAddress[:index]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || host == "::ffff:127.0.0.1"
}

// uniqueActiveRouteGroupIds mirrors uniqueActiveRouteGroupIds.
func uniqueActiveRouteGroupIds(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, binding := range recordBindings(apiKeyRecord) {
		groupID := strings.TrimSpace(binding.GroupID)
		if groupID == "" || binding.Status != "active" || binding.GroupEnabled == 0 || seen[groupID] {
			continue
		}
		seen[groupID] = true
		result = append(result, groupID)
	}
	return result
}

// mergeGatewaySettings mirrors mergeGatewaySettings: the override wins field
// by field and streamCircuitBreakerEnabled stays pinned to true.
func mergeGatewaySettings(base gatewayruntimecache.GatewaySettings, override *gatewayruntimecache.GatewaySettings) (gatewayruntimecache.GatewaySettings, error) {
	if override == nil {
		return gatewayruntimecache.CloneGatewaySettings(base), nil
	}
	merged := base
	if override.GatewayTextRawBodyLimitMegabytes != 0 {
		merged.GatewayTextRawBodyLimitMegabytes = override.GatewayTextRawBodyLimitMegabytes
	}
	if override.AccountCircuitConfirmationFailuresRequired != 0 {
		merged.AccountCircuitConfirmationFailuresRequired = override.AccountCircuitConfirmationFailuresRequired
	}
	if override.GatewayUserRequestLimitPerMinute != nil {
		merged.GatewayUserRequestLimitPerMinute = override.GatewayUserRequestLimitPerMinute
	}
	if override.GatewayUserRequestLimitPerDay != nil {
		merged.GatewayUserRequestLimitPerDay = override.GatewayUserRequestLimitPerDay
	}
	if override.GatewayUserRequestLimitPerWeek != nil {
		merged.GatewayUserRequestLimitPerWeek = override.GatewayUserRequestLimitPerWeek
	}
	if override.GatewayUserRequestLimitPerMonth != nil {
		merged.GatewayUserRequestLimitPerMonth = override.GatewayUserRequestLimitPerMonth
	}
	if override.UsageStatsTimezone != "" {
		merged.UsageStatsTimezone = override.UsageStatsTimezone
	}
	if override.DefaultTemporaryUnschedulableMinutes != 0 {
		merged.DefaultTemporaryUnschedulableMinutes = override.DefaultTemporaryUnschedulableMinutes
	}
	if override.TemporaryUnschedulableRetryIntervalSeconds != 0 {
		merged.TemporaryUnschedulableRetryIntervalSeconds = override.TemporaryUnschedulableRetryIntervalSeconds
	}
	if override.TemporaryUnschedulableRetryAttempts != 0 {
		merged.TemporaryUnschedulableRetryAttempts = override.TemporaryUnschedulableRetryAttempts
	}
	if override.TextFirstResponseTimeoutSeconds != 0 {
		merged.TextFirstResponseTimeoutSeconds = override.TextFirstResponseTimeoutSeconds
	}
	if override.TextNonStreamFirstResponseTimeoutSeconds != 0 {
		merged.TextNonStreamFirstResponseTimeoutSeconds = override.TextNonStreamFirstResponseTimeoutSeconds
	}
	if override.TextStreamIdleTimeoutSeconds != 0 {
		merged.TextStreamIdleTimeoutSeconds = override.TextStreamIdleTimeoutSeconds
	}
	if override.TextUncommittedAttemptMaxLifetimeSeconds != 0 {
		merged.TextUncommittedAttemptMaxLifetimeSeconds = override.TextUncommittedAttemptMaxLifetimeSeconds
	}
	if override.ImageFirstResponseTimeoutSeconds != 0 {
		merged.ImageFirstResponseTimeoutSeconds = override.ImageFirstResponseTimeoutSeconds
	}
	if override.ImageStreamIdleTimeoutSeconds != 0 {
		merged.ImageStreamIdleTimeoutSeconds = override.ImageStreamIdleTimeoutSeconds
	}
	if override.ImageUncommittedAttemptMaxLifetimeSeconds != 0 {
		merged.ImageUncommittedAttemptMaxLifetimeSeconds = override.ImageUncommittedAttemptMaxLifetimeSeconds
	}
	if override.ImageRequestWallTimeoutSeconds != 0 {
		merged.ImageRequestWallTimeoutSeconds = override.ImageRequestWallTimeoutSeconds
	}
	if override.AudioFirstResponseTimeoutSeconds != 0 {
		merged.AudioFirstResponseTimeoutSeconds = override.AudioFirstResponseTimeoutSeconds
	}
	if override.VideoCreateTimeoutSeconds != 0 {
		merged.VideoCreateTimeoutSeconds = override.VideoCreateTimeoutSeconds
	}
	if override.NoAvailableAccountWaitTimeoutSeconds != 0 {
		merged.NoAvailableAccountWaitTimeoutSeconds = override.NoAvailableAccountWaitTimeoutSeconds
	}
	if override.StreamFailureThresholdCount != 0 {
		merged.StreamFailureThresholdCount = override.StreamFailureThresholdCount
	}
	if override.StreamFailureThresholdWindowMinutes != 0 {
		merged.StreamFailureThresholdWindowMinutes = override.StreamFailureThresholdWindowMinutes
	}
	merged.StreamCircuitBreakerEnabled = true
	return merged, nil
}

// isTemporarilyBlockedRouteFailure mirrors isTemporarilyBlockedRouteFailure.
func isTemporarilyBlockedRouteFailure(failure *gatewayrouting.GatewayRouteFinalFailure) bool {
	if failure == nil {
		return false
	}
	return failure.RetryAfterMs != nil ||
		failure.FailureAttribution == "gateway_capacity" ||
		failure.StatusCode == 429
}

// sendClientIPErrorCircuitGatewayResponse mirrors sendClientIpErrorCircuitGatewayResponse.
func (s *Service) sendClientIPErrorCircuitGatewayResponse(_ context.Context, input clientIPResponseInput) bool {
	if !input.circuit.Blocked {
		return false
	}
	statusCode := 429
	responsePayload := GatewayErrorPayloadOf("当前来源短时间错误过多，请稍后重试", "rate_limit_exceeded", "client_ip_error_circuit_open")
	if input.circuit.RetryAfterSeconds != nil && !input.res.HeadersSent() {
		input.res.Header().Set("Retry-After", formatInt64(*input.circuit.RetryAfterSeconds))
	}
	s.Observability.Logger().Warn("gateway_client_ip_error_circuit_blocked", map[string]any{
		"reason":            input.circuit.Reason,
		"retryAfterSeconds": input.circuit.RetryAfterSeconds,
		"failureCount":      input.circuit.FailureCount,
		"systemAccountId":   input.systemAccountID,
		"apiKeyId":          input.apiKeyID,
		"groupId":           input.groupID,
		"clientIp":          input.clientIP,
	}, "客户端 IP 级错误熔断已短路请求")
	input.auditCapture.AddGatewayMetadata("client_ip_error_circuit", map[string]any{
		"blocked":           true,
		"reason":            input.circuit.Reason,
		"retryAfterSeconds": input.circuit.RetryAfterSeconds,
		"failureCount":      input.circuit.FailureCount,
	})
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req: input.req, Res: input.res, AuditCapture: input.auditCapture,
		UsageContext: input.usageContext, StartedAt: input.startedAt,
		StatusCode: statusCode, ResponsePayload: responsePayload,
		Audit: FailureAudit{
			Outcome: AuditOutcomeGatewayFailed, ErrorPhase: "security",
			ErrorCode: "client_ip_error_circuit_open", ErrorMessage: responsePayload.Error.Message,
		},
	})
	return true
}

type clientIPResponseInput struct {
	req             *GatewayRequest
	res             GatewayResponseWriter
	auditCapture    AuditCaptureContext
	usageContext    GatewayFailureUsageContext
	startedAt       int64
	circuit         CircuitDecision
	systemAccountID string
	apiKeyID        string
	groupID         string
	clientIP        string
}

// sendInteractionAffinityFailure mirrors sendInteractionAffinityFailure.
func (s *Service) sendInteractionAffinityFailure(_ context.Context, input interactionFailureInput) {
	responsePayload := GatewayErrorPayloadOf(input.message, "invalid_request_error", input.code)
	errorPhase := "request_validation"
	if input.statusCode >= 500 {
		errorPhase = "dispatch"
	}
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req: input.req, Res: input.res, AuditCapture: input.auditCapture,
		UsageContext: input.usageContext, StartedAt: input.startedAt,
		StatusCode: input.statusCode, ResponsePayload: responsePayload,
		Audit: FailureAudit{
			Outcome: AuditOutcomeGatewayFailed, ErrorPhase: errorPhase,
			ErrorCode: input.code, ErrorMessage: input.message,
		},
	})
}

type interactionFailureInput struct {
	req          *GatewayRequest
	res          GatewayResponseWriter
	auditCapture AuditCaptureContext
	usageContext GatewayFailureUsageContext
	startedAt    int64
	statusCode   int
	code         string
	message      string
}

// gatewayModelsBindings 替代 gatewayModelsProviderCodes（设计 6.4）：同一
// 激活绑定遍历，返回 (groupID, providerCode) 对——按 (groupID, providerCode)
// 去重，保持绑定顺序，供 /v1/models 成员装载端口使用。
func gatewayModelsBindings(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) []GatewayModelBinding {
	seen := map[string]bool{}
	bindings := []GatewayModelBinding{}
	for _, binding := range recordBindings(apiKeyRecord) {
		if binding.Status != "active" {
			continue
		}
		providerCode := strings.TrimSpace(binding.ProviderCode)
		groupID := strings.TrimSpace(binding.GroupID)
		if providerCode == "" {
			continue
		}
		key := groupID + "\x00" + providerCode
		if seen[key] {
			continue
		}
		seen[key] = true
		bindings = append(bindings, GatewayModelBinding{GroupID: groupID, ProviderCode: providerCode})
	}
	return bindings
}

// modelsResponseKind mirrors modelsResponseKind.
func modelsResponseKind(protocol ResponseProtocolCode) string {
	switch protocol {
	case ResponseProtocolAnthropicV:
		return "anthropic"
	case ResponseProtocolGeminiV:
		return "gemini"
	default:
		return "openai"
	}
}

type modelsBeforeAuthInput struct {
	req          *GatewayRequest
	res          GatewayResponseWriter
	auditCapture AuditCaptureContext
	protocol     ResponseProtocolCode
	startedAt    int64
	clientIP     string
	traceID      string
	endpoint     string
}

// handleGatewayModelsRequestBeforeRequiredAuth mirrors
// handleGatewayModelsRequestBeforeRequiredAuth; completed=true means the
// request finished here.
func (s *Service) handleGatewayModelsRequestBeforeRequiredAuth(ctx context.Context, input modelsBeforeAuthInput) (bool, error) {
	apiKey, err := s.ResolveGatewayAPIKeyForModelsAsync(ctx, input.res, input.req, ResolveGatewayRuntimeOptions{
		InspectClientIPPolicyAfterRuntime: boolPtr(false),
	})
	if err != nil {
		return false, err
	}
	if apiKey == nil {
		s.Responses.FinalizeGatewayAuthFailureAudit(input.req, input.res, input.auditCapture)
		return true, nil
	}
	usageContext := GatewayFailureUsageContext{
		TraceID: input.traceID, TrafficSource: TrafficSourceGateway,
		ClientIP: input.clientIP, SystemAccountID: apiKey.SystemAccountID,
		APIKeyID: apiKey.ID, Endpoint: input.endpoint,
	}
	input.auditCapture.BindContext(AuditGatewayContext{
		SystemAccountID: apiKey.SystemAccountID,
		APIKeyID:        apiKey.ID,
		GroupID:         apiKey.SelectedGroupID,
		TrafficSource:   TrafficSourceGateway,
	})
	rateLimitDecision, err := s.ModelsRateLimit.Consume(ctx, AuthenticatedModelsRateLimitInput{
		APIKeyID: apiKey.ID, ClientIP: input.clientIP,
	})
	if err != nil {
		return false, err
	}
	if !rateLimitDecision.Allowed {
		s.sendAuthenticatedModelsRateLimitFailure(ctx, modelsRateLimitFailureInput{
			req: input.req, res: input.res, auditCapture: input.auditCapture,
			usageContext: usageContext, startedAt: input.startedAt,
			decision: rateLimitDecision,
		})
		return true, nil
	}
	if err := s.Responses.SendAuthenticatedModelsGatewayResponse(ModelsResponseInput{
		Req: input.req, Res: input.res, AuditCapture: input.auditCapture,
		UsageContext: usageContext,
		Bindings:     gatewayModelsBindings(apiKey),
		Context:      ctx,
		Protocol:     modelsResponseKind(input.protocol),
		StartedAt:    input.startedAt,
	}); err != nil {
		// 装载失败：协议化错误收尾后就地完成请求，禁止裸 error 落到外层
		// generic 500 或继续链路（设计 4.6.4）。
		s.finishGatewayModelsLoadFailure(modelsLoadFailureInput{
			req: input.req, res: input.res, auditCapture: input.auditCapture,
			usageContext: usageContext, startedAt: input.startedAt,
			traceID: input.traceID,
		}, gatewayModelsErrorProtocol(input.protocol), err)
	}
	return true, nil
}

type modelsRateLimitFailureInput struct {
	req          *GatewayRequest
	res          GatewayResponseWriter
	auditCapture AuditCaptureContext
	usageContext GatewayFailureUsageContext
	startedAt    int64
	decision     AuthenticatedModelsRateLimitDecision
}

// modelsLoadFailureInput 是 before-auth 与 Identity 注入两处 models 发送点
// 共用的装载失败收尾输入（设计 4.6.4）。
type modelsLoadFailureInput struct {
	req          *GatewayRequest
	res          GatewayResponseWriter
	auditCapture AuditCaptureContext
	usageContext GatewayFailureUsageContext
	startedAt    int64
	traceID      string
}

// finishGatewayModelsLoadFailure 是 /v1/models 成员装载失败的共用收尾：
// 记录 authenticated_models_list_load_failed 审计元数据后，经
// SendGatewayFailureResponse 一次发送 500 + models_list_unavailable 标准
// 载荷（protocol 为已判定的 models 响应协议），失败回包、失败审计与失败
// 用量由既有失败 sink 完成。调用方此后必须按"已完成"返回，不得继续链路
// 或上游派发。
func (s *Service) finishGatewayModelsLoadFailure(input modelsLoadFailureInput, protocol GatewayErrorProtocol, loadErr error) {
	input.auditCapture.AddGatewayMetadata("authenticated_models_list_load_failed", map[string]any{
		"reason":  loadErr.Error(),
		"traceId": input.traceID,
	})
	responsePayload := GatewayErrorPayloadOf("模型列表暂不可用，请稍后重试", "internal_error", "models_list_unavailable")
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req: input.req, Res: input.res, AuditCapture: input.auditCapture,
		UsageContext: input.usageContext, StartedAt: input.startedAt,
		StatusCode:      500,
		ResponsePayload: responsePayload,
		Protocol:        protocol,
		Audit: FailureAudit{
			Outcome: AuditOutcomeGatewayFailed, ErrorPhase: "dispatch",
			ErrorCode: "models_list_unavailable", ErrorMessage: responsePayload.Error.Message,
		},
	})
}

// gatewayModelsErrorProtocol 把已判定的 models 响应协议映射为失败 sink 的
// 显式错误协议：openai/codex 请求按 openai 形态，显式 anthropic/gemini 头
// 按对应协议（设计 4.6.3）。
func gatewayModelsErrorProtocol(protocol ResponseProtocolCode) GatewayErrorProtocol {
	switch protocol {
	case ResponseProtocolAnthropicV:
		return GatewayErrorProtocolAnthropic
	case ResponseProtocolGeminiV:
		return GatewayErrorProtocolGemini
	default:
		return GatewayErrorProtocolOpenAI
	}
}

// sendAuthenticatedModelsRateLimitFailure mirrors sendAuthenticatedModelsRateLimitFailure.
func (s *Service) sendAuthenticatedModelsRateLimitFailure(_ context.Context, input modelsRateLimitFailureInput) {
	limiterUnavailable := input.decision.Unavailable
	statusCode := 429
	if limiterUnavailable {
		statusCode = 503
	}
	errorCode := "authenticated_models_rate_limited"
	if limiterUnavailable {
		errorCode = "authenticated_models_rate_limit_unavailable"
	}
	retryAfterSeconds := int64(1)
	if limiterUnavailable {
		retryAfterSeconds = 5
	}
	if input.decision.RetryAfterSeconds != nil {
		retryAfterSeconds = *input.decision.RetryAfterSeconds
	}
	if !input.res.HeadersSent() {
		input.res.Header().Set("Retry-After", formatInt64(retryAfterSeconds))
	}
	input.auditCapture.AddGatewayMetadata("authenticated_models_rate_limit", map[string]any{
		"scope":             input.decision.Scope,
		"limit":             input.decision.Limit,
		"retryAfterSeconds": retryAfterSeconds,
		"unavailable":       limiterUnavailable,
	})
	message := "模型列表请求过于频繁，请稍后重试"
	payloadType := "rate_limit_exceeded"
	if limiterUnavailable {
		message = "模型列表限流服务暂不可用，请稍后重试"
		payloadType = "service_unavailable"
	}
	responsePayload := GatewayErrorPayloadOf(message, payloadType, errorCode)
	errorPhase := "request_validation"
	if limiterUnavailable {
		errorPhase = "security"
	}
	s.Responses.SendGatewayFailureResponse(FailureResponseInput{
		Req: input.req, Res: input.res, AuditCapture: input.auditCapture,
		UsageContext: input.usageContext, StartedAt: input.startedAt,
		StatusCode: statusCode, ResponsePayload: responsePayload,
		Audit: FailureAudit{
			Outcome: AuditOutcomeGatewayFailed, ErrorPhase: errorPhase,
			ErrorCode: errorCode, ErrorMessage: responsePayload.Error.Message,
		},
	})
}

// bindAuditSessionIdentity mirrors bindAuditSessionIdentity.
func bindAuditSessionIdentity(auditCapture AuditCaptureContext, identity SessionIdentity, clientProfile string) {
	auditCapture.BindContext(AuditGatewayContext{
		SessionID:         identity.SessionID,
		SessionClientType: clientProfile,
		ConversationKey:   identity.ConversationKey,
	})
}

// gatewayAffinityProviderProfilePool mirrors gatewayAffinityProviderProfilePool.
func gatewayAffinityProviderProfilePool(accounts []gatewayruntimecache.OpenAIAccountSecret) string {
	seen := map[string]bool{}
	profileIDs := []string{}
	for _, account := range accounts {
		id := strings.TrimSpace(account.ProviderProtocolProfileID)
		if id == "" {
			id = strings.TrimSpace(account.ProviderCode)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)
	joined := strings.Join(profileIDs, ",")
	if joined == "" {
		joined = "empty"
	}
	return "pool:" + joined
}

// gatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey.
func gatewayAccountRuntimeKey(account gatewayruntimecache.OpenAIAccountSecret) string {
	return account.ID
}

// resolveSessionIdentityFromStrategy mirrors the
// clientSource?.sessionIdentity ?? resolveGatewaySessionIdentity(req, ...)
// fallback chain.
func resolveSessionIdentityFromStrategy(s *Service, req *GatewayRequest, strategy ClientStrategyContext, systemAccountID, apiKeyID string) SessionIdentity {
	if strategy.ClientSource != nil && strategy.ClientSource.SessionIdentity != nil {
		return *strategy.ClientSource.SessionIdentity
	}
	return s.SessionIdentity.ResolveGatewaySessionIdentity(req, SessionIdentityInput{
		ClientProfile:   strategy.ClientProfile,
		SystemAccountID: systemAccountID, APIKeyID: apiKeyID,
	})
}

// groupAccessProviderCode returns the provider code for the strategy input.
func groupAccessProviderCode(groupAccess *gatewayruntimecache.GroupUsageAccessMetadata) string {
	if groupAccess == nil {
		return ""
	}
	return groupAccess.ProviderCode
}

// groupUsageFieldsOr mirrors the `input.groupAccess ? ... : runtimeGroupAccess ? ... : undefined`
// chain of currentGroupUsageContext.
func groupUsageFieldsOr(groupAccess, runtimeGroupAccess *gatewayruntimecache.GroupUsageAccessMetadata) *GroupUsageMetadataFields {
	source := groupAccess
	if source == nil {
		source = runtimeGroupAccess
	}
	if source == nil {
		return nil
	}
	fields := GroupUsageMetadata(*source)
	return &fields
}

// apiKeyHasActiveBindingForGroup mirrors the affinity group binding check.
func apiKeyHasActiveBindingForGroup(apiKey gatewayruntimecache.GatewayAPIKeyRow, groupID string) bool {
	for _, binding := range apiKey.GroupBindings {
		if binding.Status == "active" && binding.GroupID == groupID {
			return true
		}
	}
	return false
}

// recordBindings nil-safe binding access.
func recordBindings(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) []gatewayruntimecache.GatewayAPIKeyGroupBindingRow {
	if apiKeyRecord == nil {
		return nil
	}
	return apiKeyRecord.GroupBindings
}

// recordBindingCount mirrors `apiKeyRecord.group_bindings?.length ?? 0`.
func recordBindingCount(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) int {
	return len(recordBindings(apiKeyRecord))
}

// recordRouteStrategyID mirrors `apiKeyRecord?.route_strategy_id`.
func recordRouteStrategyID(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) string {
	if apiKeyRecord == nil {
		return ""
	}
	return apiKeyRecord.RouteStrategyID
}

// defaultTrafficSource mirrors `options.trafficSource ?? 'gateway'`.
func defaultTrafficSource(trafficSource string) string {
	if trafficSource == "" {
		return TrafficSourceGateway
	}
	return trafficSource
}

// firstNonNilRecord mirrors `groupFallbackApiKeyRecord ?? apiKeyRecord`.
func firstNonNilRecord(primary, fallback *gatewayruntimecache.GatewayAPIKeyRow) *gatewayruntimecache.GatewayAPIKeyRow {
	if primary != nil {
		return primary
	}
	return fallback
}

// asRecordAlias bridges GatewayAPIKeyRow and the record alias.
func asRecordAlias(row *gatewayruntimecache.GatewayAPIKeyRow) *gatewayruntimecache.GatewayAPIKeyRow {
	if row == nil {
		return nil
	}
	record := gatewayruntimecache.GatewayAPIKeyRow(*row)
	return &record
}

// orEmptyPolicies mirrors `runtimeResponseInspectionPolicies ?? []`.
func orEmptyPolicies(policies []gatewayruntimecache.ResponseInspectionPolicySummary) []gatewayruntimecache.ResponseInspectionPolicySummary {
	if policies == nil {
		return []gatewayruntimecache.ResponseInspectionPolicySummary{}
	}
	return policies
}

// probeModelOverride mirrors `options.forwardModelsRequestToUpstream ?
// options.accountProbeModel : undefined`.
func probeModelOverride(options *PreflightOptions) string {
	if options.ForwardModelsRequestToUpstream {
		return options.AccountProbeModel
	}
	return ""
}

// candidateLoader mirrors the loadModelAwareCandidateAccounts closure.
func candidateLoader(s *Service, options *PreflightOptions, interaction *gatewaygemini.AffinityBinding, apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow, groupID, systemAccountID string) func(model, sourceEndpointFamily string) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if options.CandidateAccounts != nil || interaction != nil {
		return nil
	}
	// 3.4（合并路由设计“候选重解析扇出”）：merge 下默认闭包按单一 groupID
	// 重读会把扁平池静默收窄回窗口组，必须按绑定分组扇出重建扁平池（去重
	// + 重组标）；模型过滤语义与单组闭包一致（同参数逐组列出）。
	if isMergeRouteStrategy(apiKeyRecord) {
		groupIDs := uniqueActiveRouteGroupIds(apiKeyRecord)
		return func(model, sourceEndpointFamily string) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
			return mergeFanoutCandidateAccounts(groupIDs, func(group string) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
				return s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(s.requestContext(), group, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
					RequestedModel: model, RequestedEndpointFamily: sourceEndpointFamily,
				})
			})
		}
	}
	return func(model, sourceEndpointFamily string) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
		return s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(s.requestContext(), groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
			RequestedModel: model, RequestedEndpointFamily: sourceEndpointFamily,
		})
	}
}

// recoverableLoader mirrors the recoverUnavailableCandidateAccounts closure:
// it delegates to waitForRecoverableOpenAIGatewayCandidateAccounts.
func recoverableLoader(s *Service, options *PreflightOptions, interaction *gatewaygemini.AffinityBinding, apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow, input recoveryInput) func() ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if interaction != nil {
		return nil
	}
	if options.CandidateAccounts != nil {
		// 可靠性批次2（缺陷5）：chat 覆盖候选注入路径同样接入可恢复等待——
		// 注入候选全部临时不可用（熔断冷却）时不再直接终态失败。读取源限定
		// 为注入的绑定作用域组（+account 模式目标账户），与外部 /v1 的
		// waitForRecoverableOpenAIGatewayCandidateAccounts 同一等待骨架与预算
		// 语义；作用域未注入（API Key 分组回退等其他 provided 路径）保持原
		// 短路语义，不按 Key 策略窗口组重读。
		if scope := options.RecoverableCandidateScope; scope != nil && len(scope.GroupIDs) > 0 {
			scoped := input
			scoped.recoverableScope = scope
			return func() ([]gatewayruntimecache.OpenAIAccountSecret, error) {
				return s.waitForRecoverableProvidedCandidateAccounts(scoped)
			}
		}
		return nil
	}
	// 3.4：merge 下逐绑定扇出刷新/恢复读取（等待 scopeKey 保持窗口组）。
	if isMergeRouteStrategy(apiKeyRecord) {
		input.mergeGroupIDs = uniqueActiveRouteGroupIds(apiKeyRecord)
	}
	return func() ([]gatewayruntimecache.OpenAIAccountSecret, error) {
		return s.waitForRecoverableOpenAIGatewayCandidateAccounts(input)
	}
}

// onceSettle mirrors onceGatewayHotQualityExplorationSettlement.
func onceSettle(settle func(outcome string) error) func(outcome string) error {
	var called bool
	return func(outcome string) error {
		if called {
			return nil
		}
		called = true
		return settle(outcome)
	}
}

// requestContext returns a background context for out-of-band cache reads
// (the Node closures close over the express req; Go uses the service context).
func (s *Service) requestContext() context.Context { return context.Background() }
