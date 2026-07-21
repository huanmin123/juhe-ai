package redis

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// PageDataProtocolVersion is the Node page-data confirm protocol version.
	PageDataProtocolVersion = 2
	// PageDataMaxConfirmDomains bounds one atomic confirm request.
	PageDataMaxConfirmDomains         = 32
	pageDataAccountsStaticDomain      = "accounts.static"
	pageDataAccountsRuntimeDomain     = "accounts.runtime"
	pageDataAccountsOptionsDomain     = "accounts.options"
	pageDataAnnouncementsPublicDomain = "announcements.public"
	pageDataUpsertOperation           = "upsert"
	pageDataDeleteOperation           = "delete"
	pageDataRangeResetOperation       = "range_reset"
	pageDataMaxEventOwners            = 256
	pageDataMaxFieldMask              = 32
	pageDataEventLogTTLSeconds        = 8 * 24 * 60 * 60
	pageDataMaxSafeInteger            = int64(9007199254740991)
)

var (
	pageDataOccurredAtPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	pageDataRedisNamespaceRule = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)
	pageDataDomains            = map[string]struct{}{
		pageDataAccountsStaticDomain:  {},
		pageDataAccountsRuntimeDomain: {},
		pageDataAccountsOptionsDomain: {},
		"usage.records":               {},
		"announcements.public":        {},
		"providers.catalog":           {},
		"groups.static":               {},
		"systemAccounts.options":      {},
		"teams.options":               {},
		"routeStrategies.options":     {},
		"stats.overview":              {},
		"stats.accountUsage":          {},
		"stats.aiPerformance":         {},
	}
)

// PageDataChangeEvent is the Node page-data protocol v2 event shape.
type PageDataChangeEvent struct {
	EventID               string   `json:"eventId"`
	Domain                string   `json:"domain"`
	EntityID              string   `json:"entityId,omitempty"`
	Operation             string   `json:"operation"`
	FieldMask             []string `json:"fieldMask"`
	OwnerSystemAccountIDs []string `json:"ownerSystemAccountIds"`
	MembershipChanged     bool     `json:"membershipChanged"`
	OrderChanged          bool     `json:"orderChanged"`
	FilterChanged         bool     `json:"filterChanged"`
	PageChanged           bool     `json:"pageChanged"`
	OccurredAt            string   `json:"occurredAt"`
	AllScopes             bool     `json:"allScopes,omitempty"`
}

type PageDataViewScope string

const (
	PageDataViewScopeSelf  PageDataViewScope = "self"
	PageDataViewScopeAdmin PageDataViewScope = "admin"
)

type PageDataScopeInput struct {
	ViewerSystemAccountID string
	ViewScope             PageDataViewScope
	TargetSystemAccountID string
}

// PageDataScope identifies a page-data stream and binds tokens to the viewer.
type PageDataScope struct {
	StreamKey   string
	Fingerprint string
}

// PageDataRevisionToken is the Node page-data protocol v2 revision token.
type PageDataRevisionToken struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Epoch           string `json:"epoch"`
	Scope           string `json:"scope"`
	Domain          string `json:"domain"`
	Sequence        int64  `json:"sequence"`
	ResetSequence   int64  `json:"resetSequence"`
}

type PageDataChangeProjection struct {
	EntityID          string   `json:"entityId,omitempty"`
	Operation         string   `json:"operation"`
	FieldMask         []string `json:"fieldMask"`
	MembershipChanged bool     `json:"membershipChanged"`
	OrderChanged      bool     `json:"orderChanged"`
	FilterChanged     bool     `json:"filterChanged"`
	PageChanged       bool     `json:"pageChanged"`
}

type PageDataConfirmAction string

const (
	PageDataConfirmActionUnchanged PageDataConfirmAction = "unchanged"
	PageDataConfirmActionDelta     PageDataConfirmAction = "delta"
	PageDataConfirmActionReload    PageDataConfirmAction = "reload"
	PageDataConfirmActionReset     PageDataConfirmAction = "reset"
)

type PageDataConfirmDomainResult struct {
	Action  PageDataConfirmAction      `json:"action"`
	Token   PageDataRevisionToken      `json:"token"`
	Changes []PageDataChangeProjection `json:"changes,omitempty"`
}

type PageDataConfirmResult struct {
	ServerTime string                                 `json:"serverTime"`
	Domains    map[string]PageDataConfirmDomainResult `json:"domains"`
}

