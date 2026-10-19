import { defineComponent, h, type VNode } from 'vue'

import { ACCOUNT_MODEL_OPTIONS_LIMIT } from './useAccountProviderModelOptions'

/**
 * 供应商模型目录下拉的截断提示：用于 `a-select` 的 `#dropdownRender` 插槽，
 * 回填原候选菜单并在面板底部追加一行次要说明；不向候选列表注入任何选项，
 * 不影响键盘选择。文案中的窗口值与取数上限（ACCOUNT_MODEL_OPTIONS_LIMIT）同源。
 */
export const AccountModelOptionsTruncatedHint = defineComponent({
  name: 'AccountModelOptionsTruncatedHint',
  props: {
    menuNode: { type: Object as () => VNode, default: undefined }
  },
  setup(props) {
    return () => [
      props.menuNode,
      h('div', {
        class: 'account-model-options-truncated-hint',
        style: {
          padding: '5px 12px',
          borderTop: '1px solid var(--juhe-border)',
          color: 'var(--juhe-faint)',
          fontSize: '12px'
        }
      }, `仅显示前 ${ACCOUNT_MODEL_OPTIONS_LIMIT} 条，输入关键词可搜索更多`)
    ]
  }
})
