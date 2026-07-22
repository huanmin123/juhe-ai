package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

const (
	pageDataMaxSafeInteger     = float64(9007199254740991)
	pageDataConfirmBodyMaxSize = 256 << 10
)

var errPageDataConfirmBodyTooLarge = errors.New("page data confirm body too large")

type PageDataChangeConfirmInput struct {
	ViewerSystemAccountID string
	ViewScope             redisplatform.PageDataViewScope
	TargetSystemAccountID string
	Domains               map[string]*redisplatform.PageDataRevisionToken
}

type PageDataChangeConfirmService interface {
	Confirm(ctx context.Context, input PageDataChangeConfirmInput) (redisplatform.PageDataConfirmResult, error)
}

type pageDataChangeConfirmRequest struct {
	ViewScope             *string         `json:"viewScope"`
	TargetSystemAccountID *string         `json:"targetSystemAccountId"`
	Domains               json.RawMessage `json:"domains"`
}

type pageDataRevisionTokenRequest struct {
	ProtocolVersion *json.Number `json:"protocolVersion"`
	Epoch           *string      `json:"epoch"`
	Scope           *string      `json:"scope"`
	Domain          *string      `json:"domain"`
	Sequence        *json.Number `json:"sequence"`
	ResetSequence   *json.Number `json:"resetSequence"`
}

func NewPageDataChangeConfirmHandler(service PageDataChangeConfirmService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !pageDataHasJSONContentType(r) {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, pageDataConfirmBodyMaxSize)

		input, err := parsePageDataChangeConfirmRequest(r)
		if err != nil {
			if errors.Is(err, errPageDataConfirmBodyTooLarge) {
				writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
				return
			}
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if input.ViewScope == redisplatform.PageDataViewScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		input.ViewerSystemAccountID = strings.TrimSpace(authContext.SystemAccountID)
		result, err := service.Confirm(r.Context(), input)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			writeMessageError(w, http.StatusServiceUnavailable, "页面数据变更确认暂不可用，请稍后重试")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func parsePageDataChangeConfirmRequest(r *http.Request) (PageDataChangeConfirmInput, error) {
	var body pageDataChangeConfirmRequest
	if err := decodePageDataSingleJSON(r.Body, &body, true); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return PageDataChangeConfirmInput{}, errPageDataConfirmBodyTooLarge
		}
		return PageDataChangeConfirmInput{}, errors.New("变更确认请求体必须是严格 JSON 对象")
	}
	if body.ViewScope == nil || (*body.ViewScope != string(redisplatform.PageDataViewScopeSelf) && *body.ViewScope != string(redisplatform.PageDataViewScopeAdmin)) {
		return PageDataChangeConfirmInput{}, errors.New("viewScope 只能是 self 或 admin")
	}

	target, err := pageDataOptionalString(body.TargetSystemAccountID, "targetSystemAccountId")
	if err != nil {
		return PageDataChangeConfirmInput{}, err
	}
	viewScope := redisplatform.PageDataViewScope(*body.ViewScope)
	if viewScope == redisplatform.PageDataViewScopeSelf && target != "" {
		return PageDataChangeConfirmInput{}, errors.New("self 视图不能指定目标系统账户")
	}

	domains, err := parsePageDataConfirmDomains(body.Domains)
	if err != nil {
		return PageDataChangeConfirmInput{}, err
	}
	return PageDataChangeConfirmInput{ViewScope: viewScope, TargetSystemAccountID: target, Domains: domains}, nil
}

func pageDataHasJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func parsePageDataConfirmDomains(raw json.RawMessage) (map[string]*redisplatform.PageDataRevisionToken, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("domains必须是对象")
	}
	var values map[string]json.RawMessage
	if err := decodePageDataSingleJSON(bytes.NewReader(raw), &values, false); err != nil || values == nil {
		return nil, errors.New("domains必须是对象")
	}
	if len(values) > redisplatform.PageDataMaxConfirmDomains {
		return nil, fmt.Errorf("单次最多确认 %d 个数据域", redisplatform.PageDataMaxConfirmDomains)
	}

	domains := make(map[string]*redisplatform.PageDataRevisionToken, len(values))
	for domain, rawToken := range values {
		if !redisplatform.IsSupportedPageDataDomain(domain) {
			return nil, fmt.Errorf("不支持的数据域：%s", domain)
		}
		if bytes.Equal(bytes.TrimSpace(rawToken), []byte("null")) {
			domains[domain] = nil
			continue
		}
		token, err := parsePageDataRevisionToken(rawToken)
		if err != nil {
			return nil, err
		}
		domains[domain] = &token
	}
	return domains, nil
}

func parsePageDataRevisionToken(raw json.RawMessage) (redisplatform.PageDataRevisionToken, error) {
	var token pageDataRevisionTokenRequest
	if err := decodePageDataSingleJSON(bytes.NewReader(raw), &token, true); err != nil {
		return redisplatform.PageDataRevisionToken{}, errors.New("revision token必须是对象且不能包含未知字段")
	}
	protocolVersion, err := pageDataSafeInteger(token.ProtocolVersion, "protocolVersion", false)
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	epoch, err := pageDataRequiredString(token.Epoch, "epoch")
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	scope, err := pageDataRequiredString(token.Scope, "scope")
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	domain, err := pageDataRequiredString(token.Domain, "domain")
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	sequence, err := pageDataSafeInteger(token.Sequence, "sequence", true)
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	resetSequence, err := pageDataSafeInteger(token.ResetSequence, "resetSequence", true)
	if err != nil {
		return redisplatform.PageDataRevisionToken{}, err
	}
	return redisplatform.PageDataRevisionToken{
		ProtocolVersion: int(protocolVersion), Epoch: epoch, Scope: scope, Domain: domain,
		Sequence: sequence, ResetSequence: resetSequence,
	}, nil
}

func decodePageDataSingleJSON(reader io.Reader, destination any, strict bool) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("请求体只能包含一个 JSON 值")
}

func pageDataOptionalString(value *string, label string) (string, error) {
	if value == nil || *value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", fmt.Errorf("%s不能为空", label)
	}
	return trimmed, nil
}

func pageDataRequiredString(value *string, label string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("%s不能为空", label)
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", fmt.Errorf("%s不能为空", label)
	}
	return trimmed, nil
}

func pageDataSafeInteger(value *json.Number, label string, nonNegative bool) (int64, error) {
	if value == nil {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	number, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || math.Abs(number) > pageDataMaxSafeInteger {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	integer := int64(number)
	if nonNegative && integer < 0 {
		return 0, fmt.Errorf("%s不能小于 0", label)
	}
	return integer, nil
}
