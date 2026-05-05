<template>
  <a-modal v-model:open="open" title="统一授权规则" width="640px" :footer="null">
    <div class="authorization-help-content">
      <div class="authorization-help-section">
        <span class="authorization-help-title">授权范围</span>
        <p>资源所有者可以把自有 AI 账户或分组授权给系统账户或系统团队；授权只提供使用权，不开放编辑、删除、查看敏感凭据或继续转授权。</p>
      </div>
      <div class="authorization-help-section">
        <span class="authorization-help-title">团队生效</span>
        <p>团队授权会自动展开到团队内启用成员；新增成员、移除成员、团队停用或系统账户停用后，会影响对应用户是否还能继续使用。</p>
      </div>
      <div class="authorization-help-section">
        <span class="authorization-help-title">来源合并</span>
        <p>同一用户通过个人和团队拿到同一资源时，列表只保留一条有效授权，并在“授权来源”里展示个人来源和团队来源。</p>
      </div>
      <div class="authorization-help-section">
        <span class="authorization-help-title">用量口径</span>
        <p>授权用量不包含资源归属人自己的消耗；团队视图只是团队成员用户消耗的汇总，真实资源总量仍归资源所有者。</p>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
const open = defineModel<boolean>('open', { required: true })
</script>

<style scoped>
.authorization-help-content {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.authorization-help-section {
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.authorization-help-title {
  display: block;
  margin-bottom: 6px;
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.authorization-help-section p {
  margin: 0;
  color: #475569;
  font-size: 13px;
  line-height: 1.7;
}
</style>
