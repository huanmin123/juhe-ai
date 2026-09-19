package main

// w2_assembly_tail_test.go —— 组合根/引导/聊天外围组残余未覆盖行收尾（w2c 前缀，
// TestW2C 入口）。只新增本文件，不修改任何现有文件。
//
// 驱动层次：
//  1. 进程内 mock 直驱：database/sql 自定义 driver 注入（chat keys 错误臂、
//     图片观察观察臂）、假端口（tool capabilities 决策矩阵）、纯几何函数
//     （图片管道）、畸形路径/只读破坏（storage preflight 与六库物理门禁）。
//  2. composeSystemAPI SQLite 组合根 + 种子破坏（限流设置坏值六臂、runtime-log
//     保留期坏值）+ 真实 /v1 派发（账户锁失效回调、可恢复等待者唤醒）。
//  3. 插桩二进制场景（main.go 引导失败臂，见文件尾）。
//
// 死代码/恒不可达登记（逐块证据，不硬凑；标注"已于 w3 清理"的条目对应
// 的生产死臂已删除，证据保留作判定记录）：
//   - 【已于 w3 清理】chain_chat.go 208-210/212-214：ListProviderCatalog 对
//     items 做 json.Marshal→Unmarshal 往返；items 来自 runtime cache 的纯数据
//     结构体，Marshal 恒成功，往返 Unmarshal 的目标类型字段同形，恒成功。
//     （往返本身是 runtimecache 大结构到 chat 小结构的类型投影，保留。）
//   - chain_chat.go 332-334：newChatTokenCount 依赖 modelcheckprobe 内嵌
//     o200k tokenizer（embed 资源恒存在），NewO200kTokenizer 进程内恒成功。
//     （w3 保留：tokenizer.Count 依赖 regexp2 回溯引擎，存在真实错误面，
//     countErr 检查与 NewO200kTokenizer 守卫均为活契约。）
//   - chain_chat.go 337-339：tokenizer.Count(text) 对任意 string 无错误出口
//     （签名只返回 int），countErr 恒 nil。（w3 复核：Count 实际签名返回
//     (int, error)，codec.Count 经 regexp2 FindStringMatch 有错误出口——
//     保留 countErr 检查。）
//   - chain_chat_keys.go 91-92：apikeys.EncryptJSON=accountcrypto.EncryptJSON，
//     密钥取 sha256(secret)（空串也合法），value 为 map[string]string 恒可
//     Marshal → 该错误臂不可达。（w3 保留：导出函数调用点守卫。）
//   - chain_chat_mount.go 39：hub.now 闭包体在 chat.GenerationHub 内已无调用点。
//     （w3 保留：闭包作为参数传入 NewGenerationHub/NewCompactionService，
//     gateway 侧有调用点；内部死点属 internal/chat 共享平台文件，不在本组范围。）
//   - 【已于 w3 清理】chain_chat_mount.go 41-43 的重复臂保留（chat.NewStore
//     导出构造器调用点守卫）；47-49 同理保留。
//   - 【已于 w3 清理】chain_chat_mount.go 117-119/120-122：writeEvent 的
//     ended 守卫与 data==nil 兜底——唯一调用点（157 行）恒传非 nil data；
//     end() 的全部四个调用点（149/161/172/177）置 ended 后立即 return，
//     事件循环单线程，闭包不外露。（w3 已删除并级联清理 ended/writeMu/end()。）
//   - 【已于 w3 清理】chain_chat_mount.go 168-170：心跳 !active——ended=true
//     时循环必已退出（同上，end() 的调用点全部同步 return）。
//   - chain_chat_mount.go 225-227：openChatDatabase 的 sql.Open("sqlite") 错误
//     臂——modernc/sqlite 的 Open 惰性建连（DSN 解析在首用时），且 SQLite 模式
//     下 ChatDatabasePath 为空已被启动 preflight（storage_bootstrap.go 48-51）
//     先行拒绝，compose 内 224 行恒成功。（w3 保留：sql.Open 惰性类惯例守卫。）
//   - 【已于 w3 清理】chain_chat_images.go 64：decodeChatImage 的全部错误出口
//     （185/190 行）只返回 errChatImageDecode，errors.Is 恒真。
//   - chain_chat_images.go 66-68：image.Decode 只会产出 init()（213-219）注册
//     的 jpeg/png/gif/webp 四种 format，其它格式直接解码失败。（w3 保留：
//     image.Decode 格式注册是进程全局面，非本包自包含。）
//   - chain_chat_images.go 71-73：decodeChatImage 已把 pixels<=0 归入解码失败，
//     成功返回的 src 边界恒正。（w3 保留：orientedChatDimensions 的 false 臂
//     被 w1 测试直接锁定为函数契约，调用点守卫保留。）
//   - chain_chat_images.go 77-79/129-131/154-156/161-163：chainVP8LEncode 仅在
//     尺寸 <=0 或 >16384 时报错；encodeChatModelImage/CreatePreview 的输入
//     尺寸已被 boundedChatImageDimensions（≤1024 边）与 preview ladder
//     （≤640 边）约束，恒在范围内。（w3 保留：chainVP8LEncode 的错误臂被
//     w1 测试直接锁定，调用点守卫保留。）
//   - chain_chat_images.go 146：preview ladder 最小尝试 256 边 → 最终像素
//     ≤256×256=65536，全字面量 VP8L 恒 32bit/像素 ≈ 256KiB + 头部 < 512KiB
//     上限，ladder 不会耗尽。（w3 保留：Go 编译需要循环后的终结 return。）
//   - 【已于 w3 清理】chain_chat_images.go 241-243：areaScale < scale 需
//     2560000·max(w,h)² < 1024²·w·h，即 min > 2.44×max，与 min ≤ max 矛盾
//     ——面积收缩臂在边长收缩臂之后恒不触发（几何不等式，任意输入都不可能）。
//   - 【已于 w3 清理】chain_chat_images.go 246-249：经 1024 边上限后 patch 数
//     ≤32×32=1024 < 2500，0.98 收缩循环体不可达。
//   - chain_chat_images.go 284：sqrtOf 的输入域为 (0, 2.56e6] 的有理数
//     （chatMaxModelImagePatch*32*32/(w*h)），牛顿迭代对开平方单调收敛，
//     两个收敛出口在 64 次迭代内必触发。（w3 保留：241 面积臂删除后 sqrtOf
//     生产调用点已无，但其直接测试位于非本组文件（w1_chat_tail_test.go /
//     w1i_images_arms_test.go），删除将越界改动，暂留待后续收尾。）
//   - 【已于 w3 清理】chain_chat_images.go 328-330：循环条件（319 行
//     offset+4<=len(data)）已蕴含 328 行的冗余守卫，同一迭代内 offset 未变。
//   - 【已于 w3 清理】chain_chat_images.go 424-426：applyChatOrientation 入口
//     已把 orientation 收窄到 [2,8]，switch 2..8 全枚举，default 不可达。
//   - 【已于 w3 清理】chain_chat_observation.go 276-278：json.Marshal(
//     map[string]any{纯 JSON 值}) 恒成功。
//   - 【已于 w3 清理】chain_chat_observation.go 452-454：chatObservationJSONString
//     对 string 调 json.Marshal，恒成功（string 恒可序列化）。
//   - chain_wiring_w2c.go 216-218：gatewaycircuit.NewMemoryStore(Capacity
//     10_000) 仅容量非法时报错，入参为常量合法值。（w3 保留：外部包构造器
//     调用点守卫。）
//   - chain_wiring_w2c.go 222-224：NewCircuitService 对合法 store 无错误出口
//     （同进程内已按常量参数装配）。（w3 保留：同上。）
//   - 【已于 w3 清理】chain_wiring_w2c.go 301-303/336-338：chainSuppressionPort
//     .FilterAsync 的实现（281-293 行）恒返回 nil error（store.FilterSuppressions
//     只返回结果值），`if err != nil` 两处不可达；Refresh 的 refreshErr 分支同源。
//   - 【已于 w3 清理】chain_wiring_w2c.go 346-348：WaitForStateLoop 仅在
//     Refresh 报错时返回 error（wait.go 引擎的其余出口全部走 finalize 的
//     nil error），Refresh 由 FilterAsync 包裹恒不报错 → waitErr 恒 nil。
//   - chain_compose.go 555-558：gatewaypreauth 组装对全量非 nil deps 无错误
//     出口（组合根逐项 fail-fast 后才到达）。（w3 保留：外部构造器调用点守卫。）
//   - chain_compose.go 805-807：http.NewRequestWithContext 对常量 method/URL/
//     bytes.Reader 无错误出口（仅非法 method/URL/context 报错）。（w3 保留：
//     标准库调用点守卫惯例。）
//   - chain_compose.go 835-837：BuildUpstreamRequest 的转换错误臂——输入 body
//     来自同一请求的解析结果且 mapping 已在主链校验（与主链同语义路径），
//     进程内无已知触发面。（w3 保留：登记自述"无已知触发面"，未证明不可达。）
//   - 【已于 w3 清理】chain_compose.go 1160-1162：url.URL.RequestURI() 对空
//     Path 恒返回 "/"（Go 标准库对空路径的回退，实测 Host 空与非空均为 "/"），
//     strings.Cut 出的 path 恒非空，path == "" 分支不可达。
//   - 【已于 w3 清理】chain_compose.go 1312-1314：crypto/rand.Read 在受支持
//     平台（含 Windows）恒成功，仅 legacy 平台可能失败。（Go 1.24+ 文档契约
//     "never returns an error"。）
//   - 【已于 w3 清理】chain_driver.go 296-298：json.Marshal(map[string]any
//     {model,project,request}) 的值全部来自 JSON 反序列化结果，恒可序列化。
//   - 【已于 w3 清理】chain_driver.go 827-829：json.Marshal(body)——body 为
//     json.Unmarshal 的产物重组（applyCodexResponsesCompatibility 等仅注入
//     纯 JSON 值），恒可序列化。
//   - chain_driver.go 1095-1097：endpoint family switch 的 default 臂——上游
//     family 由协议表面（account mapping endpoint family 白名单）归一化后
//     仅可能为 chat_completions/anthropic_messages/gemini_*。（w3 保留：
//     NormalizeEndpointFamily 对未知值原样透传，default 是对持久化数据的
//     真实守卫。）
//   - chain_driver.go 1105-1108：anthropic 模式 switch 的 default 臂——
//     RequestSupportedEndpointMode 只产出四种已枚举模式（同文件 1103 行）。
//     （w3 保留：default 返回 "", false 是对非 messages 形态的真实过滤，
//     依赖上游协议面检查的完整性，非自包含。）
//   - chain_ports.go 1761-1764/1766-1768：DispatchAuditLog 对 gatewayusage.
//     AuditLogInput 做 Marshal→Unmarshal 往返，纯 JSON 数据结构体，恒成功。
//     （【错误臂已于 w3 清理】；往返本身是保留 wire contract 等价的类型桥。）
//   - chain_openaicompat.go 33-35：openaicompat.NewStore 仅 db==nil 报错，
//     mountChainOpenAICompatFamilies 26-28 行已守卫 composed.db==nil。（w3
//     保留：导出构造器调用点守卫。）
//   - compose.go 283-285/324-326/352-354/367-369：sql.Open("sqlite") 惰性建连
//     恒成功（driver 已注册，错误延迟到 configureSQLiteConnection 首用）。
//     （w3 保留：sql.Open 惰性类惯例守卫。）
//   - compose.go 405-407/440-442：openSQLiteReadOnly 同为惰性 open，对存在
//     文件恒成功（402 行 os.Stat 已守卫存在性）。（w3 保留：同上。）
//   - compose.go 462-464/481-483/502-504/513-515/547-549/552-554/565-567/
//     569-571/583-585/597-599/605-607/615-617/647-649/655-657/675-677/679-681/
//     683-685/693-695/762-764/773-775/867-869/873-875/877-879：各 store 构造器
//     仅在 db==nil / 秘钥为空（apikeys/accounts，空秘钥路径在 572-575 行
//     apikeys.NewStore 先行失败）时报错；句柄来自 270-293 行已守卫的
//     composed.db，构造器无 DDL/查询，组合根内不可达。（w3 保留：导出构造器
//     调用点守卫，保持组合根 fail-fast 语义。）
//   - compose.go 1015-1017/1029-1031：newAccountsRuntimeResetBridge /
//     newChainErrorPolicyEffectsBridge 唯一失败面是 accountkeystates.NewStore
//     的空秘钥守卫（99-101 行），而 composed 秘钥已在 572 行 apikeys.NewStore
//     通过同一非空校验。（w3 保留：同上。）
//   - compose.go 1137-1139 之外的 1161-1163/1168-1170：mountChainOpenAICompat
//     Families 的错误面（nil db/nil cache、NewStore nil db）在 ChainEnabled
//     装配下恒非 nil；openChatDatabase 的 PG 臂共享已成功 Acquire 的池、
//     SQLite 臂 chat 路径已被 preflight 要求非空并 ensure。（w3 保留：调用点
//     守卫。）
//   - 【已于 w3 清理】compose.go 1440-1442：settings.Load 对 SystemSettingKeys
//     全键合并 schema 默认值，`!ok || raw == nil` 不可达。
//   - 【已于 w3 清理】compose.go 1444-1454/1457-1477：settings.loadFromDatabase
//     在读取时逐键走 normalizeSystemSetting 严格校验（数字键必须 float 整数且
//     落在 spec.min/max 内，否则 Load 整体失败）——组合根闭包读到的快照值恒为
//     已归一化的合法 float64，字符串解析臂（1444/1446/1449）、越界臂
//     （1451-1453）与六个 load 错误返回（1457..1477）全部不可达；坏值只能从
//     store.Load 错误出口（1436-1437，已有覆盖）出现。
//   - 【已于 w3 清理】compose.go 493-495：runtime-log 保留期闭包的 parseErr 臂
//     ——闭包读取的 runtimeLogIndexRetentionDays 在 settings.Load 阶段已被
//     normalize 成合法数字（同上 normalizeSystemSetting 证据），readErr 臂
//     （489-491）已有覆盖，Atoi 对归一化值恒成功。
//   - 【已于 w3 清理】compose.go 1578-1579/1580-1581/1582-1586：
//     aiAccountLimitSettingsAdapter 的快照值来自 settings.Load 的 JSON 反序列化
//     （数字恒 float64）+ 缺键合并默认 100（float64），int64/int/default 三臂
//     不可达。
//   - main.go 369-371/407-410：pgx stdlib 经 database/sql 注册后 sql.Open 为
//     惰性建连（DSN 解析在首次使用时），pgpool.Acquire 只做静态配置校验
//     （opener/URL/role 非空、池上限合法，均为常量合法值）；坏 DSN 只让其后的
//     EnsureSchema 失败（实测命中 378/425 的已覆盖臂）。（w3 保留：pgpool
//     Acquire 调用点惯例守卫，判据含运行时配置值。）
//   - 【已于 w3 清理】main.go 620-622：内部注册表与 compose 的共享登录守卫/
//     captcha 服务使用同一个 goredis redis.ParseURL，且 state driver=redis 时
//     守卫先于 610 行组装（authsys.NewRedisNamespacedStateStore 急切解析 URL），
//     任何 NewRegistry 会拒绝的 URL 都让组合根先行失败；Secret 空亦被 compose
//     的 apikeys.NewStore 空秘钥守卫先行拒绝（纯重复判据）。
//   - 【已于 w3 清理】main.go 698-700：supervisor 对组件错误只重试不回传
//     （runComponent 循环重试直到 ctx 结束），Run 的错误出口仅剩"组件定义
//     不完整"预检，而 main 的全部组件定义静态完整且经 nil 守卫追加。
//   - main.go 171-346 各 owner 构造器守卫（171-172/187-188/203-204/222-224/
//     232-234/268-269/272-273/276-277/283-285/288-289/340-341/343-344/346-347）：
//     w3 保留——均为外部导出构造器/挂载方法的调用点守卫，判据含运行时
//     环境值（OwnerGate 等），非纯重复判据。
//   - main.go 689-691：J3b management server 的 Serve 错误仅在 listener 异常
//     关闭时非 ErrServerClosed。（w3 保留：goroutine 错误回传判定是活契约。）
//   - main.go 728-729：runPassiveGateway 的 server.Shutdown 对活跃 loopback
//     listener 恒成功（仅 Shutdown 自身超时/二次关闭才报错）。（w3 保留：
//     Shutdown 超时（慢客户端拖住排空）是真实可达场景。）
//   - chain_ports.go 454-456/631-633/690-692/903-905：usage.RecordFailedUpstream
//     Attempt 的唯一错误出口是收尾队列的单条容量上限（gatewayUsageFinalization
//     TaskMaxBytes = 64MiB，finalization.go 242-244）；这三个失败面输入的
//     BodyText/ErrorMessage 均来自上游失败体的有界捕获（远小于 64MiB），估算
//     归一化另有 2MiB 上限，队列 Dispatch 恒准入，err 恒 nil。（w3 保留：
//     被调函数错误契约真实存在（容量上限校验），当前输入域裕度非类型排除，
//     调用点守卫保留。）
//   - chain_ports.go 1444-1446：G14 IdentityService.Resolve 仅在
//     ValidateGatewaySessionIdentityCandidate 对原始候选校验失败时返回错误；
//     默认解析器族产出的候选来自受控请求头/会话源，本批次未构造出校验失败
//     形态（属 G14 身份域深水区），该 1 句保留待后续。
//   - 【已于 w3 清理】chain_chat_tool_capabilities.go 75-77：chatToolModelOption
//     恒返回非 nil（157-166 行：无目录行时返回空列表视图，注释即声明
//     "option never nil"），option == nil 分支不可达；空目录行为等价落"没有
//     可用的对话路由"。
//   - chain_wiring_w2c.go 91-92：gatewayrouting 选择器只对输入绑定行做重排/
//     权重归一/轮换，输出的 id 恒 ⊆ 输入 id 集，originals 回查不落空。（w3
//     保留：chainCacheBindingRowOf 回查 fallback 是对外部选择器行为契约的
//     防御分支。）
//   - chain_wiring_w2c.go 507-509：gatewayproxyhealth 排序服务对候选/模型优先
//     级输入无错误出口（返回值仅携带排序标记），错误臂不可达。（w3 保留：
//     外部函数调用点守卫。）
//   - chain_wiring_w2c.go 553-555：gatewayhotquality 排序引擎对该输入域同样
//     无错误出口（引擎在组合测试内以合法候选集调用）。（w3 保留：同上。）
//   - chain_wiring_w2c.go 575-579：ExplorationReservation 仅在热质量探索模式
//     产生活跃预留时非 nil；该状态由引擎内部字段流转（与 w1 已登记的
//     AttachReservation 同因，进程内不可注入）。（w3 保留：真实条件分支，
//     探索模式下非 nil。）
//   - storage_bootstrap_physical.go 116/130（w3 保留）：平台条件分支位于
//     跨平台编译文件（无构建标签；平台差异在 physical_windows.go/_unix.go），
//     对应平台真实可达，非死代码。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// ---------------------------------------------------------------------------
// w2c mock database/sql driver：按查询片段脚本化 行集 / 错误 / Scan 形状
// ---------------------------------------------------------------------------

