import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModelsMock } = vi.hoisted(() => ({
  getAvailableModelsMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels: getAvailableModelsMock
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: { type: [String, Number, Boolean, null], default: '' },
    options: { type: Array, default: () => [] },
    valueKey: { type: String, default: 'value' },
    labelKey: { type: String, default: 'label' }
  },
  emits: ['update:modelValue'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value)"
    >
      <option
        v-for="option in options"
        :key="option[valueKey]"
        :value="option[valueKey]"
      >
        {{ option[labelKey] }}
      </option>
    </select>
  `
})

const TextAreaStub = defineComponent({
  name: 'TextArea',
  props: {
    modelValue: { type: String, default: '' }
  },
  emits: ['update:modelValue'],
  template: `
    <textarea
      v-bind="$attrs"
      :value="modelValue"
      @input="$emit('update:modelValue', $event.target.value)"
    />
  `
})

function buildAccount() {
  return {
    id: 1,
    name: 'OpenAI OAuth',
    platform: 'openai',
    type: 'oauth',
    status: 'active',
    credentials: {},
    extra: {},
    concurrency: 1,
    priority: 1,
    proxy_id: null,
    auto_pause_on_expired: false
  } as any
}

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

describe('AccountTestModal', () => {
  const originalFetch = global.fetch

  beforeEach(() => {
    getAvailableModelsMock.mockReset()
    getAvailableModelsMock.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      body: {
        getReader: () => ({
          read: vi.fn().mockResolvedValue({ done: true, value: undefined })
        })
      }
    } as any)
    localStorage.setItem('auth_token', 'test-token')
  })

  afterEach(() => {
    global.fetch = originalFetch
    localStorage.clear()
  })

  it('posts compact mode for OpenAI compact probe', async () => {
    const wrapper = mount(AccountTestModal, {
      props: {
        show: true,
        account: buildAccount()
      },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          Select: SelectStub,
          TextArea: TextAreaStub,
          Icon: true
        }
      }
    })

    await flushPromises()
    ;(wrapper.vm as any).selectedModelId = 'gpt-5.4'
    ;(wrapper.vm as any).testMode = 'compact'
    await (wrapper.vm as any).startTest()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, options] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(options.body)).toMatchObject({
      model_id: 'gpt-5.4',
      mode: 'compact'
    })
  })

  it('renders Chat Completions path status from test SSE', async () => {
    const encoder = new TextEncoder()
    const chunks = [
      encoder.encode('data: {"type":"status","text":"已通过 /v1/chat/completions 验证"}\n\n'),
      encoder.encode('data: {"type":"test_complete","success":true}\n\n')
    ]
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      body: {
        getReader: () => ({
          read: vi.fn().mockImplementation(() => Promise.resolve(
            chunks.length > 0
              ? { done: false, value: chunks.shift() }
              : { done: true, value: undefined }
          ))
        })
      }
    } as any)

    const wrapper = mount(AccountTestModal, {
      props: {
        show: true,
        account: buildAccount()
      },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          Select: SelectStub,
          TextArea: TextAreaStub,
          Icon: true
        }
      }
    })

    await flushPromises()
    ;(wrapper.vm as any).selectedModelId = 'gpt-5.4'
    await (wrapper.vm as any).startTest()
    await flushPromises()

    expect(wrapper.text()).toContain('已通过 /v1/chat/completions 验证')
  })

  it('renders Grok timing metrics from a successful completion event', async () => {
    getAvailableModelsMock.mockResolvedValue([
      { id: 'grok-4.3', display_name: 'Grok 4.3' }
    ])
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(createStreamResponse([
        'data: {"type":"test_complete","success":true,"ttft_ms":345,"duration_ms":1234}\n'
      ]))
      .mockResolvedValueOnce(createStreamResponse([
        'data: {"type":"test_complete","success":true}\n'
      ])))

    const wrapper = mount(AccountTestModal, {
      props: {
        show: true,
        account: {
          ...buildAccount(),
          name: 'Grok OAuth',
          platform: 'grok',
          type: 'oauth'
        }
      },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          Select: SelectStub,
          TextArea: TextAreaStub,
          Icon: true
        }
      }
	})

	await flushPromises()
    const modelSelect = wrapper.getComponent(SelectStub)
    modelSelect.vm.$emit('update:modelValue', 'grok-4.3')
    await flushPromises()
	const startButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()
    if (!startButton) throw new Error('start test button not found')
    expect(startButton.attributes('disabled')).toBeUndefined()
    await startButton.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.testFirstToken')
    expect(wrapper.text()).toContain('345ms')
    expect(wrapper.text()).toContain('admin.accounts.testDuration')
    expect(wrapper.text()).toContain('1.23s')

    await startButton.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.testCompleted')
    expect(wrapper.text()).not.toContain('admin.accounts.testFirstToken')
    expect(wrapper.text()).not.toContain('admin.accounts.testDuration')
  })
})
