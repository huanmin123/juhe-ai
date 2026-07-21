<template>
  <a-modal
    :open="open"
    title="公告"
    width="720px"
    :footer="null"
    class="announcement-modal"
    @update:open="emit('update:open', $event)"
  >
    <template #closeIcon>
      <CloseOutlined />
    </template>

    <a-spin :spinning="loading">
      <a-empty v-if="!announcements.length" class="announcement-empty" description="暂无公告" />
      <a-timeline v-else class="announcement-timeline">
        <a-timeline-item v-for="item in announcements" :key="item.id" :color="timelineColor(item.level)">
          <article class="announcement-item">
            <header class="announcement-item-head">
              <div class="announcement-title-wrap">
                <a-tag :color="levelColor(item.level)">{{ levelText(item.level) }}</a-tag>
                <h3>{{ item.title }}</h3>
              </div>
              <time>{{ formatRelativeDateTime(item.publishedAt) }}</time>
            </header>
            <a-spin v-if="expandedIds.has(item.id) && loadingIds.has(item.id)" size="small" class="content-loading" />
            <p v-else-if="expandedIds.has(item.id) && contentById[item.id]" class="expanded">{{ contentById[item.id] }}</p>
            <a-button type="link" size="small" class="expand-button" :loading="loadingIds.has(item.id)" @click="toggleExpand(item.id)">
              {{ contentActionLabel(item.id) }}
            </a-button>
          </article>
        </a-timeline-item>
      </a-timeline>
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { CloseOutlined } from '@ant-design/icons-vue'
import { ref, watch } from 'vue'

import { formatRelativeDateTime } from '@/shared/formatters'
import type { PublishedAnnouncementListItem } from '@/types/domain'
import {
  announcementLevelColor as levelColor,
  announcementLevelText as levelText,
  announcementTimelineColor as timelineColor
} from '@/views/announcements/announcementFormatters'

const props = defineProps<{
  announcements: PublishedAnnouncementListItem[]
  contentById: Record<string, string | undefined>
  loadingIds: Set<string>
  loading: boolean
  open: boolean
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'load-content', id: string): void
}>()

const expandedIds = ref(new Set<string>())

function toggleExpand(id: string) {
  const next = new Set(expandedIds.value)
  if (next.has(id)) {
    if (!props.contentById[id]) {
      emit('load-content', id)
      return
    }
    next.delete(id)
  } else {
    next.add(id)
    if (!props.contentById[id]) {
      emit('load-content', id)
    }
  }
  expandedIds.value = next
}

function contentActionLabel(id: string): string {
  if (!expandedIds.value.has(id)) return '查看内容'
  return props.contentById[id] ? '收起' : '重试'
}

watch(
  () => props.open,
  (open) => {
    if (!open) {
      expandedIds.value = new Set()
    }
  }
)
</script>

<style scoped>
.announcement-empty {
  padding: 36px 0;
}

.announcement-timeline {
  max-height: min(62vh, 640px);
  overflow-y: auto;
  padding: 10px 8px 0 0;
}

.announcement-timeline :deep(.ant-timeline-item-content) {
  top: 0;
}

.announcement-item {
  min-width: 0;
  padding-bottom: 12px;
}

.announcement-item-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.announcement-title-wrap {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 8px;
}

.announcement-title-wrap h3 {
  min-width: 0;
  margin: 0;
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
  line-height: 22px;
  overflow-wrap: anywhere;
}

.announcement-item time {
  flex: 0 0 auto;
  color: #64748b;
  font-size: 12px;
  line-height: 22px;
  white-space: nowrap;
}

.announcement-item p {
  display: -webkit-box;
  margin: 0;
  overflow: hidden;
  color: #334155;
  font-size: 14px;
  line-height: 24px;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
}

.announcement-item p.expanded {
  display: block;
}

.content-loading {
  display: block;
  min-height: 24px;
}

.expand-button {
  height: 24px;
  margin-top: 4px;
  padding: 0;
}

@media (max-width: 640px) {
  .announcement-item-head {
    flex-direction: column;
    gap: 4px;
  }

  .announcement-item time {
    white-space: normal;
  }
}
</style>
