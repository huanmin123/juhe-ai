// 一次性修正：ProviderListItem 增加 PeakWindows 字段（轻量列表 DTO 与完整
// definitions 同源透出供应商高峰定义）。
import { readFileSync, writeFileSync } from 'node:fs'

const p = 'internal/providers/store.go'
let s = readFileSync(p, 'utf8')
const before = `	AccountTypes            []string \`json:"accountTypes"\`
	Capabilities            []string \`json:"capabilities"\`
}

// ProviderListItem mirrors the catalogue list row of GET /providers/list`
const after = `	AccountTypes            []string \`json:"accountTypes"\`
	Capabilities            []string \`json:"capabilities"\`
	PeakWindows             *ProviderPeakWindows \`json:"peakWindows,omitempty"\`
}

// ProviderListItem mirrors the catalogue list row of GET /providers/list`
if (!s.includes(before)) {
  console.log('pattern-miss')
  process.exit(1)
}
s = s.split(before).join(after)
writeFileSync(p, s)
console.log('fixed')
