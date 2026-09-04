package gatewayhybrid

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// Hybrid gateway route resolution, mirroring
// backend/src/modules/gateway/hybrid/routing.service.ts.

// Route outcome mirrors HybridGatewayRouteResult.
const (
	RouteOutcomeSelected = "selected"
	RouteOutcomeSkipped  = "skipped"
	RouteOutcomeFailed   = "failed"
)

// Route failure / skip reasons (byte-identical with the Node literals).
const (
	RouteSkipNotHybridStrategy      = "not_hybrid_route_strategy"
	RouteSkipNotJSONPost            = "not_json_post_request"
	RouteFailScoringFallbackGone    = "hybrid_scoring_fallback_unavailable"
	RouteFailLevelRouteMissing      = "hybrid_level_route_missing"
	RouteFailTargetGroupUnavailable = "hybrid_target_group_unavailable"
)

// Rewrite error messages (byte-identical with the Node literals).
const (
	RewriteEmptyBodyError   = "混合路由无法改写空请求体"
	RewriteNotObjectError   = "混合路由请求体必须是 JSON 对象"
	RewriteModelFailedError = "混合路由模型改写失败"
)

// HybridGatewayRouteResult mirrors the selected/skipped/failed union; check
// Outcome first. Selected carries the full dispatch bundle.
type HybridGatewayRouteResult struct {
	Outcome string

	// selected
	APIKeyRecord               *APIKeyRecord
	GroupID                    string
	GroupAccess                GroupUsageAccessMetadata
	Accounts                   []OpenAIAccountSecret
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
	Scoring                    HybridScoringResult
	Route                      routestrategies.HybridLevelRoute
	Config                     *routestrategies.HybridRoutingConfig
	TargetModel                string
	AffinityApplied            bool
	ScoringFallbackApplied     bool

	// skipped
	Reason string

	// failed
	TargetModel2 string // failed branch carries its own targetModel
}

// HybridGatewayRuntimeRoute mirrors HybridGatewayRuntimeRoute (re-exported
// shape used by the request lifecycle).
type HybridGatewayRuntimeRoute struct {
	APIKeyRecord           APIKeyRecord
	Config                 *routestrategies.HybridRoutingConfig
	Scoring                HybridScoringResult
	Route                  routestrategies.HybridLevelRoute
	TargetModel            string
	AffinityApplied        bool
	ScoringFallbackApplied bool
	QualityRetryCount      int
}

// HybridGatewayTargetRoute mirrors HybridGatewayTargetRoute.
type HybridGatewayTargetRoute struct {
	APIKeyRecord               APIKeyRecord
	GroupID                    string
	GroupAccess                GroupUsageAccessMetadata
	Accounts                   []OpenAIAccountSecret
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
	Route                      routestrategies.HybridLevelRoute
	TargetModel                string
}

// RouteService mirrors resolveHybridGatewayRoute and
// resolveNextHybridGatewayRoute.
type RouteService struct {
	affinity    *AffinityService
	selector    TargetGroupSelector
	identity    SessionIdentityPort
	diagnostics RouteDiagnosticsPublisher
}

func NewRouteService(affinity *AffinityService, selector TargetGroupSelector, identity SessionIdentityPort, diagnostics RouteDiagnosticsPublisher) *RouteService {
	return &RouteService{affinity: affinity, selector: selector, identity: identity, diagnostics: diagnostics}
}

// RouteInput mirrors the resolveHybridGatewayRoute input.
type RouteInput struct {
	View                       *GatewayRequestView
	Body                       RequestBodyGateway
	APIKeyRecord               APIKeyRecord
	TraceID                    string
	ClientIP                   string
	Endpoint                   string
	Audit                      AuditMetadataCapture
	RequestClientCompatibility ClientCompatibilityCapability
}

