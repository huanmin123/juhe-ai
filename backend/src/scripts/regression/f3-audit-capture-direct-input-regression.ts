import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const projectRoot = resolve(import.meta.dirname, '../../..')
const targets = [
  'src/modules/gateway/audit/capture.service.ts',
  'src/modules/gateway/request/body-middleware.ts',
  'src/modules/gateway/request/pre-auth.ts'
]

const forbidden = /(?:enqueueAuditLog|recordDroppedAuditCapture)/
const dispatch = /dispatchAuditLogToGo/

for (const relativePath of targets) {
  const path = resolve(projectRoot, relativePath)
  const source = await readFile(path, 'utf8')
  if (forbidden.test(source)) {
    throw new Error(`F3 capture direct-input regression failed: legacy queue symbol remains in ${relativePath}`)
  }
  if (!dispatch.test(source)) {
    throw new Error(`F3 capture direct-input regression failed: Go input client is not referenced in ${relativePath}`)
  }
}

console.log('F3 audit capture direct-input regression passed')