// PageDataChangeConfirmer confirms Node-compatible page-data revisions from
// the Redis streams written by PageDataChangePublisher.
type PageDataChangeConfirmer struct {
	client        *Client
	keyPrefix     string
	proposedEpoch string
	now           func() time.Time
	runConfirm    func(context.Context, []string, ...interface{}) (interface{}, error)
}

// PageDataChangePublisher publishes Node-compatible events to the Redis fanout
// streams. Events are built separately so callers can retry the same event
// value, including its eventId.
type PageDataChangePublisher struct {
	client        *Client
	keyPrefix     string
	proposedEpoch string
	now           func() time.Time
	newEventID    func() string

	getEpoch     func(context.Context, string) (string, error)
	setEpochNX   func(context.Context, string, string, time.Duration) (bool, error)
	runPublish   func(context.Context, []string, ...interface{}) error
	marshalEvent func(PageDataChangeEvent) ([]byte, error)

	epochLockOnce sync.Once
	epochLock     chan struct{}
	epoch         string
}

// AccountChangeInput describes one account entity change event.
type AccountChangeInput struct {
	AccountID             string
	OwnerSystemAccountIDs []string
	FieldMask             []string
	MembershipChanged     bool
	OrderChanged          bool
	FilterChanged         bool
	PageChanged           bool
	AllScopes             bool
}

// NewPageDataChangePublisher creates a publisher rooted at the raw Redis
// namespace. The namespace must be the configured RedisNamespace, not a
// client namespace such as RedisNamespace+":state".
func NewPageDataChangePublisher(client *Client, redisNamespace string) (*PageDataChangePublisher, error) {
	if client == nil {
		return nil, errors.New("page data redis client is required")
	}
	if redisNamespace == "" {
		return nil, errors.New("page data redis namespace is required")
	}
	if redisNamespace != strings.TrimSpace(redisNamespace) || !pageDataRedisNamespaceRule.MatchString(redisNamespace) {
		return nil, fmt.Errorf("invalid page data redis namespace %q", redisNamespace)
	}
	publisher := &PageDataChangePublisher{
		client:        client,
		keyPrefix:     "juhe-ai:" + redisNamespace + ":page-data-change",
		proposedEpoch: uuid.NewString(),
		now:           time.Now,
		newEventID:    uuid.NewString,
		marshalEvent:  func(event PageDataChangeEvent) ([]byte, error) { return json.Marshal(event) },
	}
	publisher.getEpoch = func(ctx context.Context, key string) (string, error) {
		value, err := client.client.Get(ctx, key).Result()
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return value, err
	}
	publisher.setEpochNX = func(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
		return client.client.SetNX(ctx, key, value, ttl).Result()
	}
	publisher.runPublish = func(ctx context.Context, keys []string, args ...interface{}) error {
		return pageDataChangePublishScript.Run(ctx, client.client, keys, args...).Err()
	}
	return publisher, nil
}

// NewPageDataScope builds the Node-compatible stream key and token fingerprint.
func NewPageDataScope(input PageDataScopeInput) (PageDataScope, error) {
	viewerID := strings.TrimSpace(input.ViewerSystemAccountID)
	if viewerID == "" {
		return PageDataScope{}, errors.New("page data viewer system account id is required")
	}
	var streamKey string
	switch input.ViewScope {
	case PageDataViewScopeSelf:
		streamKey = "owner:" + viewerID
	case PageDataViewScopeAdmin:
		targetID := strings.TrimSpace(input.TargetSystemAccountID)
		if input.TargetSystemAccountID != "" && targetID == "" {
			return PageDataScope{}, errors.New("page data target system account id must be non-empty when provided")
		}
		if targetID == "" {
			streamKey = "global"
		} else {
			streamKey = "owner:" + targetID
		}
	default:
		return PageDataScope{}, fmt.Errorf("invalid page data view scope %q", input.ViewScope)
	}
	targetOrWildcard := strings.TrimSpace(input.TargetSystemAccountID)
	if targetOrWildcard == "" {
		targetOrWildcard = "*"
	}
	fingerprintSource := strings.Join([]string{
		strconv.Itoa(PageDataProtocolVersion), viewerID, string(input.ViewScope), targetOrWildcard, streamKey,
	}, "|")
	sum := sha256.Sum256([]byte(fingerprintSource))
	fingerprint := base64.RawURLEncoding.EncodeToString(sum[:])
	return PageDataScope{StreamKey: streamKey, Fingerprint: fingerprint[:24]}, nil
}

