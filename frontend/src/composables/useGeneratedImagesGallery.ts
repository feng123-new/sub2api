import { onBeforeUnmount, ref } from 'vue'
import { generatedImagesAPI, type GeneratedImage, type GeneratedImageFilters } from '@/api/admin/generatedImages'

export const GENERATED_IMAGES_PAGE_SIZE = 24
const CONTENT_CONCURRENCY = 3

export interface GalleryImage {
  image: GeneratedImage
  url: string
  status: 'loading' | 'ready' | 'error'
}

export function useGeneratedImagesGallery() {
  const items = ref<GalleryImage[]>([])
  const total = ref(0)
  const page = ref(1)
  const loading = ref(false)
  const failed = ref(false)
  let controller: AbortController | undefined

  function clear() {
    controller?.abort()
    for (const item of items.value) {
      if (item.url) URL.revokeObjectURL(item.url)
    }
    items.value = []
  }

  async function loadContent(rows: GalleryImage[], signal: AbortSignal) {
    let next = 0
    async function worker() {
      while (!signal.aborted && next < rows.length) {
        const row = rows[next++]
        try {
          const blob = await generatedImagesAPI.content(row.image.id, signal)
          // A superseded request can still resolve when a transport ignores abort.
          if (signal.aborted) return
          row.url = URL.createObjectURL(blob)
          row.status = 'ready'
        } catch {
          if (signal.aborted) return
          row.status = 'error'
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(CONTENT_CONCURRENCY, rows.length) }, worker))
  }

  async function load(filters: GeneratedImageFilters = {}, nextPage = 1) {
    clear()
    const current = new AbortController()
    controller = current
    page.value = nextPage
    total.value = 0
    loading.value = true
    failed.value = false
    try {
      const result = await generatedImagesAPI.list({ ...filters, page: nextPage, page_size: GENERATED_IMAGES_PAGE_SIZE }, current.signal)
      if (current.signal.aborted) return
      const lastPage = Math.max(1, Math.ceil(result.total / GENERATED_IMAGES_PAGE_SIZE))
      if (nextPage > lastPage) {
        await load(filters, lastPage)
        return
      }
      total.value = result.total
      // Keep browser memory and request count bounded even if a server ignores page_size.
      items.value = result.items.slice(0, GENERATED_IMAGES_PAGE_SIZE).map(image => ({ image, url: '', status: 'loading' }))
      void loadContent(items.value, current.signal)
    } catch {
      if (!current.signal.aborted) failed.value = true
    } finally {
      if (!current.signal.aborted) loading.value = false
    }
  }

  onBeforeUnmount(clear)
  return { items, total, page, loading, failed, load }
}
