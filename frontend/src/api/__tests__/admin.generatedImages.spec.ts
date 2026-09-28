import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, remove } = vi.hoisted(() => ({ get: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, delete: remove } }))
import { generatedImagesAPI } from '@/api/admin/generatedImages'

describe('admin generated images API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('passes pagination, filters and cancellation through the authenticated client', async () => {
    const data = { items: [], total: 0, page: 2, page_size: 24, retention_days: 30 }
    const params = { page: 2, page_size: 24, user_id: 7, model: 'gpt-image-1', start_date: '2026-09-20', end_date: '2026-09-23' }
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data })
    expect(await generatedImagesAPI.list(params, signal)).toEqual(data)
    expect(get).toHaveBeenCalledWith('/admin/generated-images', { params, signal })
  })

  it('requests authenticated binary content and safely encodes the image identifier', async () => {
    const data = new Blob(['image'], { type: 'image/png' })
    const signal = new AbortController().signal
    get.mockResolvedValueOnce({ data })
    expect(await generatedImagesAPI.content('image/id', signal)).toBe(data)
    expect(get).toHaveBeenCalledWith('/admin/generated-images/image%2Fid/content', { responseType: 'blob', signal })
  })

  it('deletes the selected identifier through the same authenticated client', async () => {
    const signal = new AbortController().signal
    await generatedImagesAPI.remove('image/id', signal)
    expect(remove).toHaveBeenCalledWith('/admin/generated-images/image%2Fid', { signal })
  })
})