// w2cMockRows 是 driver.Rows 的脚本化实现；nextErr 模拟迭代中错误（rows.Err），
// extraColumn 注入列数不匹配（Scan 错误臂）。
type w2cMockRows struct {
	columns      []string
	values       [][]driver.Value
	nextErr      error
	position     int
	unexpectedOK bool // true 时 Next 迭代一次后注入 nextErr
}

func (r *w2cMockRows) Columns() []string { return r.columns }
func (r *w2cMockRows) Close() error      { return nil }
func (r *w2cMockRows) Next(dest []driver.Value) error {
	if r.nextErr != nil && r.position >= len(r.values) {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	if r.position >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.position])
	r.position++
	return nil
}

// w2cMockResult 支持 RowsAffected 报错（观察结算 221 臂）。
type w2cMockResult struct {
	affected        int64
	rowsAffectedErr error
}

func (r w2cMockResult) LastInsertId() (int64, error) { return 0, nil }
func (r w2cMockResult) RowsAffected() (int64, error) { return r.affected, r.rowsAffectedErr }

// w2cMockStep 按 include 子串匹配查询/执行语句；未命中任何 step 时按
// defaultQuery/defaultExec 的脚本行为兜底。
type w2cMockStep struct {
	include  string
	columns  []string
	values   [][]driver.Value
	queryErr error
	rowsErr  error
	execErr  error
	result   driver.Result
	// emptyFirstN 前 emptyFirstN 次命中返回空行集（sql.ErrNoRows 语义），其后
	// 返回 values——用于“先查无、插入冲突后再查有”的竞态恢复脚本。
	emptyFirstN int
	matched     int
}

