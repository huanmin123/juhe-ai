import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = resolve(import.meta.dirname, '../..')
const runPanel = readFileSync(resolve(root, 'src/views/model-checks/ModelCheckRunPanel.vue'), 'utf8')
const view = readFileSync(resolve(root, 'src/views/model-checks/ModelChecksView.vue'), 'utf8')
const detail = readFileSync(resolve(root, 'src/views/model-checks/ModelCheckRunDetailDrawer.vue'), 'utf8')
const types = readFileSync(resolve(root, 'src/types/domain/model-checks.ts'), 'utf8')

assert(!runPanel.includes('极限长上下文'), '模型检测面板不得继续暴露高成本极限长上下文开关')
assert(!runPanel.includes('includeExtremeContext'))
assert(!view.includes('includeExtremeContext'))
assert(!view.includes('--extreme-context'))
assert(!types.includes('includeExtremeContext'))
assert(types.includes("interceptBaselineStatus?: 'unavailable' | 'calibration_pending' | 'active'"))
assert(detail.includes('固定开销基线'))
assert(detail.includes('待真实样本校准'))
assert(detail.includes("fixed_intercept_calibration_pending"))
assert(detail.includes("interceptStrongGateEnabled ? '已开启' : '已关闭'"))

console.log('模型可信前端契约回归通过：极限长上下文入口已移除，截距校准状态展示符合预期')
