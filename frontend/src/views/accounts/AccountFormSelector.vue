<template>
  <template v-if="!editing">
    <div class="setup-progress">
      <div class="setup-step" :class="{ active: !providerCode, done: Boolean(providerCode) }">
        <span>1</span>
        <strong>选择供应商</strong>
      </div>
      <div class="setup-step" :class="{ active: Boolean(providerCode) && !accountType, done: Boolean(accountType) }">
        <span>2</span>
        <strong>选择类型</strong>
      </div>
      <div class="setup-step" :class="{ active: Boolean(providerCode && accountType) }">
        <span>3</span>
        <strong>填写配置</strong>
      </div>
    </div>
  </template>

  <section class="form-section selector-section">
    <div class="form-section-head">
      <div>
        <h4>选择供应商</h4>
        <p>未来接入 Claude Code、Gemini 等供应商时，也会从这里进入。</p>
      </div>
    </div>
    <div class="choice-grid provider-choice-grid">
      <button
        v-for="provider in providers"
        :key="provider.code"
        type="button"
        class="choice-card provider-choice-card"
        :class="{ active: providerCode === provider.code, disabled: editing || !provider.enabled }"
        :disabled="editing || !provider.enabled"
        @click="$emit('select-provider', provider.code)"
      >
        <span class="choice-card-icon">{{ provider.name.slice(0, 1).toUpperCase() }}</span>
        <span class="choice-card-content">
          <strong>{{ provider.name }}</strong>
          <small>{{ providerAccountTypeCount(provider) }} 种账户类型</small>
        </span>
        <a-tag :color="provider.enabled ? 'green' : 'default'">{{ provider.enabled ? '可用' : '停用' }}</a-tag>
      </button>
    </div>
  </section>

  <section v-if="selectedProtocolProfile" class="form-section selector-section">
    <div class="form-section-head">
      <div>
        <h4>选择账户类型</h4>
        <p>{{ selectedProvider?.name || '当前供应商' }} 当前支持 {{ accountTypeChoices.length }} 种账户创建方式。</p>
      </div>
    </div>
    <div class="choice-grid type-choice-grid">
      <button
        v-for="item in accountTypeChoices"
        :key="item.value"
        type="button"
        class="choice-card type-choice-card"
        :class="{ active: accountType === item.value, disabled: editing }"
        :disabled="editing"
        @click="$emit('select-type', item.value)"
      >
        <span class="choice-card-content">
          <strong>{{ item.label }}</strong>
          <small>{{ item.description }}</small>
        </span>
        <a-tag color="blue">{{ item.tag }}</a-tag>
      </button>
    </div>
    <div class="type-note">账号类型只影响上游能力和转发方式；统计、会话亲和和缓存按本地 API Key 与分组连续。</div>
  </section>
</template>

<script setup lang="ts">
import type { AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'

interface AccountTypeChoice {
  value: AccountType
  label: string
  description: string
  tag: string
}

defineProps<{
  accountType: AccountType
  accountTypeChoices: AccountTypeChoice[]
  editing: boolean
  providerCode: string
  providers: ProviderDefinition[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
}>()

defineEmits<{
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type', type: AccountType): void
}>()

function providerAccountTypeCount(provider: ProviderDefinition): number {
  const accountTypes = provider.protocolProfiles.length
    ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
    : provider.accountTypes
  return new Set(accountTypes).size
}
</script>

<style scoped>
.setup-progress {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.setup-step {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  color: #64748b;
  border: 1px solid #e8edf5;
  border-radius: 14px;
  background: #f8fafc;
}

.setup-step span {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  color: #64748b;
  font-weight: 700;
  border-radius: 999px;
  background: #e2e8f0;
}

.setup-step.active {
  color: #1d4ed8;
  border-color: #bfdbfe;
  background: linear-gradient(135deg, #eff6ff 0%, #ffffff 100%);
}

.setup-step.active span,
.setup-step.done span {
  color: #fff;
  background: #2563eb;
}

.setup-step.done {
  color: #0f172a;
  border-color: #bbf7d0;
  background: #f0fdf4;
}

.form-section {
  padding: 16px;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  background: #fff;
}

.form-section-head {
  margin-bottom: 12px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.selector-section {
  background: linear-gradient(180deg, #ffffff 0%, #f8fafc 100%);
}

.choice-grid {
  display: grid;
  gap: 12px;
}

.provider-choice-grid {
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
}

.type-choice-grid {
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
}

.choice-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  min-height: 82px;
  padding: 14px;
  text-align: left;
  cursor: pointer;
  border: 1px solid #dbe3ef;
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.92);
  box-shadow: 0 8px 22px rgba(15, 23, 42, 0.04);
  transition: border-color 0.18s ease, box-shadow 0.18s ease, transform 0.18s ease;
}

.choice-card:hover:not(.disabled) {
  border-color: #93c5fd;
  box-shadow: 0 14px 32px rgba(37, 99, 235, 0.12);
  transform: translateY(-1px);
}

.choice-card.active {
  border-color: #2563eb;
  background: linear-gradient(135deg, #eff6ff 0%, #ffffff 78%);
  box-shadow: 0 16px 34px rgba(37, 99, 235, 0.14);
}

.choice-card.disabled {
  cursor: not-allowed;
  opacity: 0.68;
}

.choice-card-icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  color: #fff;
  font-size: 18px;
  font-weight: 800;
  border-radius: 14px;
  background: linear-gradient(135deg, #2563eb 0%, #7c3aed 100%);
}

.choice-card-content {
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.choice-card-content strong {
  color: #0f172a;
  font-size: 15px;
}

.choice-card-content small {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.type-note {
  margin-top: 8px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

@media (max-width: 992px) {
  .setup-progress {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 900px) {
  .choice-card {
    align-items: flex-start;
  }

  .provider-choice-card {
    flex-wrap: wrap;
  }
}
</style>