type w2cMockConn struct {
	steps  []w2cMockStep
	calls  []string
	mu     sync.Mutex
	closed bool
}

func (c *w2cMockConn) match(query string) *w2cMockStep {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, query)
	for index := range c.steps {
		if strings.Contains(query, c.steps[index].include) {
			step := &c.steps[index]
			step.matched++
			return step
		}
	}
	return nil
}

func (c *w2cMockConn) Prepare(query string) (driver.Stmt, error) {
	return nil, fmt.Errorf("w2c mock: Prepare 不支持: %s", query)
}
func (c *w2cMockConn) Close() error              { c.closed = true; return nil }
func (c *w2cMockConn) Begin() (driver.Tx, error) { return nil, errors.New("w2c mock: Begin 不支持") }

func (c *w2cMockConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step := c.match(query)
	if step == nil {
		return nil, fmt.Errorf("w2c mock: 未脚本化的查询: %s", query)
	}
	if step.queryErr != nil {
		return nil, step.queryErr
	}
	if step.queryErr == nil && step.execErr != nil {
		return nil, step.execErr
	}
	if step.emptyFirstN > 0 && step.matched <= step.emptyFirstN {
		return &w2cMockRows{columns: step.columns}, nil
	}
	return &w2cMockRows{columns: step.columns, values: step.values, nextErr: step.rowsErr}, nil
}