// Resolve mirrors resolveHybridGatewayRoute.
func (service *RouteService) Resolve(ctx context.Context, input RouteInput, scoringService *ScoringService) (HybridGatewayRouteResult, error) {
	config := input.APIKeyRecord.HybridRoutingConfig
	if input.APIKeyRecord.RouteStrategyMode != routestrategies.ModeHybridSmart || config == nil {
		return HybridGatewayRouteResult{Outcome: RouteOutcomeSkipped, Reason: RouteSkipNotHybridStrategy}, nil
	}
	if !IsHybridRoutableRequest(input.View) {
		return HybridGatewayRouteResult{Outcome: RouteOutcomeSkipped, Reason: RouteSkipNotJSONPost}, nil
	}
	scoring := scoringService.Score(ctx, ScoreInput{
		View:         input.View,
		APIKeyRecord: input.APIKeyRecord,
		Config:       config,
		TraceID:      input.TraceID,
		ClientIP:     input.ClientIP,
		Endpoint:     input.Endpoint,
	})
	if scoring.Failed {
		fallbackTarget, err := service.selectHybridScoringFallbackTarget(ctx, input, config)
		if err != nil {
			return HybridGatewayRouteResult{}, err
		}
		if fallbackTarget != nil {
			identity := hybridIdentityDiagnostics(service.identity, input.View)
			diagnostics := NewOrderedJSON()
			diagnostics.Set("traceId", input.TraceID)
			diagnostics.Set("apiKeyId", input.APIKeyRecord.ID)
			diagnostics.Set("conversationKey", identity)
			diagnostics.Set("endpoint", input.Endpoint)
			diagnostics.Set("outcome", "selected")
			diagnostics.Set("level", scoring.Level)
			diagnostics.Set("confidence", confidenceOrUndefined(scoring.Confidence))
			diagnostics.Set("scoringDefaulted", scoring.Defaulted)
			diagnostics.Set("scoringCacheHit", scoring.CacheHit)
			if scoring.ScoringAccountID != "" {
				diagnostics.Set("scoringAccountId", scoring.ScoringAccountID)
			} else {
				diagnostics.Set("scoringAccountId", Undefined)
			}
			if scoring.ErrorCode != "" {
				diagnostics.Set("scoringErrorCode", scoring.ErrorCode)
			} else {
				diagnostics.Set("scoringErrorCode", Undefined)
			}
			if scoring.ErrorMessage != "" {
				diagnostics.Set("scoringErrorMessage", scoring.ErrorMessage)
			} else {
				diagnostics.Set("scoringErrorMessage", Undefined)
			}
			diagnostics.Set("scoringFactors", stringSliceOrUndefined(scoring.Factors))
			diagnostics.Set("scoringReason", optionalStringOrUndefined(scoring.Reason))
			diagnostics.Set("targetModel", fallbackTarget.Route.TargetModel)
			diagnostics.Set("targetGroupId", fallbackTarget.GroupID)
			diagnostics.Set("levelRange", []any{fallbackTarget.Route.MinLevel, fallbackTarget.Route.MaxLevel})
			diagnostics.Set("scoringFallbackApplied", true)
			fallbackReason := scoring.ErrorCode
			if fallbackReason == "" {
				fallbackReason = "hybrid_scoring_failed"
			}
			diagnostics.Set("scoringFallbackReason", fallbackReason)
			diagnostics.Set("scoringFallbackMaxLevel", config.ScoringFallbackMaxLevel)
			diagnostics.Set("affinityApplied", false)
			service.publishRouteDiagnostics(input, diagnostics)
			if err := rewriteHybridRequestModel(ctx, input.Body, fallbackTarget.Route.TargetModel); err != nil {
				return HybridGatewayRouteResult{}, err
			}
			return HybridGatewayRouteResult{
				Outcome:                   RouteOutcomeSelected,
				APIKeyRecord:              withSelectedGroup(input.APIKeyRecord, fallbackTarget.GroupID),
				GroupID:                   fallbackTarget.GroupID,
				GroupAccess:               fallbackTarget.GroupAccess,
				Accounts:                  fallbackTarget.Accounts,
				ResponseInspectionPolicies: fallbackTarget.ResponseInspectionPolicies,
				Scoring:                   scoring,
				Route:                     fallbackTarget.Route,
				Config:                    config,
				TargetModel:               fallbackTarget.Route.TargetModel,
				AffinityApplied:           false,
				ScoringFallbackApplied:    true,
			}, nil
		}
		diagnostics := NewOrderedJSON()
		diagnostics.Set("traceId", input.TraceID)
		diagnostics.Set("apiKeyId", input.APIKeyRecord.ID)
		diagnostics.Set("conversationKey", hybridIdentityDiagnostics(service.identity, input.View))
		diagnostics.Set("endpoint", input.Endpoint)
		diagnostics.Set("outcome", "failed")
		diagnostics.Set("reason", RouteFailScoringFallbackGone)
		diagnostics.Set("scoringFallbackMaxLevel", config.ScoringFallbackMaxLevel)
		diagnostics.Set("scoringDefaulted", scoring.Defaulted)
		diagnostics.Set("scoringCacheHit", scoring.CacheHit)
		if scoring.ScoringAccountID != "" {
			diagnostics.Set("scoringAccountId", scoring.ScoringAccountID)
		} else {
			diagnostics.Set("scoringAccountId", Undefined)
		}
		if scoring.ErrorCode != "" {
			diagnostics.Set("scoringErrorCode", scoring.ErrorCode)
		} else {
			diagnostics.Set("scoringErrorCode", Undefined)
		}
		if scoring.ErrorMessage != "" {
			diagnostics.Set("scoringErrorMessage", scoring.ErrorMessage)
		} else {
			diagnostics.Set("scoringErrorMessage", Undefined)
		}
		service.publishRouteDiagnostics(input, diagnostics)
		return HybridGatewayRouteResult{
			Outcome: RouteOutcomeFailed,
			Reason:  RouteFailScoringFallbackGone,
			Scoring: scoring,
		}, nil
	}

	initialRoute := TargetHybridLevelRouteForLevel(config, scoring.Level)
	if initialRoute == nil {
		return HybridGatewayRouteResult{Outcome: RouteOutcomeFailed, Reason: RouteFailLevelRouteMissing, Scoring: scoring}, nil
	}
	affinity, err := service.affinity.ApplyAsync(ctx, AffinityInput{
		View:            input.View,
		SystemAccountID: input.APIKeyRecord.SystemAccountID,
		APIKeyID:        input.APIKeyRecord.ID,
		Config:          config,
		Level:           scoring.Level,
		Route:           *initialRoute,
	})
	if err != nil {
		return HybridGatewayRouteResult{}, err
	}
	route := affinity.Route
	candidates := append([]routestrategies.HybridLevelRoute{route}, HigherHybridLevelRoutes(config, &route)...)
	for index := range candidates {
		candidateRoute := candidates[index]
		target, err := service.selectHybridTargetGroup(ctx, input, &candidateRoute)
		if err != nil {
			return HybridGatewayRouteResult{}, err
		}
		if target == nil {
			continue
		}
		diagnostics := NewOrderedJSON()
		diagnostics.Set("traceId", input.TraceID)
		diagnostics.Set("apiKeyId", input.APIKeyRecord.ID)
		diagnostics.Set("conversationKey", hybridIdentityDiagnostics(service.identity, input.View))
		diagnostics.Set("endpoint", input.Endpoint)
		diagnostics.Set("outcome", "selected")
		diagnostics.Set("level", scoring.Level)
		diagnostics.Set("confidence", confidenceOrUndefined(scoring.Confidence))
		diagnostics.Set("scoringDefaulted", scoring.Defaulted)
		diagnostics.Set("scoringCacheHit", scoring.CacheHit)
		if scoring.ScoringAccountID != "" {
			diagnostics.Set("scoringAccountId", scoring.ScoringAccountID)
		} else {
			diagnostics.Set("scoringAccountId", Undefined)
		}
		if scoring.ErrorCode != "" {
			diagnostics.Set("scoringErrorCode", scoring.ErrorCode)
		} else {
			diagnostics.Set("scoringErrorCode", Undefined)
		}
		if scoring.ErrorMessage != "" {
			diagnostics.Set("scoringErrorMessage", scoring.ErrorMessage)
		} else {
			diagnostics.Set("scoringErrorMessage", Undefined)
		}
		diagnostics.Set("scoringFactors", stringSliceOrUndefined(scoring.Factors))
		diagnostics.Set("scoringReason", optionalStringOrUndefined(scoring.Reason))
		diagnostics.Set("targetModel", candidateRoute.TargetModel)
		diagnostics.Set("targetGroupId", target.GroupID)
		diagnostics.Set("levelRange", []any{candidateRoute.MinLevel, candidateRoute.MaxLevel})
		if candidateRoute.TargetModel != route.TargetModel {
			diagnostics.Set("upgradedFromModel", route.TargetModel)
		} else {
			diagnostics.Set("upgradedFromModel", Undefined)
		}
		diagnostics.Set("affinityApplied", affinity.Applied)
		if affinity.Reason != "" {
			diagnostics.Set("affinityReason", affinity.Reason)
		} else {
			diagnostics.Set("affinityReason", Undefined)
		}
		if affinity.PreviousModel != "" {
			diagnostics.Set("previousModel", affinity.PreviousModel)
		} else {
			diagnostics.Set("previousModel", Undefined)
		}
		if affinity.LowCount != nil {
			diagnostics.Set("lowCount", *affinity.LowCount)
		} else {
			diagnostics.Set("lowCount", Undefined)
		}
		service.publishRouteDiagnostics(input, diagnostics)
		if err := rewriteHybridRequestModel(ctx, input.Body, candidateRoute.TargetModel); err != nil {
			return HybridGatewayRouteResult{}, err
		}
		return HybridGatewayRouteResult{
			Outcome:                   RouteOutcomeSelected,
			APIKeyRecord:              withSelectedGroup(input.APIKeyRecord, target.GroupID),
			GroupID:                   target.GroupID,
			GroupAccess:               target.GroupAccess,
			Accounts:                  target.Accounts,
			ResponseInspectionPolicies: target.ResponseInspectionPolicies,
			Scoring:                   scoring,
			Route:                     candidateRoute,
			Config:                    config,
			TargetModel:               candidateRoute.TargetModel,
			AffinityApplied:           affinity.Applied,
			ScoringFallbackApplied:    false,
		}, nil
	}
	diagnostics := NewOrderedJSON()
	diagnostics.Set("traceId", input.TraceID)
	diagnostics.Set("apiKeyId", input.APIKeyRecord.ID)
	diagnostics.Set("conversationKey", hybridIdentityDiagnostics(service.identity, input.View))
	diagnostics.Set("endpoint", input.Endpoint)
	diagnostics.Set("outcome", "failed")
	diagnostics.Set("reason", RouteFailTargetGroupUnavailable)
	diagnostics.Set("level", scoring.Level)
	diagnostics.Set("confidence", confidenceOrUndefined(scoring.Confidence))
	diagnostics.Set("scoringDefaulted", scoring.Defaulted)
	diagnostics.Set("scoringCacheHit", scoring.CacheHit)
	if scoring.ErrorCode != "" {
		diagnostics.Set("scoringErrorCode", scoring.ErrorCode)
	} else {
		diagnostics.Set("scoringErrorCode", Undefined)
	}
	if scoring.ErrorMessage != "" {
		diagnostics.Set("scoringErrorMessage", scoring.ErrorMessage)
	} else {
		diagnostics.Set("scoringErrorMessage", Undefined)
	}
	diagnostics.Set("scoringFactors", stringSliceOrUndefined(scoring.Factors))
	diagnostics.Set("scoringReason", optionalStringOrUndefined(scoring.Reason))
	diagnostics.Set("targetModel", route.TargetModel)
	service.publishRouteDiagnostics(input, diagnostics)
	return HybridGatewayRouteResult{
		Outcome:      RouteOutcomeFailed,
		Reason:       RouteFailTargetGroupUnavailable,
		Scoring:      scoring,
		TargetModel2: route.TargetModel,
	}, nil
}

