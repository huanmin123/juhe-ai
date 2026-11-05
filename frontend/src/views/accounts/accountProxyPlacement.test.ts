import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

// 对话域「添加/编辑 AI 账户」弹窗代理字段归位契约（源码级断言，accountMediaFormBlocks.test.ts 同惯例）：
// 代理字段从第 3 步「策略与模型」高级折叠卡（原「策略与代理」）迁移到凭证区末尾
// （AccountGroupField 之前），创建向导第 1 步与编辑态常用配置区共用 AccountProxyField
// 同一处渲染；策略卡摘要/计数不再包含代理。

const accountsDir = path.resolve(process.cwd(), 'src/views/accounts')
const modalSource = readFileSync(path.resolve(accountsDir, 'AccountEditModal.vue'), 'utf8')
const strategySource = readFileSync(path.resolve(accountsDir, 'AccountStrategySection.vue'), 'utf8')
const proxyFieldSource = readFileSync(path.resolve(accountsDir, 'AccountProxyField.vue'), 'utf8')

describe('AccountEditModal.vue 代理字段归位（源码级）', () => {
  const proxyFieldCount = modalSource.match(/<AccountProxyField/g)?.length ?? 0
  const credentialIndex = modalSource.indexOf('id="acct-sec-credential"')
  const proxyFieldIndex = modalSource.indexOf('<AccountProxyField')
  const groupFieldIndex = modalSource.indexOf('<AccountGroupField')
  // 凭证 section 的结束边界：下一个分区锚点 acct-sec-basic 之前。
  const basicSectionIndex = modalSource.indexOf('id="acct-sec-basic"')

  it('模板中 <AccountProxyField> 只出现 1 次（两形态共用同一处渲染）', () => {
    expect(proxyFieldCount, '代理字段必须只渲染一次').toBe(1)
  })

  it('代理字段位于凭证 section 内、该 section 的 AccountGroupField 之前', () => {
    expect(credentialIndex).toBeGreaterThan(-1)
    expect(proxyFieldIndex, '代理字段必须位于 id="acct-sec-credential" 之后').toBeGreaterThan(credentialIndex)
    expect(proxyFieldIndex, '代理字段必须仍属于凭证 section（在 acct-sec-basic 之前）').toBeLessThan(basicSectionIndex)
    expect(groupFieldIndex, '凭证 section 内缺少 AccountGroupField').toBeGreaterThan(proxyFieldIndex)
  })

  it('高级折叠卡更名为「策略与模型」，不再承载代理摘要文本', () => {
    expect(modalSource).toContain('name="策略与模型"')
    expect(modalSource).not.toContain('name="策略与代理"')
    expect(modalSource).not.toContain('代理已绑定')
  })
})

describe('AccountStrategySection.vue 代理字段移除（源码级）', () => {
  it('不再包含 ProxySelect / proxyProfileId / strategy-proxy-field', () => {
    expect(strategySource).not.toContain('ProxySelect')
    expect(strategySource).not.toContain('proxyProfileId')
    expect(strategySource).not.toContain('strategy-proxy-field')
  })
})

describe('AccountProxyField.vue 共用组件契约（源码级）', () => {
  it('保留 OAuth 四家必选代理判断与 ProxySelect 渲染', () => {
    expect(proxyFieldSource).toContain("['gpt', 'anthropic', 'gemini', 'xai']")
    expect(proxyFieldSource).toContain('<ProxySelect')
  })
})