func (c *w2cMockConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	step := c.match(query)
	if step == nil {
		return nil, fmt.Errorf("w2c mock: 未脚本化的执行: %s", query)
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	if step.result != nil {
		return step.result, nil
	}
	return w2cMockResult{affected: 1}, nil
}

type w2cMockConnector struct {
	conn *w2cMockConn
}

func (c *w2cMockConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c *w2cMockConnector) Driver() driver.Driver                        { return w2cMockDriver{} }

type w2cMockDriver struct{}

func (w2cMockDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w2c mock: 用 OpenDB")
}

var w2cMockDriverOnce int

func w2cNewMockDB(t *testing.T, steps []w2cMockStep) *sql.DB {
	t.Helper()
	if w2cMockDriverOnce == 0 {
		w2cMockDriverOnce = 1
		sql.Register("w2cmock", w2cMockDriver{})
	}
	conn := &w2cMockConn{steps: steps}
	db := sql.OpenDB(&w2cMockConnector{conn: conn})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// chain_chat_keys.go 错误臂（78/87/205/214/228-233/312/317/334/339）
// ---------------------------------------------------------------------------

// w2cKeysScript 里的片段名取自各查询的稳定子串。
const (
	w2cQGroups         = "FROM groups WHERE system_account_id"
	w2cQRStratForGroup = "ORDER BY route_strategies.updated_at DESC"
	w2cQGPTStrategy    = "route_strategies.id, route_strategies.name"
	w2cQAPIKeyName     = "SELECT name FROM api_keys"
	w2cQRStratName     = "SELECT name FROM route_strategies"
	w2cQAPIKeysInsert  = "INSERT INTO api_keys"
	w2cQRStratInsert   = "INSERT INTO route_strategies"
	w2cQRSGInsert      = "INSERT INTO route_strategy_groups"
	w2cQChatKeyID      = "purpose = 'chat' LIMIT 1"
	w2cQFindChatKey    = "key_secret_encrypted"
)

func TestW2CChatAPIKeyProviderErrorArms(t *testing.T) {
	// 各用例独立 mock 连接，脚本按 EnsureChatAPIKey / ensureDefaultRouteStrategies
	// 的调用序列命中具体错误臂。
	cases := []struct {
		name    string
		secret  string
		steps   []w2cMockStep
		wantErr string
	}{
		{
			name: "gpt策略查询错误78",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: [][]driver.Value{{"rs_exists"}}},
				{include: w2cQChatKeyID, columns: []string{"id"}, values: nil},
				{include: w2cQGPTStrategy, queryErr: errors.New("w2c: gpt 策略查询失败")},
			},
			wantErr: "gpt 策略查询失败",
		},
		{
			name: "nextDefaultApiKeyName查询错误87",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: [][]driver.Value{{"rs_exists"}}},
				{include: w2cQChatKeyID, columns: []string{"id"}, values: nil},
				{include: w2cQGPTStrategy, columns: []string{"id", "name"}, values: [][]driver.Value{{"rs_gpt", "GPT 默认"}}},
				{include: w2cQAPIKeyName, queryErr: errors.New("w2c: api key 名称查询失败")},
			},
			wantErr: "api key 名称查询失败",
		},
		{
			name: "route策略组查询错误205",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, queryErr: errors.New("w2c: 组内策略查询失败")},
			},
			wantErr: "组内策略查询失败",
		},
		{
			name: "nextDefaultRouteStrategyName查询错误214",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: nil},
				{include: w2cQRStratName, queryErr: errors.New("w2c: 策略名称查询失败")},
			},
			wantErr: "策略名称查询失败",
		},
		{
			name: "route策略名scan错误312",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: [][]driver.Value{{"rs_exists"}}},
				{include: w2cQChatKeyID, columns: []string{"id"}, values: nil},
				{include: w2cQGPTStrategy, columns: []string{"id", "name"}, values: [][]driver.Value{{"rs_gpt", "GPT 默认"}}},
				{include: w2cQAPIKeyName, columns: []string{"a", "b"}, values: [][]driver.Value{{"x", "y"}}},
			},
			wantErr: "expected 2 destination arguments in Scan, not 1",
		},
		{
			name: "route策略名scan错误334",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: nil},
				{include: w2cQRStratName, columns: []string{"a", "b"}, values: [][]driver.Value{{"x", "y"}}},
			},
			wantErr: "expected 2 destination arguments in Scan, not 1",
		},
		{
			name: "apiKeys名称迭代错误317",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: [][]driver.Value{{"rs_exists"}}},
				{include: w2cQChatKeyID, columns: []string{"id"}, values: nil},
				{include: w2cQGPTStrategy, columns: []string{"id", "name"}, values: [][]driver.Value{{"rs_gpt", "GPT 默认"}}},
				{include: w2cQAPIKeyName, columns: []string{"name"}, values: [][]driver.Value{{"AI 对话 API Key"}}, rowsErr: errors.New("w2c: api key 名称迭代失败")},
			},
			wantErr: "api key 名称迭代失败",
		},
		{
			name: "route策略名迭代错误339",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: nil},
				{include: w2cQRStratName, columns: []string{"name"}, values: [][]driver.Value{{"AI 对话路由"}}, rowsErr: errors.New("w2c: 策略名称迭代失败")},
			},
			wantErr: "策略名称迭代失败",
		},
		{
			name: "route策略INSERT冲突且回读为空233",
			steps: []w2cMockStep{
				{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", "默认组", "gpt"}}},
				{include: w2cQRStratForGroup, columns: []string{"id"}, values: nil},
				{include: w2cQRStratName, columns: []string{"name"}, values: nil},
				{include: w2cQRStratInsert, execErr: errors.New("UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name")},
			},
			wantErr: "UNIQUE constraint failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := w2cNewMockDB(t, tc.steps)
			provider := newChatAPIKeyProvider(db, false, "w2c-secret")
			keyID, err := provider.EnsureChatAPIKey("w2c-owner")
			if err == nil {
				t.Fatalf("期望错误 %q，实际 keyID=%q err=nil", tc.wantErr, keyID)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 = %v，want 包含 %q", err, tc.wantErr)
			}
		})
	}

	// 冲突竞态恢复臂（228 条件 + 231 continue）：INSERT 报重名错误但按组回读
	// 到已有策略 → continue 走完后续 API Key 创建。
	t.Run("route策略INSERT冲突竞态恢复231", func(t *testing.T) {
		db := w2cNewMockDB(t, []w2cMockStep{
			{include: w2cQGroups, columns: []string{"id", "name", "provider_code"}, values: [][]driver.Value{{"g1", nil, "gpt"}}},
			{include: w2cQRStratForGroup, columns: []string{"id"}, values: [][]driver.Value{{"rs_exists"}}, emptyFirstN: 1},
			{include: w2cQRStratName, columns: []string{"name"}, values: nil},
			{include: w2cQRStratInsert, execErr: errors.New("UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name")},
			{include: w2cQChatKeyID, columns: []string{"id"}, values: nil},
			{include: w2cQGPTStrategy, columns: []string{"id", "name"}, values: [][]driver.Value{{"rs_gpt", "GPT 默认"}}},
			{include: w2cQAPIKeyName, columns: []string{"name"}, values: nil},
			{include: w2cQAPIKeysInsert, columns: nil, values: nil},
			{include: w2cQRSGInsert, columns: nil, values: nil},
		})
		provider := newChatAPIKeyProvider(db, false, "w2c-secret")
		keyID, err := provider.EnsureChatAPIKey("w2c-owner")
		if err != nil {
			t.Fatalf("竞态恢复路径必须成功: %v", err)
		}
		if !strings.HasPrefix(keyID, "key_") {
			t.Fatalf("keyID 形态异常: %q", keyID)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_chat_tool_capabilities.go 决策矩阵（29-116/142-150）
// ---------------------------------------------------------------------------

type w2cChatKeysFake struct {
	record *chat.ChatAPIKeyRecord
	err    error
}

func (f w2cChatKeysFake) EnsureChatAPIKey(string) (string, error) { return "", nil }
func (f w2cChatKeysFake) FindChatAPIKey(string, string) (*chat.ChatAPIKeyRecord, error) {
	return f.record, f.err
}

type w2cGatewayKeysFake struct {
	view *chat.GatewayKeyView
	err  error
}

func (f w2cGatewayKeysFake) ValidateGatewayKey(string) (*chat.GatewayKeyView, error) {
	return f.view, f.err
}

type w2cModelCatalogFake struct {
	accounts map[string][]chat.ChatTransportAccount
	catalog  []chat.ProviderModelCatalogItem
}

func (f w2cModelCatalogFake) ListAccountsForGroup(groupID, _, requestedModel, endpointFamily string) []chat.ChatTransportAccount {
	key := groupID + "|" + requestedModel + "|" + endpointFamily
	return f.accounts[key]
}
func (f w2cModelCatalogFake) ListProviderCatalog(string, string) []chat.ProviderModelCatalogItem {
	return f.catalog
}

func w2cBoolPtr(value bool) *bool { return &value }
func w2cStrPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// w2cResponsesAccount 构造一个 responses 协议可用的账户（支持模型 gpt-test）。
func w2cResponsesAccount() chat.ChatTransportAccount {
	return chat.ChatTransportAccount{
		ID:                     "w2c-acc",
		Type:                   "api_key",
		ProviderCode:           "openai",
		SupportedEndpointModes: []string{"responses_sse", "chat_sse"},
		SupportedModels:        []string{"gpt-test"},
		ModelMappings: []chat.ChatTransportModelMapping{{
			Enabled:     w2cBoolPtr(true),
			SourceModel: "gpt-test",
		}},
	}
}

// w2cToolPayloadOf 从 payload 提取工具条目，便于断言。
func w2cToolEntryOf(t *testing.T, payload any, id string) map[string]any {
	t.Helper()
	object, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload 形态异常: %#v", payload)
	}
	tools, ok := object["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools 形态异常: %#v", object["tools"])
	}
	for _, entry := range tools {
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("tools 缺少 %s 条目: %#v", id, tools)
	return nil
}

func TestW2CChatToolCapabilitiesResolverMatrix(t *testing.T) {
	ownerID := "w2c-owner"
	validKeyRecord := &chat.ChatAPIKeyRecord{ID: "chat-key", Name: "对话 Key", Secret: "sk-chat", Status: "active"}
	conversation := func(lastModel string, apiKeyID string) *chat.Conversation {
		return &chat.Conversation{ID: "conv", SystemAccountID: ownerID, LastModel: w2cStrPtr(lastModel), APIKeyID: w2cStrPtr(apiKeyID)}
	}
	fullCatalog := func() w2cModelCatalogFake {
		return w2cModelCatalogFake{
			accounts: map[string][]chat.ChatTransportAccount{
				"grp|gpt-test|":                 {w2cResponsesAccount()},
				"grp|gpt-test|responses":        {w2cResponsesAccount()},
				"grp|gpt-test|chat_completions": {w2cResponsesAccount()},
				"grp|gpt-image-2|":              {{ID: "img-acc", Type: "api_key", ProviderCode: "openai"}},
			},
			catalog: []chat.ProviderModelCatalogItem{{Model: "gpt-test", ProviderCode: "openai", SupportedTools: []string{"web_search", "function_calling"}}},
		}
	}

	t.Run("解析闭包29与全可用矩阵", func(t *testing.T) {
		deps := &chat.Deps{
			ChatKeys:     w2cChatKeysFake{record: validKeyRecord},
			GatewayKeys:  w2cGatewayKeysFake{view: &chat.GatewayKeyView{GroupBindings: []chat.GatewayGroupBinding{{GroupID: "grp", Status: "active", GroupEnabled: true}}, ImageGenerationEnabled: true}},
			ModelCatalog: fullCatalog(),
		}
		resolver := newChatToolCapabilitiesResolver(deps)
		payload := resolver(conversation("gpt-test", "chat-key"), ownerID)
		webSearch := w2cToolEntryOf(t, payload, "web_search")
		image := w2cToolEntryOf(t, payload, "generate_image")
		if webSearch["available"] != true || image["available"] != true {
			t.Fatalf("全可用矩阵不符: web=%v image=%v", webSearch, image)
		}
		if _, hasReason := webSearch["reason"]; hasReason {
			t.Fatalf("可用工具不应携带 reason: %#v", webSearch)
		}
	})

	t.Run("模型缺失44", func(t *testing.T) {
		payload := resolveChatToolCapabilities(&chat.Deps{}, conversation("", "chat-key"), ownerID)
		entry := w2cToolEntryOf(t, payload, "web_search")
		if entry["reason"] != "当前会话尚未选择对话模型" {
			t.Fatalf("reason = %v", entry["reason"])
		}
	})

	t.Run("依赖与密钥守卫50-66", func(t *testing.T) {
		// deps nil / ChatKeys nil / conversation nil / APIKeyID nil / 空白 key。
		if payload := resolveChatToolCapabilities(nil, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("deps nil 应落 catch 文案")
		}
		if payload := resolveChatToolCapabilities(&chat.Deps{}, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("ChatKeys nil 应落 catch 文案")
		}
		noKey := conversation("gpt-test", "")
		noKey.APIKeyID = nil
		if payload := resolveChatToolCapabilities(&chat.Deps{}, noKey, ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("APIKeyID nil 应落 catch 文案")
		}
		// FindChatAPIKey 错误。
		errored := &chat.Deps{ChatKeys: w2cChatKeysFake{err: errors.New("w2c: find failed")}}
		if payload := resolveChatToolCapabilities(errored, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("Find 错误应落 catch 文案")
		}
		// 空 secret / 非 active。
		emptySecret := &chat.Deps{ChatKeys: w2cChatKeysFake{record: &chat.ChatAPIKeyRecord{Secret: "", Status: "active"}}}
		if payload := resolveChatToolCapabilities(emptySecret, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("空 secret 应落 catch 文案")
		}
		inactive := &chat.Deps{ChatKeys: w2cChatKeysFake{record: &chat.ChatAPIKeyRecord{Secret: "sk", Status: "disabled"}}}
		if payload := resolveChatToolCapabilities(inactive, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("非 active 应落 catch 文案")
		}
		// GatewayKeys nil / 校验错误 / key 不可用。
		noGateway := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}}
		if payload := resolveChatToolCapabilities(noGateway, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("GatewayKeys nil 应落 catch 文案")
		}
		validateErr := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: w2cGatewayKeysFake{err: errors.New("w2c: validate failed")}}
		if payload := resolveChatToolCapabilities(validateErr, conversation("gpt-test", "chat-key"), ownerID); w2cToolEntryOf(t, payload, "web_search")["reason"] != chatToolCapabilitiesCatchReason {
			t.Fatalf("Validate 错误应落 catch 文案")
		}
		nilKey := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: w2cGatewayKeysFake{}}
		payload := resolveChatToolCapabilities(nilKey, conversation("gpt-test", "chat-key"), ownerID)
		if entry := w2cToolEntryOf(t, payload, "web_search"); entry["reason"] != "会话绑定的 API Key 不可用" {
			t.Fatalf("gatewayKey nil reason = %v", entry["reason"])
		}
	})

	t.Run("目录与协议原因矩阵74-111", func(t *testing.T) {
		gateway := w2cGatewayKeysFake{view: &chat.GatewayKeyView{GroupBindings: []chat.GatewayGroupBinding{{GroupID: "grp"}}, ImageGenerationEnabled: true}}
		// 目录为空（无账户无目录行）→ option 恒非 nil（空列表），落“无对话路由”。
		emptyCatalog := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: gateway, ModelCatalog: w2cModelCatalogFake{}}
		if entry := w2cToolEntryOf(t, resolveChatToolCapabilities(emptyCatalog, conversation("gpt-test", "chat-key"), ownerID), "web_search"); entry["reason"] != "当前 API Key 没有可用的对话路由" {
			t.Fatalf("空目录 reason = %v", entry["reason"])
		}
		// 无任何账户 → 双“没有可用的对话路由”。
		noAccounts := w2cModelCatalogFake{catalog: []chat.ProviderModelCatalogItem{{Model: "gpt-test", SupportedTools: []string{"web_search", "function_calling"}}}}
		noRoute := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: gateway, ModelCatalog: noAccounts}
		payload := resolveChatToolCapabilities(noRoute, conversation("gpt-test", "chat-key"), ownerID)
		if entry := w2cToolEntryOf(t, payload, "web_search"); entry["reason"] != "当前 API Key 没有可用的对话路由" {
			t.Fatalf("无路由 web reason = %v", entry["reason"])
		}
		if entry := w2cToolEntryOf(t, payload, "generate_image"); entry["reason"] != "当前 API Key 没有可用的对话路由" {
			t.Fatalf("无路由 image reason = %v", entry["reason"])
		}
		// 仅 chat_completions 协议：web 落“路由不支持 Responses”，image 落缺
		// gpt-image-2 账户的 default 臂。
		chatOnlyAccount := w2cResponsesAccount()
		chatOnlyAccount.SupportedEndpointModes = []string{"chat_sse"}
		chatOnly := w2cModelCatalogFake{
			accounts: map[string][]chat.ChatTransportAccount{
				"grp|gpt-test|":                 {chatOnlyAccount},
				"grp|gpt-test|chat_completions": {chatOnlyAccount},
			},
			catalog: []chat.ProviderModelCatalogItem{{Model: "gpt-test", SupportedTools: []string{"web_search", "function_calling"}, SupportedAPIProtocols: []string{"chat_completions"}}},
		}
		chatOnlyDeps := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: gateway, ModelCatalog: chatOnly}
		payload = resolveChatToolCapabilities(chatOnlyDeps, conversation("gpt-test", "chat-key"), ownerID)
		if entry := w2cToolEntryOf(t, payload, "web_search"); entry["reason"] != "当前路由不支持 Responses 网页搜索" {
			t.Fatalf("chat-only web reason = %v", entry["reason"])
		}
		if entry := w2cToolEntryOf(t, payload, "generate_image"); entry["reason"] != "当前 API Key 路由没有可用的 gpt-image-2 API Key 账户" {
			t.Fatalf("chat-only image reason = %v", entry["reason"])
		}
		// 模型不支持 web_search（95-96）。
		noWebSearch := fullCatalog()
		noWebSearch.catalog = []chat.ProviderModelCatalogItem{{Model: "gpt-test", SupportedTools: []string{"function_calling"}}}
		noWebSearchDeps := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: gateway, ModelCatalog: noWebSearch}
		if entry := w2cToolEntryOf(t, resolveChatToolCapabilities(noWebSearchDeps, conversation("gpt-test", "chat-key"), ownerID), "web_search"); entry["reason"] != "当前模型不支持网页搜索" {
			t.Fatalf("不支持搜索 reason = %v", entry["reason"])
		}
		// 图片生成权限关闭（106-107）。
		noPermission := w2cGatewayKeysFake{view: &chat.GatewayKeyView{GroupBindings: []chat.GatewayGroupBinding{{GroupID: "grp"}}, ImageGenerationEnabled: false}}
		noPermissionDeps := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: noPermission, ModelCatalog: fullCatalog()}
		if entry := w2cToolEntryOf(t, resolveChatToolCapabilities(noPermissionDeps, conversation("gpt-test", "chat-key"), ownerID), "generate_image"); entry["reason"] != "当前用户未开启图片生成" {
			t.Fatalf("权限关闭 reason = %v", entry["reason"])
		}
		// 不支持 function_calling（108-109）。
		noFunctionCalling := fullCatalog()
		noFunctionCalling.catalog = []chat.ProviderModelCatalogItem{{Model: "gpt-test", SupportedTools: []string{"web_search"}}}
		noFunctionCalling.accounts["grp|gpt-image-2|"] = []chat.ChatTransportAccount{{ID: "img-acc", Type: "api_key"}}
		noFCDeps := &chat.Deps{ChatKeys: w2cChatKeysFake{record: validKeyRecord}, GatewayKeys: gateway, ModelCatalog: noFunctionCalling}
		if entry := w2cToolEntryOf(t, resolveChatToolCapabilities(noFCDeps, conversation("gpt-test", "chat-key"), ownerID), "generate_image"); entry["reason"] != "当前模型不支持函数工具调用" {
			t.Fatalf("不支持函数调用 reason = %v", entry["reason"])
		}
	})

	t.Run("chatToolTrimmedModel142-150", func(t *testing.T) {
		if got := chatToolTrimmedModel(nil); got != nil {
			t.Fatalf("nil conversation 应返回 nil: %#v", got)
		}
		if got := chatToolTrimmedModel(&chat.Conversation{}); got != nil {
			t.Fatalf("无模型应返回 nil: %#v", got)
		}
		blank := "   "
		if got := chatToolTrimmedModel(&chat.Conversation{LastModel: &blank}); got != nil {
			t.Fatalf("空白模型应返回 nil: %#v", got)
		}
		model := "  gpt-test  "
		if got := chatToolTrimmedModel(&chat.Conversation{LastModel: &model}); got != "gpt-test" {
			t.Fatalf("修剪结果 = %#v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_chat.go 派发原语（79/91/110/124/138）
// ---------------------------------------------------------------------------

func TestW2CChatDispatchPrimitives(t *testing.T) {
	t.Run("commitHeader重复提交79", func(t *testing.T) {
		reader, pipe := io.Pipe()
		writer := newChatPipeWriter(pipe)
		writer.commitHeader(http.StatusOK)
		writer.commitHeader(http.StatusTeapot) // 二次提交被 79 行守卫吞掉
		if writer.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", writer.status)
		}
		_ = reader.Close()
		writer.finish()
	})

	t.Run("WriteHeader显式调用91", func(t *testing.T) {
		reader, pipe := io.Pipe()
		writer := newChatPipeWriter(pipe)
		writer.WriteHeader(http.StatusAccepted)
		if writer.status != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", writer.status)
		}
		_ = reader.Close()
		writer.finish()
	})

	t.Run("路径补斜杠110与头透传124", func(t *testing.T) {
		var gotPath, gotHeader string
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotHeader = r.Header.Get("X-W2C-Marker")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		executor := newChatGatewayExecutor(handler)
		response, err := executor.Dispatch(context.Background(), chat.GenerationDispatchRequest{
			Path:    "v1/chat/completions",
			Method:  "POST",
			Headers: map[string]string{"X-W2C-Marker": "w2c-value"},
			Body:    []byte("{}"),
		})
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		if gotPath != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
		}
		if gotHeader != "w2c-value" {
			t.Fatalf("header 透传失败: %q", gotHeader)
		}
		if response.Status != http.StatusOK || string(body) != "ok" {
			t.Fatalf("响应不符: status=%d body=%s", response.Status, string(body))
		}
	})

	t.Run("请求上下文取消138", func(t *testing.T) {
		block := make(chan struct{})
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-block:
			case <-r.Context().Done():
			}
		})
		executor := newChatGatewayExecutor(handler)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(80 * time.Millisecond)
			cancel()
		}()
		_, err := executor.Dispatch(ctx, chat.GenerationDispatchRequest{Body: []byte("{}")})
		close(block)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_chat_images.go 几何臂（241/290）
