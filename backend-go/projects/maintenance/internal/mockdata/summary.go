package mockdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// MockUserPassword 是全部 mockdata_* 配套用户的固定口令。
//
// 为什么是固定明文：本地联调需要能直接登录这些用户（Node shared.ts 的
// mockPassword 就是固定值）；这些账号只在本地开发库存在，且摘要文件本身
// 就写在本地数据目录里，所以这里不引入额外加密层。
const MockUserPassword = "mockdata123456"

// SummaryFilename 是摘要文件名（与 Node mockdataSummaryPath 一致：
// <数据根>/mockdata-summary.json）。
const SummaryFilename = "mockdata-summary.json"

// mockOwner 是摘要里的 owner（Node 取 created.users.admin）。
type mockOwner struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

// mockUser 是摘要里的一条配套用户。admin 自身不进列表（它是 owner）。
type mockUser struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Password    string `json:"password"`
}

// mockAPIKey 是摘要里的一条 API Key。Key 字段是 mock 明文，只用于本地网关
// 验证（Node 的 apiKeySummariesForMockdata 同样记录 key）。
type mockAPIKey struct {
	Name                   string `json:"name"`
	ID                     string `json:"id"`
	Label                  string `json:"label,omitempty"`
	Key                    string `json:"key"`
	OwnerSystemAccountID   string `json:"ownerSystemAccountId,omitempty"`
	OwnerSystemAccountName string `json:"ownerSystemAccountName,omitempty"`
	RouteStrategyID        string `json:"routeStrategyId,omitempty"`
	RouteStrategyName      string `json:"routeStrategyName,omitempty"`
	RouteStrategyMode      string `json:"routeStrategyMode,omitempty"`
	RouteStrategyStatus    string `json:"routeStrategyStatus,omitempty"`
	Status                 string `json:"status,omitempty"`
	ExpiresAt              string `json:"expiresAt,omitempty"`
}

// summaryOptions 是摘要里的 options 段（与 Node MockdataOptions 对齐）。
type summaryOptions struct {
	Days          int `json:"days"`
	DailyRequests int `json:"dailyRequests"`
}

// summaryDomain 是摘要里的域接线快照条目。wired 的定义与 env.domainWired 一致：
// Counts 非空即视为已接线。--verify-mockdata-coverage 在独立进程运行，内存里
// 没有本次造数的域记账，这份落盘快照是「该域本次是否产出过数据」的唯一证据；
// 没有它，覆盖命令只能把全部域断言标成 NotCovered。
type summaryDomain struct {
	Name   string         `json:"name"`
	Wired  bool           `json:"wired"`
	Counts map[string]int `json:"counts"`
}

// summaryDocument 是 mockdata-summary.json 的结构。
//
// 字段结构参照 Node 归档实现
// migration-backup-1/node/final-archive/backend/src/scripts/maintenance/mockdata/summary.ts
// 的 writeSummary（generatedAt / options / owner / mockUserPassword /
// mockUsers / apiKeys / counts）。Node 的 apiKeyBindingRule、authorizationSamples、
// upstreamResponseModelSamples 等段由各造数域在其实施时追加，本骨架只保留
// 与域无关的公共段。
type summaryDocument struct {
	GeneratedAt      string         `json:"generatedAt"`
	Options          summaryOptions `json:"options"`
	Owner            mockOwner      `json:"owner"`
	MockUserPassword string         `json:"mockUserPassword"`
	MockUsers        []mockUser     `json:"mockUsers"`
	APIKeys          []mockAPIKey   `json:"apiKeys"`
	Counts           map[string]int `json:"counts"`
	// Domains 是域接线快照（Go 实现新增，Node 归档实现没有）：覆盖校验命令
	// 读取后把快照中的域视为已接线；缺文件或缺该字段时维持全部 NotCovered，
	// 对旧摘要向后兼容。
	Domains []summaryDomain `json:"domains"`
}