// ResolveNext mirrors resolveNextHybridGatewayRoute (quality repair upgrade
// path): the first available route above the current one.
func (service *RouteService) ResolveNext(ctx context.Context, input RouteInput, currentRoute routestrategies.HybridLevelRoute) (*HybridGatewayTargetRoute, error) {
	config := input.APIKeyRecord.HybridRoutingConfig
	if input.APIKeyRecord.RouteStrategyMode != routestrategies.ModeHybridSmart || config == nil {
		return nil, nil
	}
	for _, candidateRoute := range HigherHybridLevelRoutes(config, &currentRoute) {
		candidate := candidateRoute
		target, err := service.selectHybridTargetGroup(ctx, input, &candidate)
		if err != nil {
			return nil, err
		}
		if target == nil {
			continue
		}
		if err := rewriteHybridRequestModel(ctx, input.Body, candidate.TargetModel); err != nil {
			return nil, err
		}
		return &HybridGatewayTargetRoute{
			APIKeyRecord:               *withSelectedGroup(input.APIKeyRecord, target.GroupID),
			GroupID:                    target.GroupID,
			GroupAccess:                target.GroupAccess,
			Accounts:                   target.Accounts,
			ResponseInspectionPolicies: target.ResponseInspectionPolicies,
			Route:                      candidate,
			TargetModel:                candidate.TargetModel,
		}, nil
	}
	return nil, nil
}