// ---------------------------------------------------------------------------

func TestW2CChatImageGeometryArms(t *testing.T) {
	// 241：面积上限臂（1024×3000 面积超过 2500 patch 预算 → areaScale 收缩）。
	target := boundedChatImageDimensions(1024, 3000)
	if target.width <= 0 || target.height <= 0 {
		t.Fatalf("收缩结果非法: %+v", target)
	}
	if target.height >= 3000 {
		t.Fatalf("面积上限未生效: %+v", target)
	}
	if chatImagePatchCount(target.width, target.height) > chatMaxModelImagePatch {
		t.Fatalf("patch 数仍超预算: %+v", target)
	}
	// 290：等尺寸 resize 走 toNRGBA 快路。
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for index := range src.Pix {
		src.Pix[index] = 128
	}
	resized := resizeChatImage(src, 3, 2)
	bounds := resized.Bounds()
	if bounds.Dx() != 3 || bounds.Dy() != 2 {
		t.Fatalf("等尺寸 resize 尺寸变化: %+v", bounds)
	}
	if _, isNRGBA := resized.(*image.NRGBA); !isNRGBA {
		t.Fatalf("等尺寸 resize 应返回 NRGBA: %T", resized)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_observation.go 观察臂（221/249/313）
// ---------------------------------------------------------------------------

type w2cObservationExecutor struct {
	status int
	body   string
	err    error
}

func (e *w2cObservationExecutor) Dispatch(context.Context, chat.GenerationDispatchRequest) (*chat.GenerationDispatchResponse, error) {
	if e.err != nil {
		return nil, e.err
	}
	return &chat.GenerationDispatchResponse{
		Status: e.status,
		Header: http.Header{},
		Body:   io.NopCloser(strings.NewReader(e.body)),
	}, nil
}

func w2cNewObservationStore(t *testing.T, steps []w2cMockStep, objects chat.ObjectStore, executor chat.GenerationExecutor) *chatImageObservations {
	t.Helper()
	db := w2cNewMockDB(t, steps)
	return newChatImageObservations(db, false, objects, executor)
}

func TestW2CChatObservationArms(t *testing.T) {
	root := t.TempDir()

	t.Run("资产数据URL缺失249", func(t *testing.T) {
		store := w2cNewObservationStore(t, []w2cMockStep{
			{include: "RETURNING observation_revision", columns: []string{"observation_revision", "observation_claim_id"}, values: [][]driver.Value{{int64(1), "w2c-claim-1"}}},
			{include: "SELECT storage_key", columns: []string{"storage_key"}, values: nil},
		}, nil, &w2cObservationExecutor{})
		err := store.runObservation(context.Background(), chat.ScheduleObservationInput{
			ConversationID: "conv", SystemAccountID: ownerIDAware(), Model: "gpt-test",
		}, chat.ObservationTarget{AssetID: "asset-1", ExpectedTurnID: "turn", ExpectedMessageID: "msg"})
		if err == nil || !strings.Contains(err.Error(), "chat_image_observation_asset_missing") {
			t.Fatalf("err = %v, want asset_missing", err)
		}
	})

	t.Run("setObservation行数错误221", func(t *testing.T) {
		store := w2cNewObservationStore(t, []w2cMockStep{
			{include: "observation_claimed_at = NULL", result: w2cMockResult{rowsAffectedErr: errors.New("w2c: rows affected 失败")}},
		}, nil, &w2cObservationExecutor{})
		_, err := store.setObservation(context.Background(), "asset", "conv", "sys", "ready", map[string]any{"summary": "ok"}, 1, "claim", "2026-09-14T00:00:00.000Z")
		if err == nil || !strings.Contains(err.Error(), "rows affected 失败") {
			t.Fatalf("err = %v, want rows affected 失败", err)
		}
	})

	t.Run("完成结算写错误313", func(t *testing.T) {
		// 资产对象文件：4 字节，与 SELECT 返回的 processed_bytes/sha 一致。
		data := []byte("w2c!")
		objects := newChatAssetObjectStoreForTest(t, root, map[string][]byte{"asset-key": data})
		sum := chatAssetSHA256Hex(data)
		store := w2cNewObservationStore(t, []w2cMockStep{
			{include: "RETURNING observation_revision", columns: []string{"observation_revision", "observation_claim_id"}, values: [][]driver.Value{{int64(3), "w2c-claim-2"}}},
			{include: "SELECT storage_key", columns: []string{"storage_key", "processed_mime_type", "processed_bytes", "processed_sha256", "source_kind"},
				values: [][]driver.Value{{"asset-key", "image/png", int64(len(data)), sum, "user_upload"}}},
			{include: "observation_claimed_at = NULL", execErr: errors.New("w2c: 结算写失败")},
		}, objects, &w2cObservationExecutor{status: 200, body: `{"output_text":"{\"summary\":\"w2c 总结\"}"}`})
		err := store.runObservation(context.Background(), chat.ScheduleObservationInput{
			ConversationID: "conv", SystemAccountID: "sys", Model: "gpt-test",
		}, chat.ObservationTarget{AssetID: "asset-1", ExpectedTurnID: "turn", ExpectedMessageID: "msg"})
		if err == nil || !strings.Contains(err.Error(), "结算写失败") {
			t.Fatalf("err = %v, want 结算写失败", err)
		}
	})
}

func ownerIDAware() string { return "w2c-sys" }

// newChatAssetObjectStoreForTest 直接落一个 chat 资产对象存储并写入测试文件。
func newChatAssetObjectStoreForTest(t *testing.T, root string, files map[string][]byte) *chatAssetObjectStore {
	t.Helper()
	store, err := newChatAssetObjectStore(root)
	if err != nil {
		t.Fatalf("create object store: %v", err)
	}
	for name, data := range files {
		if err := store.Write(name, data, int64(len(data))+16, ""); err != nil {
			t.Fatalf("write asset %s: %v", name, err)
		}
	}
	return store
}

// ---------------------------------------------------------------------------
// storage_bootstrap.go（37/76/80/93）与 storage_bootstrap_physical.go（97/162）
// ---------------------------------------------------------------------------

// w2cFullPreflightConfig 返回六个库路径齐全的 runtimeConfig（不要求文件已存在）。
func w2cFullPreflightConfig(t *testing.T) runtimeConfig {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "codex-context"), 0o750); err != nil {
		t.Fatalf("create shard root: %v", err)
	}
	return runtimeConfig{
		Secret:                   "w2c-preflight-secret",
		BusinessDatabasePath:     filepath.Join(root, "business.sqlite3"),
		StatsDatabasePath:        filepath.Join(root, "stats.sqlite3"),
		ChatDatabasePath:         filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:      filepath.Join(root, "dataset.sqlite3"),
		RuntimeLogDatabasePath:   filepath.Join(root, "runtime-log.sqlite3"),
		TableMonitorDatabasePath: filepath.Join(root, "table-monitor.sqlite3"),
		UsageCatalogDatabasePath: filepath.Join(root, "usage-catalog.sqlite3"),
		CodexContextShardRoot:    filepath.Join(root, "codex-context"),
		CodexContextShardCount:   1,
	}
}

