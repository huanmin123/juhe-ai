<template>
  <div v-if="shouldRenderToolbar" class="responsive-list-toolbar" :style="toolbarStyle">
    <div v-if="shouldRenderMain" class="responsive-list-toolbar-main" :class="{ 'without-search': !showSearch }">
      <a-input
        v-if="showSearch"
        :value="keyword"
        allow-clear
        :disabled="searchDisabled"
        :placeholder="searchPlaceholder"
        class="responsive-list-search"
        @update:value="emitKeywordUpdate"
        @press-enter="emit('search')"
      >
        <template #prefix>
          <SearchOutlined class="responsive-list-search-icon" />
        </template>
      </a-input>
      <slot name="inline-filters" />
      <a-badge v-if="shouldShowFilterButton" class="responsive-list-filter-badge" :count="filterButtonBadgeCount" :offset="[-2, 2]">
        <a-button class="responsive-list-filter-button" @click="filtersOpen = true">
          <FilterOutlined />
          {{ filterButtonText }}
        </a-button>
      </a-badge>
      <a-button v-if="shouldShowTopRefresh" class="responsive-list-refresh" :loading="refreshLoading" @click="emit('refresh')">
        <template #icon>
          <ReloadOutlined />
        </template>
        刷新
      </a-button>
      <a-button v-if="shouldShowTopReset" class="responsive-list-reset" @click="emit('reset')">重置</a-button>
    </div>
    <div v-if="hasActions && shouldShowInlineActions" class="responsive-list-toolbar-actions" :class="{ 'mobile-inline': isMobile, single: isMobile && mobileVisibleActionCount === 1 }">
      <slot name="actions" />
    </div>

    <div v-else-if="hasActions" class="responsive-list-toolbar-actions mobile-drawer-trigger">
      <a-button class="responsive-list-actions-button" @click="actionsOpen = true">更多操作</a-button>
    </div>
  </div>

  <a-drawer
    v-if="shouldRenderFilterDrawer"
    v-model:open="filtersOpen"
    :title="filterDrawerTitle"
    :placement="filterDrawerPlacement"
    :height="filterDrawerHeight"
    :width="filterDrawerWidth"
    class="responsive-list-filter-drawer"
    :body-style="{ padding: '14px 16px 16px' }"
  >
    <div class="responsive-list-filter-body">
      <template v-if="isMobile">
        <slot v-if="filterNodeCount > 0" name="filters" />
        <slot v-else name="advanced-filters" />
      </template>
      <slot v-else name="advanced-filters" />
    </div>
    <div v-if="showReset || showFilterSearch" class="responsive-list-filter-actions" :class="{ single: !showReset || !showFilterSearch }">
      <a-button v-if="showReset" @click="handleDrawerReset">重置</a-button>
      <a-button v-if="showFilterSearch" type="primary" :loading="refreshLoading" @click="handleDrawerRefresh">
        <template #icon>
          <ReloadOutlined />
        </template>
        刷新
      </a-button>
    </div>
  </a-drawer>

  <a-drawer v-if="hasActions && shouldCollapseActions" v-model:open="actionsOpen" title="更多操作" placement="bottom" height="min(62vh, 420px)" class="responsive-list-actions-drawer" :body-style="{ padding: '14px 16px 16px' }">
    <div class="responsive-list-actions-drawer-body">
      <slot name="actions" />
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { FilterOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { Comment, Fragment, Text, computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, useSlots, type VNode } from 'vue'

const props = withDefaults(defineProps<{
  keyword?: string
  searchPlaceholder?: string
  searchDisabled?: boolean
  searchWidth?: string
  filterTitle?: string
  advancedFilterTitle?: string
  activeFilterCount?: number
  advancedFilterCount?: number
  mobileActionCount?: number
  mobileBreakpoint?: number
  showSearch?: boolean
  showReset?: boolean
  showFilterSearch?: boolean
  showRefresh?: boolean
  showFilters?: boolean
  refreshLoading?: boolean
}>(), {
  keyword: '',
  searchPlaceholder: '搜索...',
  searchDisabled: false,
  filterTitle: '筛选',
  activeFilterCount: 0,
  advancedFilterCount: 0,
  mobileBreakpoint: 900,
  showSearch: true,
  showReset: true,
  showFilterSearch: true,
  showRefresh: true,
  showFilters: true,
  refreshLoading: false
})

const emit = defineEmits<{
  (event: 'update:keyword', value: string): void
  (event: 'search'): void
  (event: 'reset'): void
  (event: 'refresh'): void
}>()

const filtersOpen = ref(false)
const actionsOpen = ref(false)
const isMobile = ref(initialMobileState())
const slots = useSlots()
let resizeListenerAttached = false

const mobileVisibleActionCount = computed(() => {
  if (typeof props.mobileActionCount === 'number') return props.mobileActionCount
  return countActionNodes(slots.actions?.() ?? [])
})
const inlineFilterNodeCount = computed(() => countActionNodes(slots.inlineFilters?.() ?? []))
const filterNodeCount = computed(() => countActionNodes(slots.filters?.() ?? []))
const advancedFilterNodeCount = computed(() => countActionNodes(slots['advanced-filters']?.() ?? []))
const mobileFilterNodeCount = computed(() => filterNodeCount.value || advancedFilterNodeCount.value)
const hasActions = computed(() => mobileVisibleActionCount.value > 0)
const hasInlineFilters = computed(() => inlineFilterNodeCount.value > 0)
const shouldShowFilterButton = computed(() => props.showFilters && (isMobile.value ? mobileFilterNodeCount.value > 0 : advancedFilterNodeCount.value > 0))
const shouldShowTopRefresh = computed(() => props.showRefresh && !isMobile.value)
const shouldShowTopReset = computed(() => props.showReset && !isMobile.value)
const shouldRenderMain = computed(() => props.showSearch || shouldShowTopRefresh.value || shouldShowTopReset.value || shouldShowFilterButton.value || hasInlineFilters.value)
const shouldRenderToolbar = computed(() => shouldRenderMain.value || hasActions.value)
const shouldCollapseActions = computed(() => isMobile.value && mobileVisibleActionCount.value > 2)
const shouldShowInlineActions = computed(() => !shouldCollapseActions.value)
const shouldRenderFilterDrawer = computed(() => props.showFilters && mobileFilterNodeCount.value > 0)
const toolbarStyle = computed(() => props.searchWidth ? { '--responsive-list-search-width': props.searchWidth } : undefined)
const filterButtonText = computed(() => (isMobile.value ? '筛选' : '更多'))
const filterButtonBadgeCount = computed(() => (isMobile.value ? props.activeFilterCount : props.advancedFilterCount))
const filterDrawerTitle = computed(() => (isMobile.value ? props.filterTitle : props.advancedFilterTitle || props.filterTitle))
const filterDrawerPlacement = computed<'bottom' | 'right'>(() => (isMobile.value ? 'bottom' : 'right'))
const filterDrawerHeight = computed(() => (isMobile.value ? 'min(78vh, 520px)' : undefined))
const filterDrawerWidth = computed(() => (isMobile.value ? undefined : 'min(420px, 92vw)'))

function emitKeywordUpdate(value: string) {
  emit('update:keyword', value)
}

function handleDrawerReset() {
  emit('reset')
}

function handleDrawerRefresh() {
  emit('refresh')
}

function updateViewportState() {
  if (typeof window === 'undefined') return
  isMobile.value = window.innerWidth <= props.mobileBreakpoint
  if (!shouldShowFilterButton.value) {
    filtersOpen.value = false
  }
  if (!shouldCollapseActions.value) {
    actionsOpen.value = false
  }
}

function addResizeListener() {
  if (resizeListenerAttached || typeof window === 'undefined') return
  resizeListenerAttached = true
  window.addEventListener('resize', updateViewportState, { passive: true })
}

function removeResizeListener() {
  if (!resizeListenerAttached || typeof window === 'undefined') return
  resizeListenerAttached = false
  window.removeEventListener('resize', updateViewportState)
}

function initialMobileState() {
  return typeof window !== 'undefined' && window.innerWidth <= props.mobileBreakpoint
}

function countActionNodes(nodes: VNode[]): number {
  return nodes.reduce((total, node) => {
    if (node.type === Comment) return total
    if (node.type === Text && typeof node.children === 'string' && !node.children.trim()) return total
    if (node.type === Fragment && Array.isArray(node.children)) {
      return total + countActionNodes(node.children as VNode[])
    }
    return total + 1
  }, 0)
}

onMounted(() => {
  updateViewportState()
  addResizeListener()
})

onActivated(() => {
  updateViewportState()
  addResizeListener()
})

onDeactivated(() => {
  filtersOpen.value = false
  actionsOpen.value = false
  removeResizeListener()
})

onBeforeUnmount(() => {
  removeResizeListener()
})
</script>

<style scoped>
.responsive-list-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  flex: 0 0 auto;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}