func (service *RouteService) selectHybridTargetGroup(ctx context.Context, input RouteInput, route *routestrategies.HybridLevelRoute) (*TargetGroupSelection, error) {
	return service.selector.SelectTargetGroup(ctx, TargetGroupSelectorInput{
		View:                       input.View,
		APIKeyRecord:               input.APIKeyRecord,
		TargetModel:                route.TargetModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
}

func (service *RouteService) selectHybridScoringFallbackTarget(ctx context.Context, input RouteInput, config *routestrategies.HybridRoutingConfig) (*HybridGatewayTargetRoute, error) {
	routes := HybridScoringFallbackRoutes(config)
	for index := range routes {
		route := routes[index]
		target, err := service.selectHybridTargetGroup(ctx, input, &route)
		if err != nil {
			return nil, err
		}
		if target == nil {
			continue
		}
		return &HybridGatewayTargetRoute{
			GroupID:                    target.GroupID,
			GroupAccess:                target.GroupAccess,
			Accounts:                   target.Accounts,
			ResponseInspectionPolicies: target.ResponseInspectionPolicies,
			Route:                      route,
			TargetModel:                route.TargetModel,
		}, nil
	}
	return nil, nil
}

func (service *RouteService) publishRouteDiagnostics(input RouteInput, metadata *OrderedJSON) {
	if input.Audit != nil {
		input.Audit.AddGatewayMetadata("hybrid_route", metadata)
	}
	if service.diagnostics != nil {
		service.diagnostics.PublishHybridRouteDecision(metadata)
	}
}

func withSelectedGroup(record APIKeyRecord, groupID string) *APIKeyRecord {
	updated := record
	updated.SelectedGroupID = groupID
	return &updated
}

// hybridIdentityDiagnostics mirrors hybridIdentityDiagnostics: the
// conversationKey or undefined.
func hybridIdentityDiagnostics(identity SessionIdentityPort, view *GatewayRequestView) any {
	if identity == nil || view.ConversationKey == "" {
		return Undefined
	}
	return view.ConversationKey
}

func confidenceOrUndefined(value *float64) any {
	if value == nil {
		return Undefined
	}
	return *value
}

func optionalStringOrUndefined(value *string) any {
	if value == nil {
		return Undefined
	}
	return *value
}

func stringSliceOrUndefined(values []string) any {
	if values == nil {
		return Undefined
	}
	array := make([]any, len(values))
	for index, value := range values {
		array[index] = value
	}
	return array
}

// RewriteHybridRequestModel mirrors rewriteHybridRequestModel (exported for
// the request lifecycle): try the direct replace, then the raw-body parse
// fallback, raising the byte-identical Chinese errors.
func RewriteHybridRequestModel(ctx context.Context, body RequestBodyGateway, targetModel string) error {
	return rewriteHybridRequestModel(ctx, body, targetModel)
}

func rewriteHybridRequestModel(ctx context.Context, body RequestBodyGateway, targetModel string) error {
	if body.ReplaceModel(targetModel) {
		return nil
	}
	if !body.HasRawBody() {
		return &HybridError{Message: RewriteEmptyBodyError}
	}
	parsed, err := body.ParseRawBody(ctx)
	if err != nil {
		return &HybridError{Message: RewriteNotObjectError}
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		return &HybridError{Message: RewriteNotObjectError}
	}
	if !body.ReplaceModelWithParsed(targetModel, object) {
		return &HybridError{Message: RewriteModelFailedError}
	}
	return nil
}

// IsHybridRoutableRequest mirrors isHybridRoutableRequest: POST + JSON
// content type + a body (raw bytes or parsed object).
func IsHybridRoutableRequest(view *GatewayRequestView) bool {
	if strings.ToUpper(view.Method) != "POST" {
		return false
	}
	if !strings.Contains(strings.ToLower(view.ContentType), "json") {
		return false
	}
	return view.hasRawBody() || view.BodyAvailable
}
