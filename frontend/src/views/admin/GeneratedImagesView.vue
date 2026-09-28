<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="space-y-2">
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('admin.generatedImages.title') }}</h1>
          <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.generatedImages.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="deleting" @click="reload(page)">
          <Icon name="refresh" size="sm" class="mr-2" />{{ t('common.refresh') }}
        </button>
      </div>

      <div class="rounded-xl border border-primary-200 bg-primary-50 p-4 text-sm text-primary-800 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-200">
        {{ t('admin.generatedImages.retention') }}
      </div>

      <form class="card space-y-4 p-4" @submit.prevent="applyFilters">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <div>
            <label for="generated-user" class="input-label">{{ t('admin.generatedImages.userId') }}</label>
            <input id="generated-user" v-model="filters.userId" class="input" type="number" min="1" step="1" :placeholder="t('admin.generatedImages.allUsers')" />
          </div>
          <div>
            <label for="generated-model" class="input-label">{{ t('admin.generatedImages.model') }}</label>
            <input id="generated-model" v-model="filters.model" class="input" type="text" :placeholder="t('admin.generatedImages.allModels')" />
          </div>
          <div>
            <label for="generated-start" class="input-label">{{ t('admin.generatedImages.startDate') }}</label>
            <input id="generated-start" v-model="filters.startDate" class="input" type="date" :max="filters.endDate || undefined" />
          </div>
          <div>
            <label for="generated-end" class="input-label">{{ t('admin.generatedImages.endDate') }}</label>
            <input id="generated-end" v-model="filters.endDate" class="input" type="date" :min="filters.startDate || undefined" />
          </div>
        </div>
        <p v-if="filterError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ filterError }}</p>
        <div class="flex flex-wrap gap-3">
          <button type="submit" class="btn btn-primary" :disabled="deleting">{{ t('admin.generatedImages.applyFilters') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="deleting" @click="resetFilters">{{ t('admin.generatedImages.resetFilters') }}</button>
        </div>
      </form>

      <div v-if="loading" class="card flex justify-center p-12" aria-live="polite"><LoadingSpinner /></div>
      <div v-else-if="failed" class="card p-6" role="alert">
        <p class="mb-4 text-sm text-red-600 dark:text-red-400">{{ t('admin.generatedImages.loadFailed') }}</p>
        <button type="button" class="btn btn-secondary" @click="reload(page)">{{ t('admin.generatedImages.retry') }}</button>
      </div>
      <EmptyState v-else-if="!items.length" class="card" :title="t('admin.generatedImages.empty')" :description="t('admin.generatedImages.emptyHint')" />
      <template v-else>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          <article v-for="row in items" :key="row.image.id" class="card overflow-hidden" data-testid="generated-image-card">
            <button type="button" class="flex aspect-square w-full items-center justify-center bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 dark:bg-dark-900" :aria-label="t('admin.generatedImages.previewImage', { model: row.image.model, user: row.image.user_email || row.image.user_id })" @click="selected = row">
              <img v-if="row.status === 'ready'" :src="row.url" :alt="t('admin.generatedImages.imageAlt', { model: row.image.model })" class="h-full w-full object-contain" loading="lazy" @error="row.status = 'error'" />
              <LoadingSpinner v-else-if="row.status === 'loading'" size="sm" />
              <span v-else class="p-4 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.generatedImages.contentFailed') }}</span>
            </button>
            <div class="space-y-2 p-4">
              <p class="break-words text-sm font-semibold text-gray-900 dark:text-white">{{ row.image.model || '—' }}</p>
              <p class="break-words text-sm text-gray-600 dark:text-dark-300">{{ row.image.user_email || t('admin.generatedImages.userId') }} <span class="text-gray-500 dark:text-dark-400">#{{ row.image.user_id }}</span></p>
              <time :datetime="row.image.created_at" class="block text-xs text-gray-500 dark:text-dark-400">{{ formatDate(row.image.created_at) }}</time>
              <div class="flex items-center justify-between gap-2 pt-2">
                <button type="button" class="btn btn-secondary btn-sm" @click="selected = row">{{ t('admin.generatedImages.preview') }}</button>
                <button type="button" class="btn btn-danger btn-sm" :disabled="deleting" @click="requestDelete(row)">{{ t('common.delete') }}</button>
              </div>
            </div>
          </article>
        </div>
        <div class="card overflow-hidden" :inert="deleting || undefined">
          <Pagination :total="total" :page="page" :page-size="GENERATED_IMAGES_PAGE_SIZE" :show-page-size-selector="false" @update:page="reload" />
        </div>
      </template>
    </div>

    <BaseDialog :show="!!selected" :title="t('admin.generatedImages.preview')" width="wide" @close="selected = null">
      <template v-if="selected">
        <div class="mb-6 flex min-h-48 items-center justify-center rounded-xl bg-gray-100 dark:bg-dark-900">
          <img v-if="selected.status === 'ready'" :src="selected.url" :alt="t('admin.generatedImages.imageAlt', { model: selected.image.model })" class="max-h-96 max-w-full object-contain" @error="selected.status = 'error'" />
          <LoadingSpinner v-else-if="selected.status === 'loading'" />
          <p v-else class="p-6 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.generatedImages.contentFailed') }}</p>
        </div>
        <dl class="grid grid-cols-1 gap-4 text-sm sm:grid-cols-2">
          <div v-for="field in details" :key="field.label" class="min-w-0">
            <dt class="text-gray-500 dark:text-dark-400">{{ field.label }}</dt>
            <dd class="mt-1 break-all text-gray-900 dark:text-dark-100">{{ field.value }}</dd>
          </div>
        </dl>
      </template>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="selected = null">{{ t('common.close') }}</button>
          <a v-if="selected?.url" :href="selected.url" :download="downloadName(selected.image)" class="btn btn-primary"><Icon name="download" size="sm" class="mr-2" />{{ t('admin.generatedImages.download') }}</a>
          <button v-if="selected" type="button" class="btn btn-danger" @click="requestDelete(selected)">{{ t('common.delete') }}</button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog :show="!!pendingDelete" :title="t('admin.generatedImages.deleteTitle')" width="narrow" :show-close-button="!deleting" :close-on-escape="!deleting" @close="cancelDelete">
      <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.generatedImages.deleteConfirm') }}</p>
      <p class="mt-3 break-all text-sm font-medium text-gray-900 dark:text-white">{{ pendingDelete?.image.model }} · {{ pendingDelete?.image.id }}</p>
      <p v-if="deleteError" role="alert" class="mt-4 text-sm text-red-600 dark:text-red-400">{{ t('admin.generatedImages.deleteFailed') }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="deleting" @click="cancelDelete">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-danger" :disabled="deleting" data-testid="confirm-image-delete" @click="confirmDelete">{{ deleting ? t('admin.generatedImages.deleting') : t('common.delete') }}</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { generatedImagesAPI, type GeneratedImage, type GeneratedImageFilters } from '@/api/admin/generatedImages'
import { GENERATED_IMAGES_PAGE_SIZE, useGeneratedImagesGallery, type GalleryImage } from '@/composables/useGeneratedImagesGallery'

const { t } = useI18n()
const { items, total, page, loading, failed, load } = useGeneratedImagesGallery()
const filters = reactive({ userId: '', model: '', startDate: '', endDate: '' })
let appliedFilters: GeneratedImageFilters = {}
const filterError = ref('')
const selected = ref<GalleryImage | null>(null)
const pendingDelete = ref<GalleryImage | null>(null)
const deleting = ref(false)
const deleteError = ref(false)
const lifetime = new AbortController()

const details = computed(() => {
  const image = selected.value?.image
  if (!image) return []
  return [
    { label: t('admin.generatedImages.userId'), value: `${image.user_email || '—'} (#${image.user_id})` },
    { label: t('admin.generatedImages.model'), value: image.model || '—' },
    { label: t('admin.generatedImages.group'), value: `${image.group_name || '—'} (#${image.group_id})` },
    { label: t('admin.generatedImages.dimensions'), value: image.width && image.height ? `${image.width} × ${image.height}` : '—' },
    { label: t('admin.generatedImages.size'), value: `${(image.byte_size / 1024).toLocaleString(undefined, { maximumFractionDigits: 1 })} KB` },
    { label: t('admin.generatedImages.mimeType'), value: image.mime_type },
    { label: t('admin.generatedImages.createdAt'), value: formatDate(image.created_at) },
    { label: t('admin.generatedImages.expiresAt'), value: formatDate(image.expires_at) },
    { label: t('admin.generatedImages.requestId'), value: image.request_id || '—' },
    { label: t('admin.generatedImages.endpoint'), value: image.endpoint || '—' },
    { label: t('admin.generatedImages.apiKeyId'), value: image.api_key_id || '—' },
    { label: t('admin.generatedImages.accountId'), value: image.account_id || '—' }
  ]
})

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

function downloadName(image: GeneratedImage) {
  const extensions: Record<string, string> = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif', 'image/avif': 'avif' }
  return `generated-${image.id}.${extensions[image.mime_type] || 'bin'}`
}

function reload(nextPage = 1) {
  if (deleting.value || lifetime.signal.aborted) return
  selected.value = null
  pendingDelete.value = null
  return load(appliedFilters, nextPage)
}

function applyFilters() {
  filterError.value = ''
  const userInput = String(filters.userId).trim()
  const userId = userInput ? Number(userInput) : undefined
  if ((userId !== undefined && (!Number.isSafeInteger(userId) || userId < 1)) || (filters.startDate && filters.endDate && filters.startDate > filters.endDate)) {
    filterError.value = t('admin.generatedImages.invalidFilters')
    return
  }
  appliedFilters = { user_id: userId, model: filters.model.trim() || undefined, start_date: filters.startDate || undefined, end_date: filters.endDate || undefined }
  void reload()
}

function resetFilters() {
  Object.assign(filters, { userId: '', model: '', startDate: '', endDate: '' })
  applyFilters()
}

function requestDelete(row: GalleryImage) {
  selected.value = null
  pendingDelete.value = row
  deleteError.value = false
}

function cancelDelete() {
  if (!deleting.value) pendingDelete.value = null
}

async function confirmDelete() {
  if (!pendingDelete.value || deleting.value) return
  deleting.value = true
  deleteError.value = false
  try {
    await generatedImagesAPI.remove(pendingDelete.value.image.id, lifetime.signal)
    if (lifetime.signal.aborted) return
    pendingDelete.value = null
    deleting.value = false
    await reload(Math.min(page.value, Math.max(1, Math.ceil((total.value - 1) / GENERATED_IMAGES_PAGE_SIZE))))
  } catch {
    if (!lifetime.signal.aborted) deleteError.value = true
  } finally {
    deleting.value = false
  }
}

onMounted(() => { void reload() })
onBeforeUnmount(() => lifetime.abort())
</script>
