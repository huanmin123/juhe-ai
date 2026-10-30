# BUG-0338 系统账户列表空结果 items 返回 null 致前端查询报错

## 基本信息

- 编号：BUG-0338
- 状态：已修复已验证（2026-10-31 页面实测通过）
- 严重程度：P2
- 发现时间：2026-10-29（管理页面实机操作）
- 发现方式：用户反馈
- 模块：后端 / 管理面 / 系统账户
- 关联：无

## 现象

系统账户管理页在搜索框输入不存在的前缀（截图值为 `i7years`）后，右上角弹出英文 toast
`Cannot read properties of null (reading 'length')`，列表仍停留在上一次查询结果，
无法进入空结果状态。期望是正常展示空列表。

## 根因

1. **响应契约被破坏**：`authsys.AccountStore.ListPage` 使用命名返回
   `items []AccountListItem`，无匹配行时 `items` 保持 nil；handler 直接写入响应，
   `encoding/json` 把 nil 切片序列化为 `items: null`。契约文档与前端类型
   `SystemAccountListResult.items: SystemAccountListItem[]` 都要求数组，相邻列表端点
   （`systemteams`、`authz`、`accounts`）也都在构造结果时显式初始化空切片，本端点漏了。
2. **前端直接解引用**：`useResponsivePagedList.applyPageResult` 在空结果路径同样读取
   `nextItems.length`，对 `null` 抛 `TypeError`；异常进入 `onError` 后被
   `extractApiErrorMessage` 原样展示，用户只看到英文运行时错误。
3. **测试漏测**：`TestListPageKeywordIsExactOrPrefix` 用
   `items, _ := data["items"].([]any)` 取值，null 的类型断言失败被忽略且
   `len(nil) == 0`，空结果用例因此假通过。

## 修复

- `backend-go/projects/gateway/internal/authsys/store_accounts.go`：`ListPage` 在遍历
  结果集前初始化 `items = []AccountListItem{}`，空结果与越页统一返回 `[]`。
- `store_patch_semantics_test.go`：`countNames` 对 `data["items"]` 做 JSON 数组类型断言，
  返回 null 时直接 `t.Fatalf`。
- `docs/functions/接口契约与权限矩阵.md`：补充 `items` 恒为 JSON 数组、空结果为 `[]` 的契约说明。

## 验证

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | 未修复时测试必须失败 | `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/internal/authsys -run '^TestListPageKeywordIsExactOrPrefix$' -count=1` | `items = <nil>, want a JSON array` 失败 | 与预期一致 | 通过 |
| 回归验证 | 修复后空结果返回 `[]` | 同上 | 测试通过 | 通过 | 通过 |
| 包级回归 | authsys 全包 | `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/internal/authsys -count=1` | 全包通过 | 既有 `TestW11A*`（SQLite 方言故障注入臂）失败，已登记为 T76 并行批次在制品；目标用例通过 | 已知遗留，不属本批 |
| 包级回归 | 跳过 T76 在制批 | 同上加 `-skip '^TestW11A'` | 其余用例通过 | `ok ... 31.4s` | 通过 |
| 功能验证 | 页面搜索无匹配前缀 | dev 环境（revision 12）重启 gateway 后，系统账户管理页输入 `zzz-not-exist-prefix` 回车 | 展示空列表，无报错 toast | 列表展示「暂无数据」，全页快照无任何报错/失败 toast；清空搜索并点刷新后 admin 行恢复 | 通过（2026-10-31） |

## 下次遇到

- 分页列表出现 `Cannot read properties of null (reading 'length')` 时，先看对应接口空结果
  响应的 `items` 是否为 `null`，而不是先改前端。
- 新增分页列表端点必须显式初始化空切片；测试要断言 JSON 数组类型，不能只断言长度。
- 前端 `useResponsivePagedList` 不做 null 归一，避免把后端契约违例静默变成空列表。