// addMockUser 登记一个配套用户（域实现者在创建 system_accounts 行后调用）。
func (e *env) addMockUser(user mockUser) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.users = append(e.users, user)
}

// addAPIKey 登记一条 API Key（域实现者在创建 api_keys 行后调用）。
func (e *env) addAPIKey(key mockAPIKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.apiKeys = append(e.apiKeys, key)
}

// setOwner 登记 owner（admin）；重复调用以最后一次为准，避免域重跑时叠加。
func (e *env) setOwner(owner mockOwner) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.owner = owner
}

// summarySnapshot 取一份摘要输入的浅拷贝，保证写文件时不再持锁。空集合保持
// nil（不预先补成空切片）：nil 的统一兜底只留在 writeSummary 一处。
func (e *env) summarySnapshot() (mockOwner, []mockUser, []mockAPIKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	users := append([]mockUser(nil), e.users...)
	keys := append([]mockAPIKey(nil), e.apiKeys...)
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return e.owner, users, keys
}

// mockdataSummaryPath 返回摘要文件路径（数据根目录下的固定名）。
func mockdataSummaryPath(paths Paths) string {
	return filepath.Join(paths.DataDir, SummaryFilename)
}

// writeSummary 写出 mockdata-summary.json 并返回路径。
//
// 为什么摘要必须落盘而不是只打印：本地联调需要从文件里取 API Key 明文去发
// 网关请求（docs/functions/Mockdata造数设计.md「验证点」），页面之外的工具
// 链路依赖这个固定路径。
func writeSummary(e *env, report Report) (string, error) {
	owner, users, keys := e.summarySnapshot()
	domains := make([]summaryDomain, 0, len(report.Domains))
	for _, result := range report.Domains {
		counts := result.Counts
		if counts == nil {
			counts = map[string]int{}
		}
		domains = append(domains, summaryDomain{Name: result.Name, Wired: len(counts) > 0, Counts: counts})
	}
	document := summaryDocument{
		GeneratedAt:      report.FinishedAt,
		Options:          summaryOptions{Days: report.Days, DailyRequests: report.DailyRequests},
		Owner:            owner,
		MockUserPassword: MockUserPassword,
		MockUsers:        users,
		APIKeys:          keys,
		Counts:           report.Counts,
		Domains:          domains,
	}
	if document.MockUsers == nil {
		document.MockUsers = []mockUser{}
	}
	if document.APIKeys == nil {
		document.APIKeys = []mockAPIKey{}
	}
	if document.Counts == nil {
		document.Counts = map[string]int{}
	}
	path := mockdataSummaryPath(e.options.Paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("创建摘要目录 %s: %w", filepath.Dir(path), err)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", fmt.Errorf("编码 mockdata 摘要: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return "", fmt.Errorf("写 mockdata 摘要 %s: %w", path, err)
	}
	return path, nil
}

// loadWiredDomainSnapshot 读取数据根下摘要里的域接线快照，返回已接线域的结果。
//
// 兼容性：文件缺失（独立覆盖校验跑在造数之前 / 旧版实现没写快照）或摘要里
// 没有 domains 字段时返回 (nil, nil)，覆盖校验维持「全部 NotCovered」的现状。
// 注入判定只认 Counts 非空（wired 的定义即 Counts 非空），手写快照里 wired 与
// 空计数矛盾的条目按未接线处理。文件存在但解析失败是前一次造数异常的证据，
// 静默忽略会让覆盖门槛悄悄退化成全部 NotCovered，因此作为错误返回而不是跳过。
func loadWiredDomainSnapshot(paths Paths) ([]DomainResult, error) {
	path := mockdataSummaryPath(paths)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读 mockdata 摘要 %s: %w", path, err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("解析 mockdata 摘要 %s: %w", path, err)
	}
	var wired []DomainResult
	for _, domain := range document.Domains {
		if !domain.Wired || len(domain.Counts) == 0 {
			continue
		}
		wired = append(wired, DomainResult{Name: domain.Name, Counts: domain.Counts})
	}
	return wired, nil
}
