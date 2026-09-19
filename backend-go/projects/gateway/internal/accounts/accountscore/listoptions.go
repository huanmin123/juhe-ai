package accountscore

// List query contracts shared between the facade list surface and the
// accountstransfer subdomain (REFACTOR-0005 阶段 C 下沉)：CollectExportIDs 的
// 分页取 id 面经 Deps 端口复用同一 ListOptions 契约，门面保留类型别名，
// list.go 的规范化/查询执行零改动。

// ListSort is one AccountListSort entry.
type ListSort struct {
	Field string
	Order string // asc | desc
}

// ListOptions mirrors AccountListOptions.
type ListOptions struct {
	Sorts                     []ListSort
	IDs                       []string
	Page                      int
	PageSize                  int
	Keyword                   string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	TagIDs                    []string
	Type                      string
	Status                    string
	Schedulable               string // all | enabled | disabled | cooling
}

// AccountListSortFields mirrors the allowed list sort field set.
var AccountListSortFields = map[string]bool{
	"priority": true, "superPriority": true, "fallback": true, "name": true,
	"type": true, "providerCode": true, "systemAccount": true, "concurrency": true,
	"status": true, "accountExpiresAt": true, "lastUsedAt": true,
}
