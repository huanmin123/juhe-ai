package gatewaycircuit

import "github.com/huanminabc/juhe-ai/backend-go-platform/circuitstate"

// REFACTOR-0008 跨模块成对收敛：7 个账户熔断 Lua 脚本下潜到
// shared/platform/circuitstate，与 jobs/internal/circuitstore 共享单一事实，
// 消除两份手工同步副本漂移导致共享熔断状态语义分叉的风险。脚本正文与
// Node 对齐头注随迁至平台包，逐字节未动；本包保留原名 const 别名，
// 调用点零改动。
const (
	redisAccountCircuitTransitionScript      = circuitstate.ScriptTransition
	redisAccountCircuitSizeScript            = circuitstate.ScriptSize
	redisAccountCircuitRestoreScript         = circuitstate.ScriptRestore
	redisAccountCircuitListDueScript         = circuitstate.ScriptListDue
	redisAccountCircuitEscalationScript      = circuitstate.ScriptEscalation
	redisAccountCircuitClearEscalationScript = circuitstate.ScriptClearEscalation
	redisAccountCircuitAccountRevisionScript = circuitstate.ScriptAccountRevision
)