// w2cOpenSeededBusiness 打开并 ensure+seed 一个真实业务库。
func w2cOpenSeededBusiness(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatalf("open business: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure business schema: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(ctx, db, bootstrap.SeedOptions{Now: time.Now, Secret: "w2c-preflight-secret"}); err != nil {
		t.Fatalf("seed business: %v", err)
	}
	return db
}

// w2cSabotageSchemaIndex 把库中第一个真实索引对应的表换成残缺列，保证下一次
// EnsureSQLiteSchema 的 CREATE INDEX 因 no such column 失败。
func w2cSabotageSchemaIndex(t *testing.T, path string) {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatalf("open sabotage target: %v", err)
	}
	defer db.Close()
	var indexName, tableName string
	if err := db.QueryRow(`SELECT name, tbl_name FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND tbl_name NOT LIKE 'sqlite_%' ORDER BY name LIMIT 1`).Scan(&indexName, &tableName); err != nil {
		t.Fatalf("find index: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP INDEX "%s"`, indexName)); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP TABLE "%s"`, tableName)); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE "%s" (w2c_junk_column INTEGER)`, tableName)); err != nil {
		t.Fatalf("recreate junk table: %v", err)
	}
}

func TestW2CStoragePreflightFailureArms(t *testing.T) {
	t.Run("业务库schema失败37", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		// 业务库文件先写入垃圾字节 → EnsureSQLiteSchema 的 DDL 失败。
		if err := os.WriteFile(cfg.BusinessDatabasePath, []byte("definitely not a sqlite database"), 0o644); err != nil {
			t.Fatalf("write garbage: %v", err)
		}
		db, err := sql.Open("sqlite", cfg.BusinessDatabasePath)
		if err != nil {
			t.Fatalf("open garbage db: %v", err)
		}
		defer func() { _ = db.Close() }()
		err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, db)
		if err == nil || !strings.Contains(err.Error(), "ensure business sqlite schema") {
			t.Fatalf("err = %v, want ensure business sqlite schema", err)
		}
	})

	t.Run("辅助库打开失败76", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		business := w2cOpenSeededBusiness(t, cfg.BusinessDatabasePath)
		// stats 路径落在一个“文件充当目录”的位置 → OpenSQLiteFile 的
		// MkdirAll 失败。
		blocker := filepath.Join(t.TempDir(), "blocker-file")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		cfg.StatsDatabasePath = filepath.Join(blocker, "stats.sqlite3")
		err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, business)
		if err == nil || !strings.Contains(err.Error(), "create sqlite directory") {
			t.Fatalf("err = %v, want create sqlite directory", err)
		}
	})

	t.Run("辅助库schema失败80", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		business := w2cOpenSeededBusiness(t, cfg.BusinessDatabasePath)
		// 先真实建 stats schema，再破坏一个索引列集 → 重跑失败。
		stats, err := bootstrap.OpenSQLiteFile(cfg.StatsDatabasePath)
		if err != nil {
			t.Fatalf("open stats: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaStats, stats); err != nil {
			_ = stats.Close()
			cancel()
			t.Fatalf("ensure stats baseline: %v", err)
		}
		_ = stats.Close()
		cancel()
		w2cSabotageSchemaIndex(t, cfg.StatsDatabasePath)
		err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, business)
		if err == nil || !strings.Contains(err.Error(), "ensure stats sqlite schema") {
			t.Fatalf("err = %v, want ensure stats sqlite schema", err)
		}
	})

	t.Run("codex分片schema失败93", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		business := w2cOpenSeededBusiness(t, cfg.BusinessDatabasePath)
		shardPath := filepath.Join(cfg.CodexContextShardRoot, "state-000.sqlite3")
		shard, err := bootstrap.OpenSQLiteFile(shardPath)
		if err != nil {
			t.Fatalf("open shard: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaCodexContext, shard); err != nil {
			_ = shard.Close()
			cancel()
			t.Fatalf("ensure shard baseline: %v", err)
		}
		_ = shard.Close()
		cancel()
		w2cSabotageSchemaIndex(t, shardPath)
		err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, business)
		if err == nil || !strings.Contains(err.Error(), "ensure codex-context[0] sqlite schema") {
			t.Fatalf("err = %v, want ensure codex-context[0] sqlite schema", err)
		}
	})
}

