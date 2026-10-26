package main

// w2_assembly_tail_test.go —— 组合根/引导/聊天外围组残余未覆盖行收尾（w2c 前缀，
// TestW2C 入口）。只新增本文件，不修改任何现有文件。
//
// 驱动层次：
//  1. 进程内 mock 直驱：database/sql 自定义 driver 注入（chat keys 错误臂、
//     图片观察观察臂）、假端口（tool capabilities 决策矩阵）、纯几何函数
//     （图片管道）、畸形路径/只读破坏（storage preflight 与六库物理门禁）。
//  2. composeSystemAPI PG 组合根 + 种子破坏（限流设置坏值、runtime-log
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
//   - 【已随 sqlite 存储形态退役】chain_chat_mount.go 225-227：openChatDatabase
//     的 sql.Open("sqlite") 错误臂——原 modernc/sqlite 惰性建连守卫随生产
//     sqlite 臂删除（2026-10-24 存储形态收口），chat 连接恒 PG 池共享臂。
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
//   - 【已随 sqlite 存储形态退役】compose.go 283-285/324-326/352-354/367-369
//     与 405-407/440-442：sql.Open("sqlite") / openSQLiteReadOnly 惰性建连
//     守卫随生产 sqlite 臂删除（2026-10-24 存储形态收口），业务/聊天连接恒
//     PG 池，只读面走限定名查询。
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
//     装配下恒非 nil；openChatDatabase 恒共享已成功 Acquire 的 PG 池
//    （原 SQLite chat 路径臂随存储形态退役删除）。（w3 保留：调用点守卫。）
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
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
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

// w2cAccountLookup 解析会话绑定账户（矩阵用固定账户 w2c-acc，启用分组 grp）。
type w2cAccountLookup struct {
	ref *chat.ChatAccountRef
	err error
}