.responsive-list-toolbar-main {
  display: flex;
  flex: 1 1 520px;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}

.responsive-list-toolbar-main.without-search {
  flex: 1 1 260px;
}

.responsive-list-search {
  width: min(var(--responsive-list-search-width, 220px), 100%);
}

.responsive-list-search-icon {
  color: #94a3b8;
}

.responsive-list-toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
}

.responsive-list-filter-body {
  display: grid;
  gap: 14px;
}

.responsive-list-filter-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 18px;
}

.responsive-list-filter-button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.responsive-list-actions-drawer-body {
  display: grid;
  gap: 10px;
}

@media (max-width: 900px) {
  .responsive-list-toolbar {
    flex-direction: column;
    gap: 10px;
    margin-bottom: 12px;
  }

  .responsive-list-toolbar-main {
    display: grid;
    width: 100%;
    flex: none;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 10px;
  }

  .responsive-list-toolbar-main.without-search {
    flex: none;
    grid-template-columns: minmax(0, 1fr);
  }

  .responsive-list-search {
    width: 100%;
  }

  .responsive-list-reset,
  .responsive-list-refresh,
  .responsive-list-filter-button {
    min-height: 38px;
    width: 100%;
    white-space: normal;
  }

  .responsive-list-filter-badge {
    display: block;
  }

  .responsive-list-toolbar-main.without-search .responsive-list-filter-badge {
    width: 100%;
    min-width: 0;
  }

  .responsive-list-toolbar-main.without-search .responsive-list-filter-button {
    justify-content: center;
  }

  .responsive-list-filter-badge :deep(.ant-btn) {
    width: 100%;
  }

  .responsive-list-filter-actions {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .responsive-list-filter-actions.single {
    grid-template-columns: 1fr;
  }

  .responsive-list-filter-actions :deep(.ant-btn) {
    min-height: 38px;
    width: 100%;
    white-space: normal;
  }

  .responsive-list-toolbar-main :deep(.toolbar-select),
  .responsive-list-toolbar-main :deep(.responsive-list-inline-filter) {
    display: none;
  }

  .responsive-list-toolbar-actions {
    justify-content: flex-start;
    width: 100%;
  }

  .responsive-list-toolbar-actions.mobile-inline {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .responsive-list-toolbar-actions.mobile-inline.single {
    grid-template-columns: 1fr;
  }

  .responsive-list-toolbar-actions.mobile-inline :deep(.ant-btn),
  .responsive-list-toolbar-actions.mobile-inline :deep(.ant-dropdown-trigger),
  .responsive-list-actions-button,
  .responsive-list-actions-drawer-body :deep(.ant-btn),
  .responsive-list-actions-drawer-body :deep(.ant-dropdown-trigger) {
    min-height: 38px;
    width: 100%;
    white-space: normal;
  }
}
</style>
