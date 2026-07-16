import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const root = resolve(import.meta.dirname, '../..')
const runPanel = readFileSync(resolve(root, 'src/views/model-checks/ModelCheckRunPanel.vue'), 'utf8')
const view = readFileSync(resolve(root, 'src/views/model-checks/ModelChecksView.vue'), 'utf8')
const detail = readFileSync(resolve(root, 'src/views/model-checks/ModelCheckRunDetailDrawer.vue'), 'utf8')
const types = readFileSync(resolve(root, 'src/types/domain/model-checks.ts'), 'utf8')

assert(runPanel.includes('label="极限长上下文"'))
assert(runPanel.includes("emit('update:includeExtremeContext', $event)"))
assert(view.includes('includeExtremeContext: form.includeExtremeContext === true'))
assert(view.includes("form.includeExtremeContext = false"), '重置必须关闭高成本极限探针')
assert(types.includes('includeExtremeContext?: boolean'))
assert(types.includes("interceptBaselineStatus?: 'unavailable' | 'calibration_pending' | 'active'"))
assert(detail.includes('固定开销基线'))
assert(detail.includes('待真实样本校准'))
assert(detail.includes("fixed_intercept_calibration_pending"))
assert(detail.includes("interceptStrongGateEnabled ? '已开启' : '已关闭'"))

console.log('模型可信前端契约回归通过：极限探针显式开关、重置和截距校准状态展示符合预期')
