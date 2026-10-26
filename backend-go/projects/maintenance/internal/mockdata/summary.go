package mockdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// MockUserPassword 是全部 mockdata_* 配套用户的固定口令。
//
// 为什么是固定明文：本地联调需要能直接登录这些用户；这些账号只在本地开发库
// 存在，摘要文件由调用方通过 --mockdata-summary-file 显式指定路径，所以这里
// 不引入额外加密层。
const MockUserPassword = "mockdata123456"

// mockOwner 是摘要里的 owner（seed 超管）。
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
// 验证。
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

// summaryOptions 是摘要里的 options 段。
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

// summaryDocument 是 mockdata 摘要 JSON 的结构。
type summaryDocument struct {
	GeneratedAt      string          `json:"generatedAt"`
	Options          summaryOptions  `json:"options"`
	Owner            mockOwner       `json:"owner"`
	MockUserPassword string          `json:"mockUserPassword"`
	MockUsers        []mockUser      `json:"mockUsers"`
	APIKeys          []mockAPIKey    `json:"apiKeys"`
	Counts           map[string]int  `json:"counts"`
	Domains          []summaryDomain `json:"domains"`
}

// writeSummary 把摘要写到 Options.SummaryFile 指定的路径并返回它。路径为空时
// 不写文件（返回空串）：PG 形态没有数据根，摘要路径必须显式给出，无隐式回退。
func writeSummary(e *env, report Report, path string) (string, error) {
	if path == "" {
		return "", nil
	}
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

// loadWiredDomainSnapshot 读取摘要文件里的域接线快照，返回已接线域的结果。
//
// 兼容性：路径为空（调用方没传 --mockdata-summary-file，无隐式回退）或文件
// 不存在时返回 (nil, nil)，覆盖校验维持「全部 NotCovered」。注入判定只认
// Counts 非空（wired 的定义即 Counts 非空），手写快照里 wired 与空计数矛盾
// 的条目按未接线处理。文件存在但解析失败是前一次造数异常的证据，静默忽略会
// 让覆盖门槛悄悄退化成全部 NotCovered，因此作为错误返回而不是跳过。
func loadWiredDomainSnapshot(path string) ([]DomainResult, error) {
	if path == "" {
		return nil, nil
	}
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
