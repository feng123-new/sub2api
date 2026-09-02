import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import UseKeyModal from '../UseKeyModal.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true)
  })
}))

describe('UseKeyModal Gemini config', () => {
  it('renders Gemini 3.6 Flash and retains Gemini 3.5 Flash', async () => {
    const wrapper = mount(UseKeyModal, {
      props: {
        show: true,
        apiKey: 'sk-test',
        baseUrl: 'https://example.com/v1',
        platform: 'gemini'
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    const opencodeTab = wrapper.findAll('button').find((button) =>
      button.text().includes('keys.useKeyModal.cliTabs.opencode')
    )
    expect(opencodeTab).toBeDefined()
    if (!opencodeTab) throw new Error('OpenCode tab not found')
    await opencodeTab.trigger('click')
    await nextTick()

    const parsed = JSON.parse(wrapper.find('pre code').text())
    const models = parsed.provider.gemini.models
    expect(models['gemini-3.6-flash']).toMatchObject({
      name: 'Gemini 3.6 Flash',
      limit: { context: 1048576, output: 65536 }
    })
    expect(models['gemini-3.5-flash']).toBeDefined()
  })
})
