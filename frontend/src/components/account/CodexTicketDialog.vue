<template>
  <BaseDialog :show="show" :title="`${t('admin.accounts.codexTicket.title')} · ${modelLabel}`" width="extra-wide" @close="$emit('close')">
    <div v-if="account" class="space-y-5">
      <div class="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800">
        <div class="space-y-1">
          <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ account.name }} <span class="font-normal text-gray-500">#{{ account.id }}</span></p>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.codexTicket.current') }}: <span data-testid="current-ticket-state" :class="codexTicketCurrentClass(status)">{{ t(`admin.accounts.codexTicket.currentState.${codexTicketCurrentState(status)}`) }}</span><span v-if="status?.expires_at"> · {{ t('admin.accounts.codexTicket.cacheDeadline') }} {{ formatDateTime(status.expires_at) }} {{ timezone }}</span></p>
          <p v-if="status?.harvest_paused" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.accounts.codexTicket.autoPaused') }} {{ status.harvest_resume_at ? formatDateTime(status.harvest_resume_at) : '' }}</p>
          <p v-if="lastSuccess" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.codexTicket.lastSuccess') }}: {{ formatDateTime(lastSuccess.occurred_at) }} {{ timezone }}</p>
        </div>
        <button class="btn btn-primary" :disabled="running || !manualAvailable" :title="!manualAvailable ? unavailableText : undefined" @click="runManual">
          <Icon name="refresh" size="sm" :class="['mr-1.5', running ? 'animate-spin' : '']" />{{ running ? t('admin.accounts.codexTicket.retrying') : t('admin.accounts.codexTicket.retry') }}
        </button>
      </div>

      <div class="space-y-2 rounded-xl border border-primary-200 bg-primary-50 p-3 text-sm dark:border-primary-900 dark:bg-primary-900/20" role="status">
        <p v-if="status?.validation_rejected" class="font-semibold text-red-700 dark:text-red-400">{{ t('admin.accounts.codexTicket.rejectedNotInjected') }}</p>
        <p v-else class="font-semibold">{{ binding?.active && binding.pin ? t('admin.accounts.codexTicket.activePin', { id: binding.pin.attempt_id }) : t('admin.accounts.codexTicket.autoLatest') }}</p>
        <p v-if="binding?.active && binding.pin" class="text-xs">{{ t('admin.accounts.codexTicket.pinExpiry') }} {{ formatDateTime(binding.pin.expires_at) }} · {{ t('admin.accounts.codexTicket.pinCheckPolicy') }}</p>
        <p v-if="binding?.last_check.checked_at" class="text-xs">{{ t('admin.accounts.codexTicket.lastCheck') }} {{ formatDateTime(binding.last_check.checked_at) }} · {{ lastCheckLabel }} · HTTP {{ binding.last_check.http_status || '—' }}</p>
        <p v-else class="text-xs">{{ t('admin.accounts.codexTicket.noCheckYet') }}</p>
        <p v-if="binding?.last_validation" :class="validationClass(binding.last_validation.outcome)">{{ t('admin.accounts.codexTicket.lastValidation') }} #{{ binding.last_validation.attempt_id }} · {{ validationLabel(binding.last_validation.outcome) }} · {{ formatDateTime(binding.last_validation.checked_at) }}</p>
        <button v-if="binding?.pin" class="btn btn-secondary btn-sm" :disabled="pinning || validating" @click="releasePin">{{ t('admin.accounts.codexTicket.releasePin') }}</button>
      </div>

      <p v-if="!manualAvailable && unavailableText" class="text-xs text-amber-600 dark:text-amber-400">{{ unavailableText }}</p>
      <p v-if="feedback" class="rounded-lg border border-primary-200 bg-primary-50 px-3 py-2 text-sm text-primary-800 dark:border-primary-900 dark:bg-primary-900/20 dark:text-primary-200" role="status">{{ feedback }}</p>

      <div class="flex flex-wrap items-center gap-4 rounded-xl border border-gray-200 px-4 py-3 text-xs text-gray-600 dark:border-dark-600 dark:text-gray-300">
        <p class="w-full">{{ t('admin.accounts.codexTicket.optInHint') }}</p>
        <label class="flex items-center gap-2"><input v-model="accountEnabled" type="checkbox" class="rounded text-primary-600" :disabled="savingPolicy" @change="savePolicy" />{{ t('admin.accounts.codexTicket.accountEnabled') }}</label>
        <label class="flex items-center gap-2"><input v-model="modelEnabled" type="checkbox" class="rounded text-primary-600" :disabled="savingPolicy" @change="savePolicy" />{{ t('admin.accounts.codexTicket.modelEnabled') }}</label>
      </div>

      <div class="flex items-center justify-between border-b border-gray-200 dark:border-dark-600">
        <div class="flex gap-5">
          <button v-for="item in tabs" :key="item.key" class="border-b-2 pb-2 text-sm font-medium transition-colors" :class="tab === item.key ? 'border-primary-500 text-primary-700 dark:text-primary-300' : 'border-transparent text-gray-500 hover:text-gray-800 dark:hover:text-gray-200'" @click="tab = item.key">{{ item.label }}</button>
        </div>
        <button class="mb-2 rounded-lg p-1.5 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700" :title="t('common.refresh')" @click="load"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" /></button>
      </div>

      <div v-if="tab === 'success'" class="space-y-2 rounded-xl border border-gray-200 p-3 text-sm dark:border-dark-600">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span>{{ selectedAttemptId === null ? t(binding?.active ? 'admin.accounts.codexTicket.currentPinnedDefault' : 'admin.accounts.codexTicket.latestDefault') : t('admin.accounts.codexTicket.selectedRecord', { id: selectedAttemptId }) }}</span>
          <div class="flex gap-2">
            <button v-if="selectedAttemptId !== null" class="btn btn-secondary btn-sm" :disabled="validating" @click="selectedAttemptId = null">{{ t('admin.accounts.codexTicket.useLatest') }}</button>
            <button class="btn btn-primary btn-sm" :disabled="selectedAttemptId === null || pinning || validating" @click="confirmSelection">{{ pinning ? t('admin.accounts.codexTicket.confirmingPin') : t('admin.accounts.codexTicket.confirmPin') }}</button>
            <button class="btn btn-secondary btn-sm" :disabled="validating || pinning" @click="validateSelected">{{ validating ? t('admin.accounts.codexTicket.validating') : t('admin.accounts.codexTicket.validateTicket') }}</button>
          </div>
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.codexTicket.validationHint') }}</p>
        <p v-if="validationFeedback" role="status" class="whitespace-pre-wrap text-sm text-primary-700 dark:text-primary-300">{{ validationFeedback }}</p>
      </div>

      <div v-if="loadError" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">{{ loadError }}</div>
      <div v-else-if="loading" class="py-10 text-center text-sm text-gray-500">{{ t('admin.accounts.codexTicket.loading') }}</div>
      <div v-else-if="!current.items.length" class="py-10 text-center text-sm text-gray-500">{{ t('admin.accounts.codexTicket.empty') }}</div>
      <div v-else class="divide-y divide-gray-200/80 rounded-xl border border-gray-200 bg-white dark:divide-dark-600 dark:border-dark-600 dark:bg-dark-800">
        <div v-for="(item, index) in current.items" :key="item.id" class="px-4 py-3.5">
          <p v-if="tab === 'success' && (index === 0 || localDate(current.items[index - 1].occurred_at) !== localDate(item.occurred_at))" class="mb-2 text-xs font-semibold text-primary-700 dark:text-primary-300">{{ localDate(item.occurred_at) }}</p>
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex items-center gap-2 text-sm">
              <span class="h-2 w-2 rounded-full" :class="item.outcome === 'success' ? 'bg-emerald-500' : item.outcome === 'miss' ? 'bg-amber-500' : 'bg-red-500'" />
              <span class="font-medium text-gray-900 dark:text-gray-100">{{ t(`admin.accounts.codexTicket.${item.outcome}`) }}</span>
              <span v-if="item.outcome === 'success'" class="rounded px-2 py-0.5 text-xs font-semibold" :class="validationClass(item.validation_outcome)">{{ validationLabel(item.validation_outcome) }}</span>
              <span v-if="item.trigger === 'manual'" class="badge badge-primary text-[10px]">{{ t('admin.accounts.codexTicket.manual') }}</span>
            </div>
            <span v-if="item.validation_checked_at" class="text-xs text-gray-500">{{ t('admin.accounts.codexTicket.validatedAt') }} {{ formatDateTime(item.validation_checked_at) }}</span>
            <time class="text-xs tabular-nums text-gray-500 dark:text-gray-400">{{ formatDateTime(item.occurred_at) }} {{ timezone }}</time>
          </div>
          <div v-if="tab === 'all'" class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            <span>HTTP {{ item.http_status ?? '—' }}</span><span>{{ t('admin.accounts.codexTicket.length') }} {{ item.ticket_length ?? '—' }}</span>
            <span>{{ item.duration_ms }} ms</span><span>{{ t('admin.accounts.codexTicket.proxy') }} {{ item.proxy_name || '—' }}</span>
            <span v-if="item.reason_code" class="text-amber-600 dark:text-amber-400">{{ t('admin.accounts.codexTicket.reason') }}: {{ item.reason_code }}</span>
          </div>
          <p v-else-if="item.proxy_name" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.codexTicket.proxy') }} {{ item.proxy_name }}</p>
          <div v-if="tab === 'success'" class="mt-3 space-y-2">
            <div class="flex flex-wrap items-center gap-4">
              <label class="flex items-center gap-2 text-xs"><input type="checkbox" :aria-label="`${t('admin.accounts.codexTicket.selectTicket')} #${item.id}`" :checked="selectedAttemptId === item.id" :disabled="validating || pinning" @change="selectedAttemptId = selectedAttemptId === item.id ? null : item.id" />{{ t('admin.accounts.codexTicket.selectTicket') }}</label>
              <button class="btn btn-secondary btn-sm" :disabled="revealing" @click="revealTicket(item.id)">{{ revealedTicket?.attempt_id === item.id ? t('admin.accounts.codexTicket.hideTicket') : t('admin.accounts.codexTicket.viewTicket') }}</button>
              <span v-if="item.expires_at" class="text-xs text-gray-500">{{ t('admin.accounts.codexTicket.expires') }} {{ formatDateTime(item.expires_at) }}</span>
            </div>
            <div v-if="revealedTicket?.attempt_id === item.id" class="space-y-2">
              <p class="text-xs text-amber-700 dark:text-amber-400">{{ t('admin.accounts.codexTicket.secretHint') }}</p>
              <textarea readonly :aria-label="t('admin.accounts.codexTicket.ticketContent')" :value="revealedTicket.ticket" rows="4" class="w-full rounded-lg border border-gray-300 bg-gray-50 p-2 font-mono text-xs text-gray-900 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-100" />
            </div>
          </div>
        </div>
      </div>

      <div class="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
        <span>{{ t('admin.accounts.codexTicket.total', { count: current.total }) }}</span>
        <div class="flex items-center gap-3"><button class="btn btn-secondary btn-sm" :disabled="current.page <= 1 || loading" @click="changePage(-1)">‹</button><span>{{ current.page }} / {{ Math.max(1, Math.ceil(current.total / 20)) }}</span><button class="btn btn-secondary btn-sm" :disabled="current.page * 20 >= current.total || loading" @click="changePage(1)">›</button></div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import { codexTicketCurrentState, codexTicketCurrentClass } from '@/utils/codexTicketState'
