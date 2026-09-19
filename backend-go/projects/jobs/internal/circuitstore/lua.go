package circuitstore

import "github.com/huanminabc/juhe-ai/backend-go-platform/circuitstate"

// REFACTOR-0008 跨模块成对收敛：7 个账户熔断 Lua 脚本下潜到
// shared/platform/circuitstate，与 gateway/internal/gatewaycircuit 共享单一
// 事实，消除两份手工同步副本漂移导致共享熔断状态语义分叉的风险。脚本正文
// 与 Node 对齐头注随迁至平台包，逐字节未动；本包保留原名 const 别名，
// 调用点零改动。原头注要点保留：脚本是共享运行态的权威转移语义
// （Node account-circuit-redis-store.ts 原文），Go store 只构建相同 JSON
// payload 并解析相同 cjson 响应；与 Node 网关 / Go 网关读写同一批键，
// store 名保持 Node 默认 gateway-account-circuit。
const (
	redisAccountCircuitTransitionScript      = circuitstate.ScriptTransition
	redisAccountCircuitSizeScript            = circuitstate.ScriptSize
	redisAccountCircuitRestoreScript         = circuitstate.ScriptRestore
	redisAccountCircuitListDueScript         = circuitstate.ScriptListDue
	redisAccountCircuitEscalationScript      = circuitstate.ScriptEscalation
	redisAccountCircuitClearEscalationScript = circuitstate.ScriptClearEscalation
	redisAccountCircuitAccountRevisionScript = circuitstate.ScriptAccountRevision
)
