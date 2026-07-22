import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

interface ContractCase {
  id: string
  boundary: string
  regressions: string[]
  guarantees: string[]
}

interface AuditWriterContractGolden {
  version: string
  referenceOwner: string
  implementationStatus: string
  cases: ContractCase[]
}

const fixturePath = fileURLToPath(new URL('./fixtures/audit-writer-contract-v1.json', import.meta.url))
const packagePath = fileURLToPath(new URL('../../../package.json', import.meta.url))
const requiredCases = [
  'capture-finalization-and-overflow',
  'local-queue-backpressure',
  'transport-capacity-fallback',
  'transport-summary-budget',
  'writer-idempotence-and-error-groups',
  'payload-blob-refcount-and-window-read',
  'retention-and-unreferenced-blob-cleanup'
] as const

const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as AuditWriterContractGolden

assert.equal(fixture.version, 'audit-writer-contract/v1', '审计 writer golden 版本必须显式固定')
assert.equal(fixture.referenceOwner, 'node', '当前审计 writer 行为只以 Node 为参考 owner')
assert.equal(fixture.implementationStatus, 'golden_only_no_go_writer', '本切片只能冻结契约，不能实现 Go writer')
assert.deepEqual(fixture.cases.map((item) => item.id), requiredCases, '审计 writer golden 用例顺序和覆盖范围不得漂移')

for (const item of fixture.cases) {
  assert(item.boundary.trim().length > 0, `${item.id} 必须说明边界`)
  assert(item.guarantees.length > 0, `${item.id} 必须至少冻结一个可迁移保证`)
  assert(item.regressions.length > 0, `${item.id} 必须绑定至少一个 Node 回归脚本`)
  for (const regression of item.regressions) {
    const regressionPath = fileURLToPath(new URL(`./${regression}`, import.meta.url))
    assert(existsSync(regressionPath), `${item.id} 绑定的 Node 回归脚本不存在：${regression}`)
  }
}

const packageJson = JSON.parse(readFileSync(packagePath, 'utf8')) as { scripts?: Record<string, string> }
const command = packageJson.scripts?.['test:audit-writer-contract-golden'] ?? ''
for (const regression of new Set(fixture.cases.flatMap((item) => item.regressions))) {
  assert(command.includes(regression), `golden 命令必须执行 Node 参考回归：${regression}`)
}

console.log(`审计 writer golden 结构校验通过：${fixture.cases.length} 个 Node 参考用例已冻结`)
