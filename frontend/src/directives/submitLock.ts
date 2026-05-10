import type { Directive } from 'vue'

interface SubmitLockBinding {
  key?: string
  pending?: boolean
  cooldownMs?: number
}

interface SubmitLockElement extends HTMLElement {
  __submitLockHandler__?: EventListener
  __submitLockLastKey__?: string
  __submitLockLastAt__?: number
}

const defaultCooldownMs = 600

export const submitLockDirective: Directive<SubmitLockElement, SubmitLockBinding | boolean | undefined> = {
  mounted(element, binding) {
    const handler: EventListener = (event) => {
      const value = normalizeBinding(binding.value)
      if (value.pending) {
        stopEvent(event)
        return
      }

      const now = Date.now()
      const cooldownMs = value.cooldownMs ?? defaultCooldownMs
      const key = value.key || 'default'
      if (element.__submitLockLastKey__ === key && element.__submitLockLastAt__ && now - element.__submitLockLastAt__ < cooldownMs) {
        stopEvent(event)
        return
      }

      element.__submitLockLastKey__ = key
      element.__submitLockLastAt__ = now
    }

    element.__submitLockHandler__ = handler
    element.addEventListener('click', handler, true)
    applyDisabledState(element, normalizeBinding(binding.value).pending)
  },
  updated(element, binding) {
    applyDisabledState(element, normalizeBinding(binding.value).pending)
  },
  unmounted(element) {
    if (element.__submitLockHandler__) {
      element.removeEventListener('click', element.__submitLockHandler__, true)
    }
    delete element.__submitLockHandler__
    delete element.__submitLockLastKey__
    delete element.__submitLockLastAt__
  }
}

function normalizeBinding(value: SubmitLockBinding | boolean | undefined): Required<Pick<SubmitLockBinding, 'pending'>> & Omit<SubmitLockBinding, 'pending'> {
  if (typeof value === 'boolean') {
    return { pending: value }
  }
  return {
    key: value?.key,
    pending: value?.pending === true,
    cooldownMs: value?.cooldownMs
  }
}

function applyDisabledState(element: HTMLElement, pending: boolean): void {
  if (!isNativeButton(element)) {
    return
  }
  element.disabled = pending
}

function stopEvent(event: Event): void {
  event.preventDefault()
  event.stopPropagation()
  if (typeof event.stopImmediatePropagation === 'function') {
    event.stopImmediatePropagation()
  }
}

function isNativeButton(element: HTMLElement): element is HTMLButtonElement {
  return element instanceof HTMLButtonElement
}