func TestW2CPhysicalGateArms(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("未验证平台 %s", runtime.GOOS)
	}

	t.Run("非法路径97", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		business := w2cOpenSeededBusiness(t, cfg.BusinessDatabasePath)
		// Windows 保留字符让 Lstat 产生非 NotExist 错误 → CanonicalPath 失败。
		cfg.StatsDatabasePath = filepath.Join(filepath.Dir(cfg.StatsDatabasePath), "w2c<bad>.sqlite3")
		err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, business)
		if err == nil || !strings.Contains(err.Error(), "无法解析为物理文件") {
			t.Fatalf("err = %v, want 无法解析为物理文件", err)
		}
	})

	t.Run("硬链接同文件碰撞162", func(t *testing.T) {
		cfg := w2cFullPreflightConfig(t)
		business := w2cOpenSeededBusiness(t, cfg.BusinessDatabasePath)
		seed, err := bootstrap.OpenSQLiteFile(cfg.ChatDatabasePath)
		if err != nil {
			t.Fatalf("create chat file: %v", err)
		}
		if err := seed.Close(); err != nil {
			t.Fatalf("close chat file: %v", err)
		}
		link := cfg.ChatDatabasePath + ".hardlink"
		if linkErr := os.Link(cfg.ChatDatabasePath, link); linkErr != nil {
			t.Skipf("当前文件系统不支持硬链接（跳过碰撞臂）: %v", linkErr)
		}
		t.Cleanup(func() { _ = os.Remove(link) })
		cfg.StatsDatabasePath = link
		err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, business)
		if err == nil || !strings.Contains(err.Error(), "指向同一个 SQLite 物理文件") {
			t.Fatalf("err = %v, want 指向同一个 SQLite 物理文件", err)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_wiring_w2c.go 343：恢复等待 Refresh 在屏蔽过期后返回可继续状态
// ---------------------------------------------------------------------------

func TestW2CSuppressionRefreshRecoversAfterExpiry(t *testing.T) {
	store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})
	port := chainSuppressionPort{
		store:  store,
		waiter: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil),
	}
	accounts := []gatewaydispatch.AccountCandidate{{ID: "w2c-acc-a"}, {ID: "w2c-acc-b"}}
	// 两个候选都屏蔽 350ms → 全屏蔽 → 进入恢复等待窗口；过期后的 Refresh
	// 命中 343 行（NextRetryAfterMs == nil → 继续派发）。
	for _, account := range accounts {
		store.Suppress(account.ID, 350, "w2c test failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	}
	startedAt := time.Now()
	startedAtMs := startedAt.UnixMilli()
	resolved, completed, err := port.ResolveLocalSuppressionFilter(context.Background(), gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts:          accounts,
		GroupID:           "w2c-grp",
		SystemAccountID:   "w2c-sys",
		APIKeyID:          "w2c-key",
		StartedAt:         startedAtMs,
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(8000, gatewaypreauth.SystemClock{}),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if completed || resolved == nil {
		t.Fatalf("恢复后应返回过滤器: resolved=%v completed=%v", resolved != nil, completed)
	}
	if elapsed := time.Since(startedAt); elapsed < 300*time.Millisecond {
		t.Fatalf("未经历恢复等待窗口: %v", elapsed)
	}
	if resolved.AllSuppressed || resolved.SuppressedCount != 0 {
		t.Fatalf("恢复后过滤器仍报屏蔽: %+v", resolved)
	}
}

// ---------------------------------------------------------------------------
// compose.go 限流设置坏值臂（1444/1446/1449/1457..1477）+ 493 保留期坏值
// ---------------------------------------------------------------------------

// w2cRateLimitKeys 顺序与 compose.go 的 load 调用顺序一致。
var w2cRateLimitKeys = []string{
	"systemApiRateLimitIpReadPerMinute",
	"systemApiRateLimitIpReadBurstPer10Seconds",
	"systemApiRateLimitIpWritePerMinute",
	"systemApiRateLimitIpWriteBurstPer10Seconds",
	"systemApiRateLimitUserReadPerMinute",
	"systemApiRateLimitUserWritePerMinute",
}

// w2cComposeWithSetting 以 stack 为模板组装组合根，并把一条全局 system_settings
// 行预写入业务库（preflight ensure 幂等，不清理已存在行）。
func w2cComposeWithSetting(t *testing.T, key, valueJSON string) *composition {
	t.Helper()
	stack := w1oNewComposeStack(t)
	business, err := bootstrap.OpenSQLiteFile(stack.cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business for seed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaBusiness, business); err != nil {
		_ = business.Close()
		t.Fatalf("ensure business for seed: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(ctx, business, bootstrap.SeedOptions{Now: time.Now, Secret: stack.cfg.Secret}); err != nil {
		_ = business.Close()
		t.Fatalf("seed business for seed: %v", err)
	}
	if key != "" {
		if _, err := business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, '2026-09-14T00:00:00.000Z')
			ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json`, key, valueJSON); err != nil {
			_ = business.Close()
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}
	if err := business.Close(); err != nil {
		t.Fatalf("close business after seed: %v", err)
	}
	composed, err := composeSystemAPI(stack.cfg, pgpool.NewRegistry(), stack.store, stack.lease, stack.auditProducer, stack.auditConfig, composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	t.Cleanup(composed.Shutdown)
	return composed
}

// w2cGetCaptcha 打一次免鉴权管理面请求，触发内核 IP 限流的设置读取。
func w2cGetCaptcha(t *testing.T, composed *composition) int {
	t.Helper()
	code, _ := w2cGetCaptchaWithBody(t, composed)
	return code
}

func w2cGetCaptchaWithBody(t *testing.T, composed *composition) (int, string) {
	t.Helper()
	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL + "/__aisys__/api/auth/captcha")
	if err != nil {
		t.Fatalf("GET captcha: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return response.StatusCode, string(body)
}

func TestW2CComposeRateLimitSettingErrorArms(t *testing.T) {
	// settings.Load 对每个键做 normalizeSystemSetting 严格校验（数字键必须为
	// 整数且落在 min/max 内，否则 Load 整体报错）——组合根的 ratelimit 设置
	// 闭包读到的快照值恒为合法 float64，compose.go 1440-1477 的字符串解析、
	// 越界与六个 load 错误返回臂全部不可达（见文件头死代码登记）。本用例保留
	// 一条坏设置 → 限流面 500 的契约回归（错误从 store.Load 传播）。
	composed := w2cComposeWithSetting(t, w2cRateLimitKeys[0], `"abc"`)
	if code := w2cGetCaptcha(t, composed); code != http.StatusInternalServerError {
		t.Fatalf("坏设置应让限流面 500，实际 %d", code)
	}
}

// ---------------------------------------------------------------------------
// compose.go 1039/1040（账户锁失效回调）+ 1137（可恢复等待者唤醒）
// ---------------------------------------------------------------------------

// w2cComposeChainStack 组装一个开启链条、带真实上游的 SQLite 组合根，并按
// w1l 手法向业务库补 /v1 所需的分组/账户/路由/API Key 行。
func w2cComposeChainStack(t *testing.T, upstream *httptest.Server) *composition {
	t.Helper()
	stack := w1oNewComposeStack(t)
	stack.cfg.ChainEnabled = true
	stack.cfg.ChatMaxTurnsPerConversation = 50
	// httptest loopback 上游：与 chainSmokeDeps 同一约定（D-192/D-146 生产
	// 默认不受影响）。
	stack.cfg.UpstreamURLSecurity = sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}

	business, err := bootstrap.OpenSQLiteFile(stack.cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business for chain seed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaBusiness, business); err != nil {
		_ = business.Close()
		t.Fatalf("ensure business for chain seed: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(ctx, business, bootstrap.SeedOptions{Now: time.Now, Secret: stack.cfg.Secret}); err != nil {
		_ = business.Close()
		t.Fatalf("seed business for chain seed: %v", err)
	}
	w2cSeedChainRows(t, business, upstream.URL)
	if err := business.Close(); err != nil {
		t.Fatalf("close business after chain seed: %v", err)
	}
	composed, err := composeSystemAPI(stack.cfg, pgpool.NewRegistry(), stack.store, stack.lease, stack.auditProducer, stack.auditConfig, composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose chain stack: %v", err)
	}
	t.Cleanup(composed.Shutdown)
	return composed
}

// w2cSeedChainRows 复刻 w1lSeedChainBusinessRows 的最小集（单组单账户单 Key），
// 列名对齐 maintenance 真实 DDL。
func w2cSeedChainRows(t *testing.T, db *sql.DB, upstreamBaseURL string) {
	t.Helper()
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('w2c_group', 'sys_admin', 'W2C 分组', 'openai', 1, 'personal', ?, ?)`, now, now)
	sealed, err := accounts.EncryptJSON("compose-test-secret", map[string]any{
		"api_key":  "sk-w2c-upstream",
		"base_url": upstreamBaseURL,
	})
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, health_check_model, health_check_endpoint_mode,
			created_at, updated_at
		) VALUES ('w2c_acc', 'sys_admin', 'openai', 'profile_openai_openai_v1', 'openai', 'v1',
			'W2C 账户', 'api_key', 'active', 1, ?, 'gpt-test', 'chat_json', ?, ?)`,
		sealed, now, now)
	seed(`INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w2c_group', 'w2c_acc', 1, ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES ('w2c_acc', 'openai', 'gpt-test', ?)`, now)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at)
		VALUES ('w2c_rs', 'sys_admin', 'W2C 路由', 'normal', 'active', ?, ?)`, now, now)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('w2c_rsg', 'w2c_rs', 'sys_admin', 'w2c_group', 1, 1, 'active', ?, ?)`, now, now)
	secret := "sk-w2c-gateway-key"
	secretEncrypted, err := accounts.EncryptJSON("compose-test-secret", map[string]any{"key": secret})
	if err != nil {
		t.Fatalf("encrypt key secret: %v", err)
	}
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, created_at, updated_at)
		VALUES ('w2c_key', 'sys_admin', 'w2c_rs', 'W2C Key', ?, ?, ?, ?, 'active', ?, ?)`,
		gatewayruntimecache.HashSecret(secret), secret[:8], secret[len(secret)-8:], secretEncrypted, now, now)
	seed(`INSERT INTO provider_model_catalog (id, provider_code, model, status, catalog_order, supported_api_protocols_json, source, catalog_visible, created_at, updated_at)
		VALUES ('w2c_cat', 'openai', 'gpt-test', 'active', 0, '["chat_completions"]', 'builtin', 1, ?, ?)`, now, now)
}

func TestW2CComposeAccountLockInvalidationCallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"w2c upstream down"}}`))
	}))
	defer upstream.Close()
	composed := w2cComposeChainStack(t, upstream)

	locks, ok := composed.chain.engine.Locks.(*chainAccountLocks)
	if !ok {
		t.Fatalf("engine.Locks 类型异常: %T", composed.chain.engine.Locks)
	}
	// ENGAGED + 已到期 + original_status=active 的锁行 → SettleDeadlineAsync
	// 走 DEAD_CONFIRMED + accounts.status='temporary_unavailable' → 组合根的
	// 失效回调（1039/1040）以 account_lock_deadline 触发 K5 总线。
	now := "2026-09-13T00:00:00.000Z"
	seeded := []string{
		`INSERT INTO account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
			incident_id, incident_started_at, deadline_at, original_status, provenance, generation, updated_at)
		VALUES ('w2c_acc', 1, 'ENGAGED', 300, 30, 'w2c-incident', '` + now + `', '2026-09-13T00:05:00.000Z', 'active', 'lock_policy', 1, '` + now + `')`,
	}
	for _, statement := range seeded {
		if _, err := composed.DB.Exec(statement); err != nil {
			t.Fatalf("seed lock row: %v", err)
		}
	}
	invalidations := make(chan string, 4)
	unsubscribe := composed.Bus.Subscribe(inval.TopicGatewayRuntime, func(topic, reason string) {
		select {
		case invalidations <- reason:
		default:
		}
	})
	defer unsubscribe()
	if err := locks.SettleDeadlineAsync(context.Background(), "w2c_acc", time.Now().UnixMilli(), nil); err != nil {
		t.Fatalf("SettleDeadlineAsync: %v", err)
	}
	select {
	case reason := <-invalidations:
		if reason != "account_lock_deadline" {
			t.Fatalf("reason = %q, want account_lock_deadline", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("账户锁失效回调未触发 K5 失效")
	}
	var status string
	if err := composed.DB.QueryRow(`SELECT status FROM accounts WHERE id = 'w2c_acc'`).Scan(&status); err != nil {
		t.Fatalf("read account status: %v", err)
	}
	if status != "temporary_unavailable" {
		t.Fatalf("账户状态 = %q, want temporary_unavailable", status)
	}
}

func TestW2CComposeWakeRecoverableWaiterOnLeaseRelease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"w2c wake upstream down"}}`))
	}))
	defer upstream.Close()
	composed := w2cComposeChainStack(t, upstream)

	// 组合根已把 1137 闭包接到 gatewaydispatch 全局唤醒钩子；屏蔽 → 等待窗口
	// → 恢复 → 单账户派发申请半开租约 → 失败释放租约 → NotifyOneForRuntimeKey。
	store := composed.chainServices.SuppressionStore
	if store == nil {
		t.Fatal("chainServices.SuppressionStore 未装配")
	}
	store.Suppress("w2c_acc", 300, "w2c wake failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)

	key := "sk-w2c-gateway-key"
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"w2c wake"}],"stream":false}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	composed.chain.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 派发在屏蔽恢复后申请并释放半开租约 → 1137 闭包被全局钩子调用
	// （wait coordinator 侧无可观察状态，行为断言为 503 契约不回上游文案）。
	if strings.Contains(recorder.Body.String(), "w2c wake upstream down") {
		t.Fatalf("上游诊断文案不应外泄: %s", recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// main.go 引导失败臂（369/388/408/442/514/620）——插桩二进制场景
// ---------------------------------------------------------------------------

// w2cBootRoot 汇总一次引导场景的全部本地 fixture（SQLite/文件，全部落在
// t.TempDir，不触碰共享 dev PG/Redis）。
type w2cBootRoot struct {
	root             string
	businessPath     string
	auditPath        string
	operationPath    string
	businessEvidence string
	healthAddress    string
	mainAddress      string
}

func w2cNewBootRoot(t *testing.T, epoch string) *w2cBootRoot {
	t.Helper()
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite")
	db, err := bootstrap.OpenSQLiteFile(businessPath)
	if err != nil {
		t.Fatalf("open business: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := bootstrap.EnsureSQLiteSchema(ctx, bootstrap.SQLiteSchemaBusiness, db); err != nil {
		_ = db.Close()
		t.Fatalf("ensure business: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(ctx, db, bootstrap.SeedOptions{Now: time.Now, Secret: "w2c-boot-secret"}); err != nil {
		_ = db.Close()
		t.Fatalf("seed business: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close business: %v", err)
	}
	for _, dir := range []string{"codex-shards", "audit-blobs", "audit-hot", "usage-spool", "logs", "business-evidence"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "business-settings.sqlite"), nil, 0o644); err != nil {
		t.Fatalf("write business settings file: %v", err)
	}
	evidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "business-evidence"), epoch)
	return &w2cBootRoot{
		root:             root,
		businessPath:     businessPath,
		auditPath:        filepath.Join(root, "audit.sqlite"),
		operationPath:    filepath.Join(root, "operation.sqlite"),
		businessEvidence: evidence,
		healthAddress:    fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t)),
		mainAddress:      fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t)),
	}
}

// w2cBootEnv 组装 owner 引导基础 env（standalone + memory 驱动，无 J3b）。
func w2cBootEnv(t *testing.T, coverageDir string, boot *w2cBootRoot, extra ...string) []string {
	t.Helper()
	pairs := []string{
		"JUHE_AI_RUNTIME_MODE=standalone",
		"JUHE_AI_DATABASE_DRIVER=sqlite",
		"JUHE_AI_CACHE_DRIVER=memory",
		"JUHE_AI_RUNTIME_STATE_DRIVER=memory",
		"JUHE_AI_SECRET=w2c-boot-secret",
		"JUHE_AI_HOST=127.0.0.1",
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=" + boot.healthAddress,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=true",
		"JUHE_AI_DATABASE_PATH=" + boot.businessPath,
		"JUHE_AI_CHAT_DATABASE_PATH=" + filepath.Join(boot.root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH=" + filepath.Join(boot.root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH=" + filepath.Join(boot.root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH=" + filepath.Join(boot.root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH=" + filepath.Join(boot.root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH=" + filepath.Join(boot.root, "runtime-log.sqlite"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=" + filepath.Join(boot.root, "codex-shards"),
		"JUHE_AI_USAGE_SHARD_ROOT=" + filepath.Join(boot.root, "usage-shards"),
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH=" + boot.businessPath,
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=epoch-w2c",
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH=" + boot.businessEvidence,
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH=" + boot.auditPath,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(boot.root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(boot.root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH=" + filepath.Join(boot.root, "business-settings.sqlite"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w2c-boot",
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH=" + boot.operationPath,
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH=" + filepath.Join(boot.root, "business-settings.sqlite"),
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w2c-boot",
		"JUHE_AI_USAGE_SPOOL_DIRECTORY=" + filepath.Join(boot.root, "usage-spool"),
		"JUHE_AI_LOG_DIR=" + filepath.Join(boot.root, "logs"),
	}
	return w1bScenarioEnv(t, coverageDir, append(pairs, extra...)...)
}

// w2cSabotageLeaseInsert 在已建好 schema 的 F3/F4 SQLite 租约表上挂一个
// BEFORE INSERT 触发器：EnsureSchema 幂等（触发器保留），下一次租约 INSERT
// 必然报错——把 main 的 lease keeperErr 臂从“需要并发持锁”变成确定性触发。
func w2cSabotageLeaseInsert(t *testing.T, path, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open %s: %v", table, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER w2c_deny_%s_lease BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'w2c lease insert denied'); END`, table, table)); err != nil {
		t.Fatalf("create trigger on %s: %v", table, err)
	}
}