func (f w2cAccountLookup) FindChatAccount(chat.ChatBindScope, string) (*chat.ChatAccountRef, error) {
	return f.ref, f.err
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

// TestW2CChatToolCapabilitiesResolverMatrix 已随账户唯一绑定与工具体系改造退场：
// toolCapabilities 不再是 cmd 侧 resolver 的可用性矩阵，而是 chat 包
// routes.go toolCapabilities 的绑定状态聚合（契约 AI问答工具体系设计 §8），
// 断言由 internal/chat 的 tool-bindings 路由测试承载。

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
	return newChatImageObservations(db, objects, executor)
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
// chain_wiring_w2c.go 343：恢复等待 Refresh 在屏蔽过期后返回可继续状态
// ---------------------------------------------------------------------------

func TestW2CSuppressionRefreshRecoversAfterExpiry(t *testing.T) {
	store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})
	port := chainSuppressionPort{
		store:  store,
		waiter: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil),
	}
	accounts := []gatewaydispatch.AccountCandidate{{ID: "w2c-acc-a"}, {ID: "w2c-acc-b"}}
	// 两个候选都屏蔽 1000ms → 全屏蔽 → 进入恢复等待窗口；过期后的 Refresh
	// 命中 343 行（NextRetryAfterMs == nil → 继续派发）。窗口远大于调度抖动，
	// 避免全量负载下的墙钟边界偶发（350ms 时代出现过一次提前恢复）。
	for _, account := range accounts {
		store.Suppress(account.ID, 1000, "w2c test failure", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
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
// 行预写入 PG 测试库（种子行已由 bootstrap.SeedPostgres 预置，此处按坏值覆盖）。
func w2cComposeWithSetting(t *testing.T, key, valueJSON string) *composition {
	t.Helper()
	stack := w1oNewComposeStack(t)
	if key != "" {
		business, err := sql.Open("pgx", stack.cfg.PostgresURL)
		if err != nil {
			t.Fatalf("open business for seed: %v", err)
		}
		if _, err := business.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', $1, $2, '2026-09-14T00:00:00.000Z')
			ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json`, key, valueJSON); err != nil {
			_ = business.Close()
			t.Fatalf("seed setting %s: %v", key, err)
		}
		_ = business.Close()
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

// w2cComposeChainStack 组装一个开启链条、带真实上游的 PG 组合根，并按
// w1l 手法向业务库补 /v1 所需的分组/账户/路由/API Key 行（schema/种子行由
// pgtestkit fresh 库的 bootstrap EnsurePostgres/SeedPostgres 预置）。
func w2cComposeChainStack(t *testing.T, upstream *httptest.Server) *composition {
	t.Helper()
	stack := w1oNewComposeStack(t)
	stack.cfg.ChainEnabled = true
	stack.cfg.ChatMaxTurnsPerConversation = 50
	// httptest loopback 上游：与 chainSmokeDeps 同一约定（D-192/D-146 生产
	// 默认不受影响）。
	stack.cfg.UpstreamURLSecurity = sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}

	business, err := sql.Open("pgx", stack.cfg.PostgresURL)
	if err != nil {
		t.Fatalf("open business for chain seed: %v", err)
	}
	w2cSeedChainRows(t, business, upstream.URL)
	_ = business.Close()
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
		if _, err := db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO juhe_business.groups (id, system_account_id, name, enabled, group_type, created_at, updated_at)
		VALUES ('w2c_group', 'sys_admin', 'W2C 分组', 1, 'personal', $1, $2)`, now, now)
	sealed, err := accounts.EncryptJSON("compose-test-secret", map[string]any{
		"api_key":  "sk-w2c-upstream",
		"base_url": upstreamBaseURL,
	})
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	// 供应商解耦（设计 §2.1/§4）：身份列 vendor_code/connection_driver_code，
	// 支持模型走 models 单表 + model_id 外键。
	seed(`INSERT INTO juhe_business.accounts (
			id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, health_check_model, health_check_endpoint_mode,
			created_at, updated_at
		) VALUES ('w2c_acc', 'sys_admin', 'gpt', 'gpt', 'openai', 'v1',
			'W2C 账户', 'api_key', 'active', 1, $1, 'gpt-test', 'chat_json', $2, $3)`,
		sealed, now, now)
	seed(`INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('sys_admin', 'w2c_group', 'w2c_acc', 1, $1, $2)`, now, now)
	seed(`INSERT INTO juhe_business.models (id, vendor_code, public_model, upstream_model, scope, status, catalog_visible, supported_endpoint_modes_json, input_usd_per_1m, created_at, updated_at)
		VALUES ('mdl_w2c_sm_gpt-test', 'gpt', 'gpt-test', 'gpt-test', 'global', 'active', TRUE, '["chat_json","chat_sse"]', 1.0, $1, $2)`, now, now)
	seed(`INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
		VALUES ('w2c_acc', 'mdl_w2c_sm_gpt-test', $1)`, now)
	// B4 第一批单连接事实源：w2c_acc 需要 enabled 连接行（health 三字段经
	// 真实 DDL 复合外键互指，连接行先行、health_check_connection_id 回填）。
	seed(`INSERT INTO juhe_business.account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn_w2c_acc', 'w2c_acc', 'gpt_openai_v1', 'openai_v1', $1, 1, $2, $3)`, upstreamBaseURL, now, now)
	seed(`UPDATE juhe_business.accounts SET health_check_connection_id = 'conn_w2c_acc' WHERE id = 'w2c_acc'`)
	seed(`INSERT INTO juhe_business.route_strategies (id, system_account_id, name, mode, status, created_at, updated_at)
		VALUES ('w2c_rs', 'sys_admin', 'W2C 路由', 'normal', 'active', $1, $2)`, now, now)
	seed(`INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('w2c_rsg', 'w2c_rs', 'sys_admin', 'w2c_group', 1, 1, 'active', $1, $2)`, now, now)
	secret := "sk-w2c-gateway-key"
	secretEncrypted, err := accounts.EncryptJSON("compose-test-secret", map[string]any{"key": secret})
	if err != nil {
		t.Fatalf("encrypt key secret: %v", err)
	}
	seed(`INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, scheduling_preference_id, created_at, updated_at)
		VALUES ('w2c_key', 'sys_admin', 'w2c_rs', 'W2C Key', $1, $2, $3, $4, 'active', 'schpref-builtin-chat-cost-first', $5, $6)`,
		gatewayruntimecache.HashSecret(secret), secret[:8], secret[len(secret)-8:], secretEncrypted, now, now)
	// 供应商解耦（设计 §3）：目录读侧收敛 models 单表；gpt-test 目录行即
	// 支持模型行本身（带价格、可见），不再另种第二行——生产
	// idx_models_identity_unique 对 (vendor, public, upstream, scope) 身份
	// 唯一，双行必冲突。
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
		`INSERT INTO juhe_business.account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
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
	if err := composed.DB.QueryRow(`SELECT status FROM juhe_business.accounts WHERE id = 'w2c_acc'`).Scan(&status); err != nil {
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