import * as accountAPI from '@/api/admin/accounts'
import type { AccountListItem } from '@/types'
import type { CodexTicketHistory, CodexTicketAttempt, CodexTicketStatus } from '@/api/admin/accounts'

const props = defineProps<{ show: boolean; account: AccountListItem | null; model: string }>()
const emit = defineEmits<{ close: []; refreshed: [] }>()
const { t } = useI18n()
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
const tab = ref<'all' | 'success'>('all')
const pages = ref({ all: 1, success: 1 })
const data = ref<Record<'all' | 'success', CodexTicketHistory | null>>({ all: null, success: null })
const current = computed(() => data.value[tab.value] ?? { items: [] as CodexTicketAttempt[], total: 0, page: pages.value[tab.value] })
const status = computed<CodexTicketStatus | undefined>(() => data.value[tab.value]?.ticket_status ?? props.account?.codex_turn_tickets?.find(ticket => ticket.model === props.model))
const lastSuccess = ref<CodexTicketAttempt | null>(null)
const manualAvailable = computed(() => data.value[tab.value]?.manual_available ?? false)
const unavailableText = computed(() => {
  const reason = data.value[tab.value]?.manual_unavailable_reason
  return reason === 'no_proxy' ? t('admin.accounts.codexTicket.noProxy') : reason === 'disabled' ? t('admin.accounts.codexTicket.disabled') : ''
})
const modelLabel = computed(() => props.model === 'gpt-6-astra' ? '6 Astra' : '5.6 Sol')
const tabs = computed(() => [{ key: 'all' as const, label: t('admin.accounts.codexTicket.attempts') }, { key: 'success' as const, label: t('admin.accounts.codexTicket.successes') }])
const loading = ref(false)
const running = ref(false)
const savingPolicy = ref(false)
const feedback = ref('')
const loadError = ref('')
const accountEnabled = ref(false)
const modelEnabled = ref(true)
function validationLabel(outcome?: string): string {
  if (!outcome) return t('admin.accounts.codexTicket.notValidated')
  const key = ({ material_missing: 'resultMissing', remote_completed: 'resultPassed', response_model_mismatch: 'resultWrongModel', validation_timeout: 'resultTimeout', transport_error: 'resultNetwork', rate_limited: 'resultRateLimit', response_failed: 'resultFailed', incomplete_response: 'resultIncomplete', upstream_http_error: 'resultHTTP' } as Record<string,string>)[outcome]
  return key ? t(`admin.accounts.codexTicket.${key}`) : `${t('admin.accounts.codexTicket.checkUnknown')} ${outcome}`
}
function validationClass(outcome?: string): string {
  if (outcome === 'remote_completed') return 'text-emerald-700 dark:text-emerald-400'
  if (outcome === 'response_model_mismatch' || outcome === 'response_failed') return 'text-red-700 dark:text-red-400'
  return 'text-amber-700 dark:text-amber-400'
}
const binding = ref<accountAPI.CodexTicketBinding | null>(null)
const pinning = ref(false)
const lastCheckLabel = computed(() => {
  const result = binding.value?.last_check.outcome
  if (result === 'remote_completed') return t('admin.accounts.codexTicket.checkPassed')
  if (result === 'local_recent_usage') return t('admin.accounts.codexTicket.checkLocal')
  return validationLabel(result)
})
let bindingLoaded = false
async function confirmSelection() {
  if (!props.account || selectedAttemptId.value === null || pinning.value) return
  const epoch = materialEpoch
  pinning.value = true
  validationFeedback.value = t('admin.accounts.codexTicket.confirmingPin')
  try {
    const result = await accountAPI.confirmCodexTicketBinding(props.account.id, props.model, selectedAttemptId.value)
    if (epoch !== materialEpoch) return
    validationFeedback.value = result.locked ? t('admin.accounts.codexTicket.pinSaved') : `${t('admin.accounts.codexTicket.pinFailed')} · ${validationLabel(result.outcome)} · HTTP ${result.http_status || '—'}`
    if (result.locked) emit('refreshed')
    await load()
  } catch (error) { if (epoch === materialEpoch) { validationFeedback.value = error instanceof Error ? error.message : String(error); await load() } }
  finally { if (epoch === materialEpoch) pinning.value = false }
}
async function releasePin() {
  if (!props.account || pinning.value) return
  const epoch = materialEpoch
  pinning.value = true
  try {
    await accountAPI.unpinCodexTicket(props.account.id, props.model)
    if (epoch !== materialEpoch) return
    selectedAttemptId.value = null
    validationFeedback.value = t('admin.accounts.codexTicket.pinReleased')
    emit('refreshed'); await load()
  } catch (error) { if (epoch === materialEpoch) validationFeedback.value = error instanceof Error ? error.message : String(error) }
  finally { if (epoch === materialEpoch) pinning.value = false }
}
const selectedAttemptId = ref<number | null>(null)
const revealedTicket = ref<accountAPI.CodexTicketMaterial | null>(null)
const revealing = ref(false)
const validating = ref(false)
const validationFeedback = ref('')
let materialEpoch = 0
async function revealTicket(id: number) {
  if (!props.account || revealing.value) return
  if (revealedTicket.value?.attempt_id === id) { revealedTicket.value = null; return }
  const epoch = materialEpoch
  revealing.value = true
  revealedTicket.value = null
  try {
    const result = await accountAPI.getCodexTicketMaterial(props.account.id, props.model, id)
    if (epoch === materialEpoch) revealedTicket.value = result
  } catch (error) { if (epoch === materialEpoch) validationFeedback.value = error instanceof Error ? error.message : String(error) }
  finally { if (epoch === materialEpoch) revealing.value = false }
}
async function validateSelected() {
  if (!props.account || validating.value) return
  const epoch = materialEpoch
  validating.value = true
  validationFeedback.value = ''
  try {
    const result = await accountAPI.validateCodexTicketMaterial(props.account.id, props.model, selectedAttemptId.value ?? undefined)
    if (epoch === materialEpoch) { validationFeedback.value = `${validationLabel(result.outcome)} · HTTP ${result.http_status || '—'} · ${formatDateTime(result.checked_at)}`; await load() }
  } catch (error) { if (epoch === materialEpoch) { validationFeedback.value = error instanceof Error ? error.message : String(error); await load() } }
  finally { if (epoch === materialEpoch) validating.value = false }
}
let requestVersion = 0

