<template>
  <div v-if="shouldRenderToolbar" class="responsive-list-toolbar">
    <div v-if="shouldRenderMain" class="responsive-list-toolbar-main" :class="{ 'without-search': !showSearch }">
      <a-input
        v-if="showSearch"
        :value="keyword"
        allow-clear
        :placeholder="searchPlaceholder"
        class="responsive-list-search"
        @update:value="emitKeywordUpdate"
        @press-enter="emit('search')"
      >
        <template #prefix>
          <SearchOutlined class="responsive-list-search-icon" />
        </template>
      </a-input>
      <a-badge v-if="shouldShowFilterButton" class="responsive-list-filter-badge" :count="activeFilterCount" :offset="[-2, 2]">
        <a-button class="responsive-list-filter-button" @click="filtersOpen = true">
          <FilterOutlined />
          筛选
        </a-button>
      </a-badge>
      <slot name="inline-filters" />
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

  <a-drawer v-if="showFilters && filterNodeCount > 0" v-model:open="filtersOpen" :title="filterTitle" placement="bottom" height="min(78vh, 520px)" class="responsive-list-filter-drawer" :body-style="{ padding: '14px 16px 16px' }">
    <div class="responsive-list-filter-body">
      <slot name="filters" />
    </div>
    <div v-if="showReset" class="responsive-list-filter-actions">
      <a-button v-if="showReset" block @click="handleDrawerReset">重置</a-button>
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
  filterTitle?: string
  activeFilterCount?: number
  mobileActionCount?: number
  mobileBreakpoint?: number
  showSearch?: boolean
  showReset?: boolean
  showRefresh?: boolean
  showFilters?: boolean
  refreshLoading?: boolean
}>(), {
  keyword: '',
  searchPlaceholder: '搜索...',
  filterTitle: '筛选',
  activeFilterCount: 0,
  mobileBreakpoint: 900,
  showSearch: true,
  showReset: true,
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
const hasActions = computed(() => mobileVisibleActionCount.value > 0)
const hasInlineFilters = computed(() => inlineFilterNodeCount.value > 0)
const shouldShowFilterButton = computed(() => props.showFilters && filterNodeCount.value > 0)
const shouldShowTopRefresh = computed(() => props.showRefresh && !isMobile.value)
const shouldShowTopReset = computed(() => props.showReset && !isMobile.value)
const shouldRenderMain = computed(() => props.showSearch || shouldShowTopRefresh.value || shouldShowTopReset.value || shouldShowFilterButton.value || hasInlineFilters.value)
const shouldRenderToolbar = computed(() => shouldRenderMain.value || hasActions.value)
const shouldCollapseActions = computed(() => isMobile.value && mobileVisibleActionCount.value > 2)
const shouldShowInlineActions = computed(() => !shouldCollapseActions.value)

function emitKeywordUpdate(value: string) {
  emit('update:keyword', value)
}

function handleDrawerReset() {
  emit('reset')
}

function updateViewportState() {
  if (typeof window === 'undefined') return
  isMobile.value = window.innerWidth <= props.mobileBreakpoint
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
  width: min(260px, 100%);
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

.responsive-list-filter-badge {
  display: none;
}

.responsive-list-filter-body {
  display: grid;
  gap: 14px;
}

.responsive-list-filter-actions {
  margin-top: 18px;
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
    grid-template-columns: 1fr;
  }

  .responsive-list-search {
    width: 100%;
  }

  .responsive-list-reset,
  .responsive-list-refresh,
  .responsive-list-filter-button {
    width: 100%;
  }

  .responsive-list-filter-badge {
    display: block;
  }

  .responsive-list-filter-badge :deep(.ant-badge) {
    width: 100%;
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
    width: 100%;
  }
}
</style>