// NewPageDataChangeConfirmer creates a confirmer over the raw Redis client.
// It intentionally uses the same un-namespaced key prefix as the publisher.
func NewPageDataChangeConfirmer(client *Client, redisNamespace string) (*PageDataChangeConfirmer, error) {
	if client == nil {
		return nil, errors.New("page data redis client is required")
	}
	if redisNamespace == "" {
		return nil, errors.New("page data redis namespace is required")
	}
	if redisNamespace != strings.TrimSpace(redisNamespace) || !pageDataRedisNamespaceRule.MatchString(redisNamespace) {
		return nil, fmt.Errorf("invalid page data redis namespace %q", redisNamespace)
	}
	confirmer := &PageDataChangeConfirmer{
		client:        client,
		keyPrefix:     "juhe-ai:" + redisNamespace + ":page-data-change",
		proposedEpoch: uuid.NewString(),
		now:           time.Now,
	}
	confirmer.runConfirm = func(ctx context.Context, keys []string, args ...interface{}) (interface{}, error) {
		return pageDataChangeConfirmScript.Run(ctx, client.client, keys, args...).Result()
	}
	return confirmer, nil
}

// Confirm atomically snapshots all requested streams and resolves each token
// using the Node v2 reload/reset/unchanged/delta contract.
func (c *PageDataChangeConfirmer) Confirm(ctx context.Context, scope PageDataScope, requestedDomains map[string]*PageDataRevisionToken) (PageDataConfirmResult, error) {
	if c == nil || c.runConfirm == nil {
		return PageDataConfirmResult{}, errors.New("page data confirmer is required")
	}
	if err := validatePageDataScope(scope); err != nil {
		return PageDataConfirmResult{}, err
	}
	domains, err := validatedPageDataConfirmDomains(requestedDomains)
	if err != nil {
		return PageDataConfirmResult{}, err
	}
	keys := make([]string, 1, 1+len(domains)*3)
	keys[0] = c.keyPrefix + ":epoch:v2"
	args := make([]interface{}, 2, 2+len(domains)*3)
	args[0] = c.proposedEpoch
	args[1] = strconv.Itoa(len(domains))
	for _, domain := range domains {
		streamScope := pageDataConfirmStreamKey(scope, domain.name)
		streamKeys := pageDataStreamKeys(c.keyPrefix, streamScope, domain.name)
		resetKeys := pageDataStreamKeys(c.keyPrefix, "all", domain.name)
		keys = append(keys, streamKeys[0], streamKeys[1], resetKeys[0])
		if validPageDataKnownTokenExceptEpoch(domain.token, scope, domain.name) {
			args = append(args, domain.token.Epoch, strconv.FormatInt(domain.token.Sequence, 10), strconv.FormatInt(domain.token.ResetSequence, 10))
		} else {
			args = append(args, "", "-1", "-1")
		}
	}
	raw, err := c.runConfirm(ctx, keys, args...)
	if err != nil {
		return PageDataConfirmResult{}, fmt.Errorf("confirm page data changes: %w", err)
	}
	snapshot, err := parsePageDataConfirmSnapshot(raw, len(domains))
	if err != nil {
		return PageDataConfirmResult{}, err
	}
	result := PageDataConfirmResult{ServerTime: c.now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"), Domains: make(map[string]PageDataConfirmDomainResult, len(domains))}
	for index, domain := range domains {
		current := snapshot.domains[index]
		token := PageDataRevisionToken{ProtocolVersion: PageDataProtocolVersion, Epoch: snapshot.epoch, Scope: scope.Fingerprint, Domain: domain.name, Sequence: current.sequence, ResetSequence: current.resetSequence}
		result.Domains[domain.name] = pageDataConfirmDomainResult(snapshot.epoch, scope, domain.name, token, domain.token, current.events)
	}
	return result, nil
}

// NewAccountsStaticResetEvents creates one or more independently publishable
// events. Owner ids are trimmed, deduplicated, sorted, and split into chunks.
func (p *PageDataChangePublisher) NewAccountsStaticResetEvents(ownerIDs []string, allScopes bool) ([]PageDataChangeEvent, error) {
	if p == nil {
		return nil, errors.New("page data publisher is required")
	}
	return p.NewRangeResetEvents(pageDataAccountsStaticDomain, ownerIDs, allScopes)
}

// NewRangeResetEvents creates Node-compatible range_reset events for a supported domain.
func (p *PageDataChangePublisher) NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]PageDataChangeEvent, error) {
	if p == nil {
		return nil, errors.New("page data publisher is required")
	}
	if !isSupportedPageDataDomain(domain) {
		return nil, fmt.Errorf("unsupported page data domain %q", domain)
	}
	return newRangeResetEvents(domain, ownerIDs, allScopes, p.now(), p.newEventID), nil
}