const localDate = (value: string) => new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'long', day: 'numeric' }).format(new Date(value))
async function load() {
  if (!props.show || !props.account || !props.model) return
  const version = ++requestVersion
  const key = tab.value
  loading.value = true
  loadError.value = ''
  try {
    const response = await accountAPI.getCodexTicketHistory(props.account.id, props.model, key, pages.value[key])
    const savedBinding = await accountAPI.getCodexTicketBinding(props.account.id, props.model)
    if (version === requestVersion) {
      binding.value = savedBinding
      accountEnabled.value = savedBinding.enabled
      modelEnabled.value = savedBinding.model_enabled
      if (!bindingLoaded) { selectedAttemptId.value = savedBinding.active ? savedBinding.pin?.attempt_id ?? null : null; bindingLoaded = true }
      data.value = { ...data.value, [key]: response }
      if (key === 'success' && pages.value.success === 1) lastSuccess.value = response.items[0] ?? null
      if (key === 'all' && !data.value.success) {
        const successes = await accountAPI.getCodexTicketHistory(props.account.id, props.model, 'success', 1)
        if (version === requestVersion) {
          data.value = { ...data.value, success: successes }
          lastSuccess.value = successes.items[0] ?? null
        }
      }
    }
  } catch (error) { if (version === requestVersion) loadError.value = error instanceof Error ? error.message : String(error) }
  finally { if (version === requestVersion) loading.value = false }
}
function changePage(delta: number) { pages.value[tab.value] += delta; load() }
async function runManual() {
  if (!props.account || running.value) return
  running.value = true
  feedback.value = ''
  try {
    const result = await accountAPI.harvestCodexTicket(props.account.id, props.model)
    feedback.value = t(`admin.accounts.codexTicket.${result.outcome}`) + (result.history_recorded ? '' : ` · ${t('admin.accounts.codexTicket.historyFailed')}`)
    data.value = { all: null, success: null }
    lastSuccess.value = null
    pages.value = { all: 1, success: 1 }
    emit('refreshed')
    await load()
  } catch (error) { feedback.value = error instanceof Error ? error.message : String(error) }
  finally { running.value = false }
}
async function savePolicy() {
  if (!props.account || savingPolicy.value) return
  savingPolicy.value = true
  try {
    const existing = (props.account.extra?.codex_ticket_harvest_models ?? {}) as Record<string, boolean>
    await accountAPI.setCodexTicketParticipation(props.account.id, accountEnabled.value, { ...existing, [props.model]: modelEnabled.value })
    feedback.value = t('admin.accounts.codexTicket.policySaved')
    emit('refreshed')
    await load()
  } catch (error) { feedback.value = error instanceof Error ? error.message : String(error) }
  finally { savingPolicy.value = false }
}
watch(() => [props.show, props.account?.id, props.model] as const, () => {
  requestVersion++
  materialEpoch++
  binding.value = null
  bindingLoaded = false
  pinning.value = false
  selectedAttemptId.value = null
  revealedTicket.value = null
  validationFeedback.value = ''
  revealing.value = false
  validating.value = false
  if (!props.show || !props.account) return
  data.value = { all: null, success: null }
  lastSuccess.value = null
  pages.value = { all: 1, success: 1 }
  tab.value = 'all'
  feedback.value = ''
  accountEnabled.value = props.account.extra?.codex_ticket_harvest_enabled === true
  const models = props.account.extra?.codex_ticket_harvest_models as Record<string, boolean> | undefined
  modelEnabled.value = models?.[props.model] !== false
  load()
}, { immediate: true })
watch(tab, () => { if (!data.value[tab.value]) load() })
</script>
