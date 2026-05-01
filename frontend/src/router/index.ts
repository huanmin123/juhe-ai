import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/providers' },
    { path: '/providers', component: () => import('@/views/providers/ProvidersView.vue') },
    { path: '/accounts', component: () => import('@/views/accounts/AccountsView.vue') },
    { path: '/groups', component: () => import('@/views/groups/GroupsView.vue') },
    { path: '/api-keys', component: () => import('@/views/api-keys/ApiKeysView.vue') },
    { path: '/proxies', component: () => import('@/views/proxies/ProxiesView.vue') },
    { path: '/usage-records', component: () => import('@/views/usage-records/UsageRecordsView.vue') },
    { path: '/settings', component: () => import('@/views/settings/SettingsView.vue') }
  ]
})

