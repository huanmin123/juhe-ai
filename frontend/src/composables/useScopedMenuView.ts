import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import { authState } from './useAuth'

export function useScopedMenuView() {
  const route = useRoute()
  const isManagementView = computed(() => authState.isAdmin.value && route.meta.viewScope === 'admin')

  function scopedSystemAccountId(filterValue = allSystemAccountsValue): string | undefined {
    if (isManagementView.value) {
      return selectedSystemAccountId(filterValue, true)
    }
    return authState.currentUser.value?.id
  }

  return {
    isManagementView,
    scopedSystemAccountId
  }
}