// NewAccountStaticUpsertEvent creates one Node-compatible accounts.static
// upsert event. Unlike range resets, entity events are intentionally not split
// because each event represents one account mutation.
func (p *PageDataChangePublisher) NewAccountStaticUpsertEvent(input AccountChangeInput) (PageDataChangeEvent, error) {
	return p.newAccountChangeEvent(pageDataAccountsStaticDomain, pageDataUpsertOperation, input)
}

// NewAccountStaticDeleteEvent creates one accounts.static delete event.
func (p *PageDataChangePublisher) NewAccountStaticDeleteEvent(input AccountChangeInput) (PageDataChangeEvent, error) {
	return p.newAccountChangeEvent(pageDataAccountsStaticDomain, pageDataDeleteOperation, input)
}

// NewAccountRuntimeUpsertEvent creates one accounts.runtime upsert event.
func (p *PageDataChangePublisher) NewAccountRuntimeUpsertEvent(input AccountChangeInput) (PageDataChangeEvent, error) {
	return p.newAccountChangeEvent(pageDataAccountsRuntimeDomain, pageDataUpsertOperation, input)
}

// NewAnnouncementPublicChangeEvent creates a Node-compatible public
// announcement entity event with the fixed public page-data flags.
func (p *PageDataChangePublisher) NewAnnouncementPublicChangeEvent(
	announcementID string,
	operation string,
	fieldMask []string,
) (PageDataChangeEvent, error) {
	if p == nil {
		return PageDataChangeEvent{}, errors.New("page data publisher is required")
	}
	event := PageDataChangeEvent{
		EventID:               p.newEventID(),
		Domain:                pageDataAnnouncementsPublicDomain,
		EntityID:              strings.TrimSpace(announcementID),
		Operation:             operation,
		FieldMask:             append([]string(nil), fieldMask...),
		OwnerSystemAccountIDs: []string{},
		MembershipChanged:     true,
		OrderChanged:          true,
		PageChanged:           true,
		OccurredAt:            p.now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if event.FieldMask == nil {
		event.FieldMask = []string{}
	}
	if err := validatePageDataChangeEvent(event); err != nil {
		return PageDataChangeEvent{}, err
	}
	return event, nil
}

func (p *PageDataChangePublisher) newAccountChangeEvent(domain string, operation string, input AccountChangeInput) (PageDataChangeEvent, error) {
	if p == nil {
		return PageDataChangeEvent{}, errors.New("page data publisher is required")
	}
	event := PageDataChangeEvent{
		EventID:               p.newEventID(),
		Domain:                domain,
		EntityID:              strings.TrimSpace(input.AccountID),
		Operation:             operation,
		FieldMask:             append([]string(nil), input.FieldMask...),
		OwnerSystemAccountIDs: normalizePageDataOwners(input.OwnerSystemAccountIDs),
		MembershipChanged:     input.MembershipChanged,
		OrderChanged:          input.OrderChanged,
		FilterChanged:         input.FilterChanged,
		PageChanged:           input.PageChanged,
		OccurredAt:            p.now().UTC().Format("2006-01-02T15:04:05.000Z"),
		AllScopes:             input.AllScopes,
	}
	if event.OwnerSystemAccountIDs == nil {
		event.OwnerSystemAccountIDs = []string{}
	}
	if event.FieldMask == nil {
		event.FieldMask = []string{}
	}
	if err := validatePageDataChangeEvent(event); err != nil {
		return PageDataChangeEvent{}, err
	}
	return event, nil
}

// NewAccountStaticResetEvents builds Node-compatible accounts.static reset
// events using UUID event ids and the supplied occurrence time.
func NewAccountStaticResetEvents(ownerIDs []string, allScopes bool, now time.Time) []PageDataChangeEvent {
	return newRangeResetEvents(pageDataAccountsStaticDomain, ownerIDs, allScopes, now, uuid.NewString)
}

func newRangeResetEvents(domain string, ownerIDs []string, allScopes bool, now time.Time, newEventID func() string) []PageDataChangeEvent {
	owners := normalizePageDataOwners(ownerIDs)
	if len(owners) == 0 {
		owners = []string{}
	}
	events := make([]PageDataChangeEvent, 0, (len(owners)+pageDataMaxEventOwners-1)/pageDataMaxEventOwners)
	for start := 0; start < len(owners) || (len(owners) == 0 && start == 0); start += pageDataMaxEventOwners {
		end := min(start+pageDataMaxEventOwners, len(owners))
		events = append(events, PageDataChangeEvent{
			EventID:               newEventID(),
			Domain:                domain,
			Operation:             pageDataRangeResetOperation,
			FieldMask:             []string{},
			OwnerSystemAccountIDs: append([]string(nil), owners[start:end]...),
			MembershipChanged:     true,
			OrderChanged:          true,
			FilterChanged:         true,
			PageChanged:           true,
			OccurredAt:            now.UTC().Format("2006-01-02T15:04:05.000Z"),
			AllScopes:             allScopes,
		})
		if len(owners) == 0 {
			break
		}
	}
	return events
}

// Publish publishes one already-created event. Retrying this method with the
// same event preserves eventId and is therefore deduplicated by Redis.
func (p *PageDataChangePublisher) Publish(ctx context.Context, event PageDataChangeEvent) error {
	if p == nil {
		return errors.New("page data publisher is required")
	}
	if err := validatePageDataChangeEvent(event); err != nil {
		return err
	}
	if _, err := p.ensureEpoch(ctx); err != nil {
		return fmt.Errorf("ensure page data epoch: %w", err)
	}
	rawEvent, err := p.marshalEvent(event)
	if err != nil {
		return fmt.Errorf("marshal page data event %q: %w", event.EventID, err)
	}
	for _, scope := range pageDataFanoutScopes(event) {
		keys := pageDataStreamKeys(p.keyPrefix, scope, event.Domain)
		if err := p.runPublish(
			ctx,
			keys,
			event.EventID,
			string(rawEvent),
			strconv.Itoa(pageDataMaxEventOwners),
			strconv.Itoa(pageDataEventLogTTLSeconds),
		); err != nil {
			return fmt.Errorf("publish page data event %q to %s: %w", event.EventID, scope, err)
		}
	}
	return nil
}

func (p *PageDataChangePublisher) ensureEpoch(ctx context.Context) (string, error) {
	release, err := p.acquireEpochLock(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if p.epoch != "" {
		return p.epoch, nil
	}
	key := p.keyPrefix + ":epoch:v2"
	existing, err := p.getEpoch(ctx, key)
	if err != nil {
		return "", err
	}
	if existing != "" {
		p.epoch = existing
		return existing, nil
	}
	inserted, err := p.setEpochNX(ctx, key, p.proposedEpoch, 0)
	if err != nil {
		return "", err
	}
	if inserted {
		p.epoch = p.proposedEpoch
		return p.epoch, nil
	}
	existing, err = p.getEpoch(ctx, key)
	if err != nil {
		return "", err
	}
	if existing == "" {
		existing = p.proposedEpoch
	}
	p.epoch = existing
	return existing, nil
}

func (p *PageDataChangePublisher) acquireEpochLock(ctx context.Context) (func(), error) {
	p.epochLockOnce.Do(func() {
		p.epochLock = make(chan struct{}, 1)
		p.epochLock <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.epochLock:
		return func() { p.epochLock <- struct{}{} }, nil
	}
}

func validatePageDataChangeEvent(event PageDataChangeEvent) error {
	if strings.TrimSpace(event.EventID) == "" {
		return errors.New("page data eventId is required")
	}
	if !isSupportedPageDataDomain(event.Domain) {
		return fmt.Errorf("unsupported page data domain %q", event.Domain)
	}
	if len(event.OwnerSystemAccountIDs) > pageDataMaxEventOwners {
		return fmt.Errorf("page data owner count exceeds %d", pageDataMaxEventOwners)
	}
	for _, ownerID := range event.OwnerSystemAccountIDs {
		if ownerID == "" || ownerID != strings.TrimSpace(ownerID) {
			return errors.New("page data owner id must be non-empty and trimmed")
		}
	}
	switch event.Operation {
	case pageDataRangeResetOperation:
		if len(event.FieldMask) != 0 {
			return fmt.Errorf("%s range_reset fieldMask must be empty", event.Domain)
		}
		if !event.MembershipChanged || !event.OrderChanged || !event.FilterChanged || !event.PageChanged {
			return fmt.Errorf("%s range_reset change flags must all be true", event.Domain)
		}
	case pageDataUpsertOperation, pageDataDeleteOperation:
		if event.Domain != pageDataAccountsStaticDomain && event.Domain != pageDataAccountsRuntimeDomain && event.Domain != pageDataAnnouncementsPublicDomain {
			return fmt.Errorf("unsupported page data entity domain %q", event.Domain)
		}
		if event.Domain == pageDataAnnouncementsPublicDomain {
			if len(event.OwnerSystemAccountIDs) != 0 || !event.MembershipChanged || !event.OrderChanged || event.FilterChanged || !event.PageChanged {
				return errors.New("announcements.public entity flags must match the Node protocol")
			}
		}
		if event.EntityID == "" || event.EntityID != strings.TrimSpace(event.EntityID) {
			return errors.New("page data entityId must be non-empty and trimmed")
		}
		if len(event.FieldMask) > pageDataMaxFieldMask {
			return fmt.Errorf("page data fieldMask count exceeds %d", pageDataMaxFieldMask)
		}
		for _, field := range event.FieldMask {
			if strings.TrimSpace(field) == "" {
				return errors.New("page data fieldMask values must be non-empty")
			}
		}
	default:
		return fmt.Errorf("unsupported page data operation %q", event.Operation)
	}
	if !pageDataOccurredAtPattern.MatchString(event.OccurredAt) {
		return errors.New("page data occurredAt must use UTC milliseconds")
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", event.OccurredAt); err != nil {
		return fmt.Errorf("invalid page data occurredAt: %w", err)
	}
	return nil
}

func isSupportedPageDataDomain(domain string) bool {
	_, ok := pageDataDomains[domain]
	return ok
}

func normalizePageDataOwners(ownerIDs []string) []string {
	seen := make(map[string]struct{}, len(ownerIDs))
	owners := make([]string, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		ownerID = strings.TrimSpace(ownerID)
		if ownerID == "" {
			continue
		}
		if _, ok := seen[ownerID]; ok {
			continue
		}
		seen[ownerID] = struct{}{}
		owners = append(owners, ownerID)
	}
	sort.Strings(owners)
	return owners
}

func pageDataFanoutScopes(event PageDataChangeEvent) []string {
	if event.AllScopes {
		return []string{"global", "all"}
	}
	scopes := []string{"global"}
	for _, ownerID := range normalizePageDataOwners(event.OwnerSystemAccountIDs) {
		scopes = append(scopes, "owner:"+ownerID)
	}
	return scopes
}

func pageDataStreamKeys(prefix, scope, domain string) []string {
	suffix := scope + ":" + domain
	return []string{prefix + ":sequence:" + suffix, prefix + ":log:" + suffix, prefix + ":dedupe:" + suffix}
}

type pageDataConfirmDomain struct {
	name  string
	token *PageDataRevisionToken
}

type pageDataSequencedEvent struct {
	sequence int64
	event    PageDataChangeEvent
}

type pageDataConfirmSnapshotDomain struct {
	sequence      int64
	resetSequence int64
	events        []pageDataSequencedEvent
}

type pageDataConfirmSnapshot struct {
	epoch   string
	domains []pageDataConfirmSnapshotDomain
}

func validatePageDataScope(scope PageDataScope) error {
	if strings.TrimSpace(scope.StreamKey) == "" || strings.TrimSpace(scope.Fingerprint) == "" {
		return errors.New("page data scope is required")
	}
	return nil
}

func validatedPageDataConfirmDomains(input map[string]*PageDataRevisionToken) ([]pageDataConfirmDomain, error) {
	if len(input) > PageDataMaxConfirmDomains {
		return nil, fmt.Errorf("page data confirm domain count exceeds %d", PageDataMaxConfirmDomains)
	}
	domains := make([]pageDataConfirmDomain, 0, len(input))
	for domain, token := range input {
		if !isSupportedPageDataDomain(domain) {
			return nil, fmt.Errorf("unsupported page data domain %q", domain)
		}
		domains = append(domains, pageDataConfirmDomain{name: domain, token: token})
	}
	sort.Slice(domains, func(left, right int) bool { return domains[left].name < domains[right].name })
	return domains, nil
}

func pageDataConfirmStreamKey(scope PageDataScope, domain string) string {
	if domain == pageDataAnnouncementsPublicDomain {
		return "global"
	}
	return scope.StreamKey
}

func validPageDataKnownTokenExceptEpoch(token *PageDataRevisionToken, scope PageDataScope, domain string) bool {
	return token != nil && token.ProtocolVersion == PageDataProtocolVersion && token.Scope == scope.Fingerprint && token.Domain == domain &&
		pageDataSafeNonNegativeInteger(token.Sequence) && pageDataSafeNonNegativeInteger(token.ResetSequence)
}

func validPageDataKnownToken(token *PageDataRevisionToken, epoch string, scope PageDataScope, domain string) bool {
	return validPageDataKnownTokenExceptEpoch(token, scope, domain) && token.Epoch == epoch
}

func pageDataSafeNonNegativeInteger(value int64) bool {
	return value >= 0 && value <= pageDataMaxSafeInteger
}

func parsePageDataConfirmSnapshot(value interface{}, expectedDomains int) (pageDataConfirmSnapshot, error) {
	values, ok := value.([]interface{})
	if !ok || len(values) != expectedDomains+1 {
		return pageDataConfirmSnapshot{}, errors.New("invalid page data redis confirm snapshot")
	}
	epoch, err := pageDataRedisString(values[0])
	if err != nil || strings.TrimSpace(epoch) == "" {
		return pageDataConfirmSnapshot{}, errors.New("invalid page data redis epoch")
	}
	result := pageDataConfirmSnapshot{epoch: epoch, domains: make([]pageDataConfirmSnapshotDomain, 0, expectedDomains)}
	for _, rawDomain := range values[1:] {
		items, ok := rawDomain.([]interface{})
		if !ok || len(items) != 3 {
			return pageDataConfirmSnapshot{}, errors.New("invalid page data redis domain snapshot")
		}
		sequence, err := pageDataRedisSafeNonNegativeInteger(items[0])
		if err != nil {
			return pageDataConfirmSnapshot{}, fmt.Errorf("parse page data stream sequence: %w", err)
		}
		resetSequence, err := pageDataRedisSafeNonNegativeInteger(items[1])
		if err != nil {
			return pageDataConfirmSnapshot{}, fmt.Errorf("parse page data reset sequence: %w", err)
		}
		rawEvents, ok := items[2].([]interface{})
		if !ok {
			return pageDataConfirmSnapshot{}, errors.New("invalid page data redis stream log")
		}
		events := make([]pageDataSequencedEvent, 0, len(rawEvents))
		for _, rawEvent := range rawEvents {
			event, valid := parsePageDataRedisSequencedEvent(rawEvent)
			if valid {
				events = append(events, event)
			}
		}
		result.domains = append(result.domains, pageDataConfirmSnapshotDomain{sequence: sequence, resetSequence: resetSequence, events: events})
	}
	return result, nil
}

func pageDataRedisSafeNonNegativeInteger(value interface{}) (int64, error) {
	number, err := redisInt64(value)
	if err != nil || !pageDataSafeNonNegativeInteger(number) {
		return 0, errors.New("must be a nonnegative safe integer")
	}
	return number, nil
}

func pageDataRedisString(value interface{}) (string, error) {
	return redisString(value)
}

func parsePageDataRedisSequencedEvent(value interface{}) (pageDataSequencedEvent, bool) {
	text, err := pageDataRedisString(value)
	if err != nil {
		return pageDataSequencedEvent{}, false
	}
	separator := strings.IndexByte(text, '\t')
	if separator <= 0 {
		return pageDataSequencedEvent{}, false
	}
	sequence, err := strconv.ParseInt(text[:separator], 10, 64)
	if err != nil || sequence <= 0 || !pageDataSafeNonNegativeInteger(sequence) {
		return pageDataSequencedEvent{}, false
	}
	var event PageDataChangeEvent
	if json.Unmarshal([]byte(text[separator+1:]), &event) != nil || validatePageDataChangeEventForConfirm(event) != nil {
		return pageDataSequencedEvent{}, false
	}
	return pageDataSequencedEvent{sequence: sequence, event: event}, true
}

func validatePageDataChangeEventForConfirm(event PageDataChangeEvent) error {
	if strings.TrimSpace(event.EventID) == "" || !isSupportedPageDataDomain(event.Domain) {
		return errors.New("invalid page data event")
	}
	switch event.Operation {
	case "upsert", "delete", "append", pageDataRangeResetOperation, "window_replace":
	default:
		return errors.New("invalid page data event operation")
	}
	if len(event.FieldMask) > pageDataMaxFieldMask || len(event.OwnerSystemAccountIDs) > pageDataMaxEventOwners {
		return errors.New("invalid page data event fields or owners")
	}
	for _, field := range event.FieldMask {
		if strings.TrimSpace(field) == "" {
			return errors.New("invalid page data event field")
		}
	}
	for _, ownerID := range event.OwnerSystemAccountIDs {
		if strings.TrimSpace(ownerID) == "" {
			return errors.New("invalid page data event owner")
		}
	}
	if !pageDataOccurredAtPattern.MatchString(event.OccurredAt) {
		return errors.New("invalid page data event occurrence time")
	}
	_, err := time.Parse("2006-01-02T15:04:05.000Z", event.OccurredAt)
	return err
}

func pageDataConfirmDomainResult(epoch string, scope PageDataScope, domain string, token PageDataRevisionToken, known *PageDataRevisionToken, events []pageDataSequencedEvent) PageDataConfirmDomainResult {
	if !validPageDataKnownToken(known, epoch, scope, domain) {
		return PageDataConfirmDomainResult{Action: PageDataConfirmActionReload, Token: token}
	}
	if known.ResetSequence != token.ResetSequence || known.Sequence > token.Sequence {
		return PageDataConfirmDomainResult{Action: PageDataConfirmActionReset, Token: token}
	}
	if known.Sequence == token.Sequence {
		return PageDataConfirmDomainResult{Action: PageDataConfirmActionUnchanged, Token: token}
	}
	filtered := make([]pageDataSequencedEvent, 0, len(events))
	for _, event := range events {
		if event.sequence > known.Sequence {
			filtered = append(filtered, event)
		}
	}
	if len(filtered) == 0 || filtered[len(filtered)-1].sequence != token.Sequence {
		return PageDataConfirmDomainResult{Action: PageDataConfirmActionReset, Token: token}
	}
	changes := make([]PageDataChangeProjection, 0, len(filtered))
	for index, item := range filtered {
		if item.sequence != known.Sequence+int64(index)+1 || item.event.Operation == pageDataRangeResetOperation {
			return PageDataConfirmDomainResult{Action: PageDataConfirmActionReset, Token: token}
		}
		if item.event.MembershipChanged || item.event.OrderChanged || item.event.FilterChanged || item.event.PageChanged {
			return PageDataConfirmDomainResult{Action: PageDataConfirmActionReload, Token: token}
		}
		changes = append(changes, PageDataChangeProjection{
			EntityID: item.event.EntityID, Operation: item.event.Operation, FieldMask: append([]string(nil), item.event.FieldMask...),
			MembershipChanged: item.event.MembershipChanged, OrderChanged: item.event.OrderChanged,
			FilterChanged: item.event.FilterChanged, PageChanged: item.event.PageChanged,
		})
	}
	return PageDataConfirmDomainResult{Action: PageDataConfirmActionDelta, Token: token, Changes: changes}
}

var pageDataChangePublishLua = `
local sequenceKey = KEYS[1]
local logKey = KEYS[2]
local dedupeKey = KEYS[3]
local eventId = ARGV[1]
local rawEvent = ARGV[2]
local maxEvents = tonumber(ARGV[3])
local ttlSeconds = tonumber(ARGV[4])
if redis.call('SISMEMBER', dedupeKey, eventId) == 1 then
  return tonumber(redis.call('GET', sequenceKey) or '0')
end
redis.call('SADD', dedupeKey, eventId)
local sequence = redis.call('INCR', sequenceKey)
redis.call('RPUSH', logKey, tostring(sequence) .. '\t' .. rawEvent)
redis.call('LTRIM', logKey, -maxEvents, -1)
redis.call('EXPIRE', logKey, ttlSeconds)
redis.call('EXPIRE', dedupeKey, ttlSeconds)
return sequence
`

var pageDataChangePublishScript = goredis.NewScript(pageDataChangePublishLua)

var pageDataChangeConfirmLua = `
local epoch = redis.call('GET', KEYS[1])
if not epoch then
  redis.call('SET', KEYS[1], ARGV[1], 'NX')
  epoch = redis.call('GET', KEYS[1]) or ARGV[1]
end
local domainCount = tonumber(ARGV[2])
local result = { epoch }
local keyIndex = 2
local argumentIndex = 3
for _ = 1, domainCount do
  local sequence = tonumber(redis.call('GET', KEYS[keyIndex]) or '0')
  local resetSequence = tonumber(redis.call('GET', KEYS[keyIndex + 2]) or '0')
  local knownEpoch = ARGV[argumentIndex]
  local knownSequence = tonumber(ARGV[argumentIndex + 1])
  local knownResetSequence = tonumber(ARGV[argumentIndex + 2])
  local rawLog = {}
  if knownEpoch == epoch
    and knownSequence >= 0
    and knownSequence < sequence
    and knownResetSequence == resetSequence then
    rawLog = redis.call('LRANGE', KEYS[keyIndex + 1], 0, -1)
  end
  table.insert(result, { sequence, resetSequence, rawLog })
  keyIndex = keyIndex + 3
  argumentIndex = argumentIndex + 3
end
return result
`

var pageDataChangeConfirmScript = goredis.NewScript(pageDataChangeConfirmLua)
