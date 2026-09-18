import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import AntiDegradeView from '../AntiDegradeView.vue'

const {
  getOverview,
  updateAccount,
  triggerMint,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getOverview: vi.fn(),
  updateAccount: vi.fn(),
  triggerMint: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin/codexState', () => ({
  codexStateAPI: {
    getOverview,
    updateAccount,
    triggerMint,
  },
  default: { getOverview, updateAccount, triggerMint },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
}))

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = { template: '<div><slot name="filters" /><slot name="table" /></div>' }
const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number, Boolean],
      default: null,
    },
  },
  template: '<div data-testid="normal-proxy-select">{{ modelValue }}</div>',
})
const ModelTagInputStub = defineComponent({
  props: {
    models: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:models'],
  setup(props, { emit }) {
    return () => h('input', {
      'data-testid': 'managed-models',
      value: (props.models as string[]).join(','),
      onInput: (event: Event) => {
        const value = (event.target as HTMLInputElement).value
        emit('update:models', value.split(',').map(item => item.trim()).filter(Boolean))
      },
    })
  },
})

function account(id: number, mode: 'off' | 'observe' | 'manual' | 'auto', configuredModels: string[] = []) {
  return {
    id,
    name: `Account ${id}`,
    status: 'active',
    schedulable: true,
    mode,
    concurrency: 1,
    refresh_before_minutes: 10,
    degraded: false,
    normal_proxy_id: undefined,
    configured_models: configuredModels,
    state_proxy_ids: [7],
    models: configuredModels.length === 0 && mode === 'off'
      ? []
      : [{
          model: configuredModels[0] || 'gpt-6-astra',
          degraded: false,
          current: {
            issued_at: '2026-09-18T08:00:00Z',
            digest: 'a1b2c3d4e5f6',
            decoded_length: 192,
            ciphertext_blocks: 12,
            length: 218,
            proxy_id: 7,
            acquired_at: '2026-09-18T08:00:01Z',
            expires_at: '2026-09-18T09:00:00Z',
            verified_at: '2026-09-18T08:00:02Z',
            value: 'raw-state-value-must-never-render',
          },
          mint_run: {
            running: false,
            attempts: 0,
          },
        }],
  }
}

function mountView() {
  return mount(AntiDegradeView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        BaseDialog: BaseDialogStub,
        Icon: true,
        HelpTooltip: { template: '<span><slot /></span>' },
        Select: SelectStub,
        ModelTagInput: ModelTagInputStub,
      },
    },
  })
}

describe('admin AntiDegradeView Codex state controls', () => {
  beforeEach(() => {
    getOverview.mockReset()
    updateAccount.mockReset()
    triggerMint.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    getOverview.mockResolvedValue({
      default_model: 'gpt-6-astra',
      proxies: [{ id: 7, name: 'Capture proxy', protocol: 'http', host: '127.0.0.1', port: 8080, status: 'active' }],
      accounts: [
        account(1, 'off'),
        account(2, 'observe'),
        account(3, 'manual'),
        account(4, 'auto', ['gpt-6-astra']),
      ],
    })
    updateAccount.mockResolvedValue(account(4, 'auto', ['gpt-6-astra', 'gpt-6-mini']))
    triggerMint.mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('offers bounded capture only for manual and auto accounts and never renders raw state', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="codex-state-capture-1-gpt-6-astra"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="codex-state-capture-2-gpt-6-astra"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="codex-state-capture-3-gpt-6-astra"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="codex-state-capture-4-gpt-6-astra"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('a1b2c3d4e5f6')
    expect(wrapper.html()).not.toContain('raw-state-value-must-never-render')

    await wrapper.get('[data-testid="codex-state-capture-3-gpt-6-astra"]').trigger('click')
    await flushPromises()

    expect(triggerMint).toHaveBeenCalledWith(3, 'gpt-6-astra')
    wrapper.unmount()
  })

  it('saves the selected mode, managed models, proxies, and auto-only refresh setting', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="codex-state-config-4"]').trigger('click')
    await wrapper.get('[data-testid="codex-state-mode-auto"]').trigger('click')
    await wrapper.get('[data-testid="managed-models"]').setValue('gpt-6-astra,gpt-6-mini')

    expect(wrapper.find('[data-testid="codex-state-refresh-before-minutes"]').exists()).toBe(true)

    await wrapper.get('[data-testid="codex-state-save"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(4, {
      mode: 'auto',
      models: ['gpt-6-astra', 'gpt-6-mini'],
      refresh_before_minutes: 10,
      normal_proxy_id: 0,
      state_proxy_ids: [7],
    })
    wrapper.unmount()
  })
})
