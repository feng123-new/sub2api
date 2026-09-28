import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { GeneratedImage, GeneratedImagePage } from '@/api/admin/generatedImages'
import GeneratedImagesView from '../GeneratedImagesView.vue'

const { list, content, remove } = vi.hoisted(() => ({ list: vi.fn(), content: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/admin/generatedImages', () => ({ generatedImagesAPI: { list, content, remove } }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))

function image(id = 'image-1'): GeneratedImage {
  return { id, user_id: 7, user_email: 'user@example.test', group_id: 2, group_name: 'Images', api_key_id: 3, account_id: 4, model: 'gpt-image-1', request_id: 'request-1', endpoint: '/v1/images/generations', mime_type: 'image/png', byte_size: 2048, width: 1024, height: 1024, created_at: '2026-09-23T08:00:00Z', expires_at: '2026-09-30T08:00:00Z' }
}
function result(items = [image()], total = items.length): GeneratedImagePage {
  return { items, total, page: 1, page_size: 24, retention_days: 30 }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const blob = () => new Blob(['image'], { type: 'image/png' })
const wrappers: ReturnType<typeof mount>[] = []
function mountView() {
  const wrapper = mount(GeneratedImagesView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { props: ['show', 'title'], template: '<section v-if="show" role="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' },
        Pagination: { name: 'Pagination', props: ['page', 'pageSize', 'total', 'showPageSizeSelector'], emits: ['update:page'], template: '<div data-testid="pagination" />' }
      }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}
function button(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find(item => item.text() === text)!
}

describe('admin generated images gallery', () => {
  beforeEach(() => {
    list.mockReset().mockResolvedValue(result())
    content.mockReset().mockResolvedValue(blob())
    remove.mockReset().mockResolvedValue(undefined)
    let sequence = 0
    vi.stubGlobal('URL', class extends URL {
      static createObjectURL = vi.fn(() => `blob:image-${++sequence}`)
      static revokeObjectURL = vi.fn()
    })
  })
  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('renders metadata and retention, and previews/downloads the authenticated blob without another request', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(list).toHaveBeenCalledWith({ page: 1, page_size: 24 }, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('admin.generatedImages.retention')
    expect(wrapper.text()).toContain('user@example.test')
    expect(wrapper.text()).toContain('gpt-image-1')
    expect(wrapper.get('img').attributes('src')).toBe('blob:image-1')
    expect(wrapper.get('time').attributes('datetime')).toBe(image().created_at)
    await button(wrapper, 'admin.generatedImages.preview').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('request-1')
    const download = wrapper.get('a[download]')
    expect(download.attributes('href')).toBe('blob:image-1')
    expect(download.attributes('download')).toBe('generated-image-1.png')
    expect(content).toHaveBeenCalledTimes(1)
  })

  it('shows loading, list error, retry and empty states', async () => {
    const pending = deferred<GeneratedImagePage>()
    list.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="status"]').exists()).toBe(true)
    pending.reject(new Error('offline'))
    await flushPromises()
    expect(wrapper.get('[role="alert"] p').text()).toBe('admin.generatedImages.loadFailed')
    list.mockResolvedValueOnce(result([]))
    await button(wrapper, 'admin.generatedImages.retry').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.generatedImages.empty')
    expect(wrapper.findComponent({ name: 'Pagination' }).exists()).toBe(false)
  })

  it('applies filters, retains them across pages and refresh, then resets to page one', async () => {
    list.mockResolvedValue(result([image()], 50))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#generated-user').setValue('7')
    await wrapper.get('#generated-model').setValue(' gpt-image-1 ')
    await wrapper.get('#generated-start').setValue('2026-09-20')
    await wrapper.get('#generated-end').setValue('2026-09-23')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const filters = { user_id: 7, model: 'gpt-image-1', start_date: '2026-09-20', end_date: '2026-09-23' }
    expect(list).toHaveBeenLastCalledWith({ ...filters, page: 1, page_size: 24 }, expect.any(AbortSignal))
    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ ...filters, page: 2, page_size: 24 }, expect.any(AbortSignal))
    expect(wrapper.getComponent({ name: 'Pagination' }).props('showPageSizeSelector')).toBe(false)
    await button(wrapper, 'common.refresh').trigger('click')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ ...filters, page: 2, page_size: 24 }, expect.any(AbortSignal))
    await button(wrapper, 'admin.generatedImages.resetFilters').trigger('click')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ user_id: undefined, model: undefined, start_date: undefined, end_date: undefined, page: 1, page_size: 24 }, expect.any(AbortSignal))
  })

  it('rejects invalid user IDs and reversed date filters without issuing a request', async () => {
    const wrapper = mountView()
    await flushPromises()
    for (const userId of ['-1', '0', '1.5', '9007199254740992']) {
      await wrapper.get('#generated-user').setValue(userId)
      await wrapper.get('form').trigger('submit')
      expect(list).toHaveBeenCalledTimes(1)
    }
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.generatedImages.invalidFilters')
    await wrapper.get('#generated-user').setValue('')
    await wrapper.get('#generated-start').setValue('2026-09-23')
    await wrapper.get('#generated-end').setValue('2026-09-20')
    await wrapper.get('form').trigger('submit')
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('loads at most 24 image files with only three simultaneous requests', async () => {
    list.mockResolvedValue(result(Array.from({ length: 30 }, (_, index) => image(`image-${index}`)), 30))
    const pending: ReturnType<typeof deferred<Blob>>[] = []
    content.mockImplementation(() => {
      const request = deferred<Blob>()
      pending.push(request)
      return request.promise
    })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('[data-testid="generated-image-card"]')).toHaveLength(24)
    expect(content).toHaveBeenCalledTimes(3)
    for (let index = 0; index < 24; index++) {
      expect(pending.length - index).toBeLessThanOrEqual(3)
      pending[index].resolve(blob())
      await flushPromises()
    }
    expect(content).toHaveBeenCalledTimes(24)
    expect(wrapper.findAll('img')).toHaveLength(24)
  })

  it('aborts superseded list requests and ignores late metadata', async () => {
    const old = deferred<GeneratedImagePage>()
    list.mockReturnValueOnce(old.promise).mockResolvedValueOnce(result([image('new')]))
    const wrapper = mountView()
    const signal = list.mock.calls[0][1] as AbortSignal
    await button(wrapper, 'common.refresh').trigger('click')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    old.resolve(result([image('old')]))
    await flushPromises()
    expect(content).toHaveBeenCalledTimes(1)
    expect(content).toHaveBeenCalledWith('new', expect.any(AbortSignal))
  })

  it('ignores stale blob completions, releases URLs on replacement, and aborts on unmount', async () => {
    const old = deferred<Blob>()
    content.mockReturnValueOnce(old.promise)
    const wrapper = mountView()
    await flushPromises()
    const oldSignal = content.mock.calls[0][1] as AbortSignal
    await button(wrapper, 'common.refresh').trigger('click')
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    old.resolve(blob())
    await flushPromises()
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
    await button(wrapper, 'common.refresh').trigger('click')
    await flushPromises()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
    const lastSignal = content.mock.calls.at(-1)![1] as AbortSignal
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(lastSignal.aborted).toBe(true)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-2')
  })

  it('stops queued blob requests on unmount without creating orphan URLs', async () => {
    const pending = deferred<Blob>()
    list.mockResolvedValue(result(Array.from({ length: 10 }, (_, index) => image(String(index)))))
    content.mockReturnValue(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    pending.resolve(blob())
    await flushPromises()
    expect(content).toHaveBeenCalledTimes(3)
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('isolates a failed image without hiding other images', async () => {
    list.mockResolvedValue(result([image('failed'), image('ok')]))
    content.mockRejectedValueOnce(new Error('expired'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.generatedImages.contentFailed')
    expect(wrapper.findAll('img')).toHaveLength(1)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('requires explicit confirmation, supports cancel and prevents duplicate delete requests', async () => {
    const pending = deferred<void>()
    remove.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await button(wrapper, 'common.delete').trigger('click')
    expect(remove).not.toHaveBeenCalled()
    await button(wrapper, 'common.cancel').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    await button(wrapper, 'common.delete').trigger('click')
    await wrapper.get('[data-testid="confirm-image-delete"]').trigger('click')
    await wrapper.get('[data-testid="confirm-image-delete"]').trigger('click')
    expect(remove).toHaveBeenCalledTimes(1)
    expect(remove).toHaveBeenCalledWith('image-1', expect.any(AbortSignal))
    list.mockResolvedValueOnce(result([]))
    pending.resolve()
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.generatedImages.empty')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:image-1')
  })

  it('keeps failed deletion reviewable and allows retry', async () => {
    remove.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountView()
    await flushPromises()
    await button(wrapper, 'common.delete').trigger('click')
    await wrapper.get('[data-testid="confirm-image-delete"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="dialog"] [role="alert"]').text()).toBe('admin.generatedImages.deleteFailed')
    expect(list).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="confirm-image-delete"]').trigger('click')
    await flushPromises()
    expect(remove).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('returns to the previous page after deleting the last item on a later page', async () => {
    list.mockResolvedValue(result([image()], 25))
    const wrapper = mountView()
    await flushPromises()
    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    await button(wrapper, 'common.delete').trigger('click')
    list.mockResolvedValueOnce(result([image('remaining')], 24))
    await wrapper.get('[data-testid="confirm-image-delete"]').trigger('click')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ page: 1, page_size: 24 }, expect.any(AbortSignal))
  })
})
