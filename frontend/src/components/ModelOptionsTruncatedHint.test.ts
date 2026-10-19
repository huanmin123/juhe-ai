import { describe, expect, it } from 'vitest'
import { createApp, h } from 'vue'

import { ACCOUNT_MODEL_OPTIONS_LIMIT } from '@/views/accounts/useAccountProviderModelOptions'
import { ModelOptionsTruncatedHint } from './ModelOptionsTruncatedHint'

// T23：供应商模型目录下拉的截断提示渲染断言（happy-dom 覆盖范围）——提示行
// 追加在回填的原候选菜单之后、以次要说明形态渲染，不向候选列表注入选项。

describe('ModelOptionsTruncatedHint 渲染', () => {
  it('在候选菜单之后追加底部提示行，不改动候选列表结构', () => {
    const host = document.createElement('div')
    const app = createApp({
      render() {
        return h(ModelOptionsTruncatedHint, {
          menuNode: h('ul', { class: 'rc-virtual-list' }, [h('li', 'model-a')])
        })
      }
    })
    app.mount(host)
    const menu = host.querySelector('ul.rc-virtual-list')
    expect(menu?.tagName).toBe('UL')
    expect(menu?.textContent).toBe('model-a')
    const hint = host.querySelector('.model-options-truncated-hint')
    expect(hint).not.toBeNull()
    expect(hint?.textContent).toBe(`仅显示前 ${ACCOUNT_MODEL_OPTIONS_LIMIT} 条，输入关键词可搜索更多`)
    // 提示行是候选菜单之后的兄弟节点（面板底部），不是列表项。
    expect(menu?.nextElementSibling).toBe(hint)
    expect(hint?.querySelector('li')).toBeNull()
    app.unmount()
  })

  it('窗口文案与取数上限常量同源且为 50', () => {
    expect(ACCOUNT_MODEL_OPTIONS_LIMIT).toBe(50)
  })
})