// w2cPrepareAuditStore / w2cPrepareOperationStore 预建 F3/F4 SQLite schema
// （boot 时的 EnsureSchema 幂等无副作用）。
func w2cPrepareAuditStore(t *testing.T, path, root string) {
	t.Helper()
	config := auditlog.Config{
		Mode:                 auditlog.ModeSQLite,
		InstanceID:           "w2c-boot",
		AuditDatabasePath:    path,
		PayloadBlobDirectory: filepath.Join(root, "audit-blobs"),
		HotSearchDirectory:   filepath.Join(root, "audit-hot"),
	}
	store, err := auditlog.OpenStore(config)
	if err != nil {
		t.Fatalf("open audit store: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("ensure audit schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close audit store: %v", err)
	}
}

func w2cPrepareOperationStore(t *testing.T, path, root string) {
	t.Helper()
	config := operationlog.Config{
		Enabled:              true,
		Mode:                 operationlog.ModeSQLite,
		InstanceID:           "w2c-boot",
		DatabasePath:         path,
		BusinessSettingsPath: filepath.Join(root, "business-settings.sqlite"),
		OwnerLease:           30 * time.Second,
	}
	store, err := operationlog.OpenStore(config)
	if err != nil {
		t.Fatalf("open operation store: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("ensure operation schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close operation store: %v", err)
	}
}

func TestW2CMainBootFailFastArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过插桩二进制场景")
	}
	w1bBuildCoverBinary(t)

	t.Run("F3审计租约keeperErr388", func(t *testing.T) {
		boot := w2cNewBootRoot(t, "epoch-w2c")
		w2cPrepareAuditStore(t, boot.auditPath, boot.root)
		w2cSabotageLeaseInsert(t, boot.auditPath, "audit_log_owner_leases")
		coverageDir := w1bCoverageDir(t, "W2C-f3-lease-err")
		env := w2cBootEnv(t, coverageDir, boot)
		_, stderr, code := w1bRunScenario(t, "W2C-f3-lease-err", env)
		w1bRequireExitCode(t, "W2C-f3-lease-err", code, 1)
		if !strings.Contains(stderr, "acquire F3 audit owner lease") {
			t.Fatalf("stderr 缺少 F3 租约失败文案: %s", stderr)
		}
	})

	t.Run("F4共享租约keeperErr442", func(t *testing.T) {
		boot := w2cNewBootRoot(t, "epoch-w2c")
		w2cPrepareOperationStore(t, boot.operationPath, boot.root)
		w2cSabotageLeaseInsert(t, boot.operationPath, "operation_log_owner_leases")
		coverageDir := w1bCoverageDir(t, "W2C-f4-lease-err")
		env := w2cBootEnv(t, coverageDir, boot)
		_, stderr, code := w1bRunScenario(t, "W2C-f4-lease-err", env)
		w1bRequireExitCode(t, "W2C-f4-lease-err", code, 1)
		if !strings.Contains(stderr, "acquire F4 operation-log owner lease") {
			t.Fatalf("stderr 缺少 F4 租约失败文案: %s", stderr)
		}
	})

}

// TestW2CMainPrivateF4LeaseErrorRetries covers main.go 514（F4 组件私有租约
// keeperErr）：SystemAPIEnabled=false 时组件自带租约；触发器让
// StartLeaseKeeper 首次执行即报错（514-516 行被实际执行），supervisor 记录
// 失败并重试；观察到失败日志后 CTRL_BREAK 优雅退出。
func TestW2CMainPrivateF4LeaseErrorRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过插桩二进制场景")
	}
	exe := w1bBuildCoverBinary(t)
	boot := w2cNewBootRoot(t, "epoch-w2c")
	w2cPrepareOperationStore(t, boot.operationPath, boot.root)
	w2cSabotageLeaseInsert(t, boot.operationPath, "operation_log_owner_leases")

	coverageDir := w1bCoverageDir(t, "W2C-f4-private-lease")
	// 复用基础 env，但把 SYSTEM_API_ENABLED 关掉（其余 F4 配置不变），使
	// F4 组件走私有租约分支（main 513-521）。
	env := w2cBootEnv(t, coverageDir, boot, "JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = env
	w1bSetNewProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("启动 F4 私有租约场景失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// 等待 F4 组件的租约失败进入 supervisor 重试日志（首次 Run 即命中 514）。
	sawFailure := false
	for !sawFailure {
		select {
		case waitErr := <-done:
			cancel()
			t.Fatalf("F4 私有租约场景提前退出: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
		case <-time.After(300 * time.Millisecond):
		}
		if strings.Contains(stdout.String(), `"component":"F4 operation-log-owner"`) && strings.Contains(stdout.String(), "acquire F4 operation-log owner lease") {
			sawFailure = true
		}
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			t.Fatalf("等待 F4 组件失败日志超时\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
		}
	}
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("CTRL_BREAK 失败: %v", err)
	}
	select {
	case waitErr := <-done:
		cancel()
		exitCode := 0
		if waitErr != nil {
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("等待退出失败: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 0 {
			t.Fatalf("期望优雅退出 exit 0，实际 %d\nstdout=%s\nstderr=%s", exitCode, stdout.String(), stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("CTRL_BREAK 后 30s 未退出\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	w1bAppendCoverageManifest(t, coverageDir)
}
