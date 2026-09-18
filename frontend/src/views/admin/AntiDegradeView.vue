<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-1 flex-wrap items-center justify-between gap-3">
          <div>
            <h1 class="text-lg font-semibold text-gray-900 dark:text-white">防降智</h1>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              管理 Codex state 模式、模型和代理路由
            </p>
          </div>
          <button
            type="button"
            class="btn btn-secondary btn-icon h-10 w-10 !p-0"
            :disabled="loading"
            title="刷新状态"
            aria-label="刷新状态"
            @click="loadOverview"
          >
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <div class="flex min-h-0 flex-1 flex-col overflow-auto">
          <div class="grid grid-cols-2 border-b border-gray-200 dark:border-dark-700 sm:grid-cols-3 xl:grid-cols-6">
            <div class="border-b border-r border-gray-100 p-3 dark:border-dark-800 sm:border-b-0">
              <div class="text-xs text-gray-500 dark:text-dark-400">OpenAI OAuth</div>
              <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ accounts.length }}</div>
            </div>
            <div class="border-b border-gray-100 p-3 dark:border-dark-800 sm:border-b-0 sm:border-r">
              <div class="text-xs text-gray-500 dark:text-dark-400">Off</div>
              <div class="mt-1 text-xl font-semibold text-gray-700 dark:text-dark-200">{{ modeCounts.off }}</div>
            </div>
            <div class="border-b border-r border-gray-100 p-3 dark:border-dark-800 xl:border-b-0">
              <div class="text-xs text-gray-500 dark:text-dark-400">Observe</div>
              <div class="mt-1 text-xl font-semibold text-sky-600 dark:text-sky-400">{{ modeCounts.observe }}</div>
            </div>
            <div class="border-b border-gray-100 p-3 dark:border-dark-800 xl:border-b-0 xl:border-r">
              <div class="text-xs text-gray-500 dark:text-dark-400">Manual</div>
              <div class="mt-1 text-xl font-semibold text-amber-600 dark:text-amber-400">{{ modeCounts.manual }}</div>
            </div>
            <div class="border-r border-gray-100 p-3 dark:border-dark-800 xl:border-r">
              <div class="text-xs text-gray-500 dark:text-dark-400">Auto</div>
              <div class="mt-1 text-xl font-semibold text-emerald-600 dark:text-emerald-400">{{ modeCounts.auto }}</div>
            </div>
            <div class="p-3">
              <div class="text-xs text-gray-500 dark:text-dark-400">当前 state</div>
              <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ validStateCount }}</div>
            </div>
          </div>

          <div v-if="loading && accounts.length === 0" class="flex flex-1 items-center justify-center py-20 text-gray-500 dark:text-dark-400">
            <Icon name="refresh" size="lg" class="mr-2 animate-spin" /> 加载中
          </div>

          <div v-else class="table-wrapper flex-1">
            <table class="min-w-full table-fixed divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800">
                <tr class="text-left text-xs font-medium text-gray-500 dark:text-dark-400">
                  <th class="min-w-40 px-4 py-3">账号</th>
                  <th class="min-w-24 px-4 py-3">模式</th>
                  <th class="min-w-40 px-4 py-3">托管模型</th>
                  <th class="min-w-48 px-4 py-3">代理</th>
                  <th class="min-w-96 px-4 py-3">Fernet state 元数据</th>
                  <th class="w-16 px-4 py-3 text-right">配置</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-800 dark:bg-dark-900">
                <tr v-for="account in accounts" :key="account.id">
                  <td class="px-4 py-3 align-top">
                    <div class="font-medium text-gray-900 dark:text-white">{{ account.name }}</div>
                    <div class="mt-1 flex flex-wrap items-center gap-2 text-xs">
                      <span class="text-gray-400">#{{ account.id }}</span>
                      <span :class="account.status === 'active' ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-400 dark:text-dark-400'">
                        {{ account.status }}
                      </span>
                      <span
                        v-if="account.degraded"
                        class="rounded bg-rose-100 px-1.5 py-0.5 font-medium text-rose-700 dark:bg-rose-950 dark:text-rose-300"
                      >
                        降智
                      </span>
                    </div>
                  </td>
                  <td class="px-4 py-3 align-top">
                    <span
                      class="inline-flex rounded px-2 py-1 text-xs font-medium"
                      :class="modeBadgeClass(account.mode)"
                      :title="modeHint(account.mode)"
                    >
                      {{ modeLabel(account.mode) }}
                    </span>
                  </td>
                  <td class="px-4 py-3 align-top">
                    <div class="flex flex-wrap gap-1.5">
                      <span
                        v-for="model in managedModels(account)"
                        :key="model"
                        class="max-w-44 truncate rounded bg-primary-50 px-2 py-1 font-mono text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                        :title="model"
                      >
                        {{ model }}
                      </span>
                    </div>
                    <p v-if="account.configured_models.length === 0" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                      后端默认模型
                    </p>
                  </td>
                  <td class="px-4 py-3 align-top text-xs leading-5 text-gray-700 dark:text-dark-300">
                    <div>
                      <span class="text-gray-500 dark:text-dark-400">正常：</span>
                      <span>{{ proxyLabel(account.normal_proxy_id) }}</span>
                    </div>
                    <div class="mt-1">
                      <span class="text-gray-500 dark:text-dark-400">捕获：</span>
                      <template v-if="account.state_proxy_ids.length">
                        <span v-for="proxyID in account.state_proxy_ids" :key="proxyID" class="mr-1 inline-block">
                          {{ proxyLabel(proxyID) }}
                        </span>
                      </template>
                      <span v-else>跟随正常调用</span>
                    </div>
                  </td>
                  <td class="px-4 py-3 align-top">
                    <div v-if="account.models.length === 0" class="text-xs text-gray-400 dark:text-dark-400">暂无已捕获 state</div>
                    <div
                      v-for="model in account.models"
                      :key="model.model"
                      class="border-b border-gray-100 py-3 first:pt-0 last:border-b-0 last:pb-0 dark:border-dark-800"
                    >
                      <div class="flex flex-wrap items-center gap-2">
                        <span class="font-medium text-gray-800 dark:text-dark-200">{{ model.model }}</span>
                        <span v-if="model.degraded" class="text-xs text-rose-600 dark:text-rose-400">312</span>
                        <span v-else-if="model.current" class="text-xs text-emerald-600 dark:text-emerald-400">292</span>
                        <button
                          v-if="canCapture(account.mode)"
                          type="button"
                          class="btn btn-primary h-8 min-w-24 whitespace-nowrap !px-3 !py-0 text-xs"
                          :data-testid="captureTestId(account, model)"
                          :disabled="isCapturePending(account, model) || model.mint_run.running"
                          :title="`为 ${model.model} 执行一次捕获`"
                          @click="triggerCapture(account, model)"
                        >
                          <Icon v-if="isCapturePending(account, model) || model.mint_run.running" name="refresh" size="xs" class="animate-spin" />
                          <Icon v-else name="play" size="xs" />
                          捕获一次
                        </button>
                      </div>

                      <p
                        class="mt-1 text-xs"
                        :class="model.mint_run.running ? 'text-primary-600 dark:text-primary-400' : 'text-gray-500 dark:text-dark-400'"
                      >
                        {{ captureStatusText(model) }}
                        <span v-if="model.mint_run.last_attempt_at"> · 最近尝试 {{ formatTime(model.mint_run.last_attempt_at) }}</span>
                      </p>

                      <div v-if="model.current" class="mt-2 space-y-1 text-xs text-gray-600 dark:text-dark-300">
                        <div class="flex flex-wrap gap-x-3 gap-y-1">
                          <span>长度 {{ model.current.decoded_length }}</span>
                          <span>分块 {{ model.current.ciphertext_blocks }}</span>
                          <span>签发 {{ formatTime(model.current.issued_at) }}</span>
                          <span>{{ ageText(model.current.issued_at) }}</span>
                        </div>
                        <div class="flex flex-wrap gap-x-3 gap-y-1">
                          <span>摘要 <span class="font-mono text-gray-800 dark:text-dark-100">{{ model.current.digest }}</span></span>
                          <span>代理 {{ proxyLabel(model.current.proxy_id) }}</span>
                        </div>
                        <p v-if="account.mode === 'auto'" class="text-gray-500 dark:text-dark-400">
                          计划轮换 {{ plannedRotationText(account, model.current) }}
                        </p>
                      </div>
                      <p v-else-if="account.mode === 'auto'" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                        计划轮换：等待首次捕获
                      </p>
                      <p v-if="model.previous" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                        上一份摘要 <span class="font-mono">{{ model.previous.digest }}</span> · 长度 {{ model.previous.decoded_length }} · 分块 {{ model.previous.ciphertext_blocks }}
                      </p>
                    </div>
                  </td>
                  <td class="px-4 py-3 align-top text-right">
                    <button
                      type="button"
                      class="btn btn-secondary btn-icon h-8 w-8 !p-0"
                      :data-testid="`codex-state-config-${account.id}`"
                      title="配置 Codex state"
                      aria-label="配置 Codex state"
                      @click="openConfig(account)"
                    >
                      <Icon name="cog" size="sm" />
                    </button>
                  </td>
                </tr>
                <tr v-if="accounts.length === 0">
                  <td colspan="6" class="px-4 py-16 text-center text-gray-500 dark:text-dark-400">没有 OpenAI OAuth 账号</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </TablePageLayout>

    <BaseDialog :show="showConfig" title="Codex state 配置" width="wide" @close="showConfig = false">
      <div v-if="editingAccount" class="space-y-5">
        <div>
          <div class="text-sm font-medium text-gray-900 dark:text-white">{{ editingAccount.name }}</div>
          <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">账号 #{{ editingAccount.id }}</div>
        </div>

        <div>
          <div class="flex items-center">
            <label class="input-label mb-1.5">运行模式</label>
            <HelpTooltip content="Off 和 Observe 不会提供捕获或注入。Manual 仅在明确点击时捕获一次；Auto 在到期前按设置轮换。" width-class="w-72" />
          </div>
          <div class="grid grid-cols-2 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700 sm:grid-cols-4" role="radiogroup" aria-label="Codex state 运行模式">
            <button
              v-for="option in modeOptions"
              :key="option.value"
              type="button"
              role="radio"
              class="min-h-12 rounded-md px-3 py-2 text-sm font-medium transition-colors"
              :data-testid="`codex-state-mode-${option.value}`"
              :aria-checked="form.mode === option.value"
              :class="modeSegmentClass(option.value)"
              @click="form.mode = option.value"
            >
              {{ option.label }}
            </button>
          </div>
          <p class="input-hint">{{ modeHint(form.mode) }}</p>
        </div>

        <div>
          <div class="flex items-center">
            <label class="input-label mb-1.5">托管模型</label>
            <HelpTooltip content="每个模型可单独执行一次捕获。清空后保存会使用后端默认模型。" width-class="w-64" />
          </div>
          <ModelTagInput
            :models="form.models"
            platform="openai"
            :placeholder="`默认 ${defaultModel}`"
            @update:models="form.models = $event"
          />
        </div>

        <div class="grid grid-cols-1 gap-5 md:grid-cols-2">
          <div>
            <label class="input-label">正常调用代理</label>
            <Select v-model="form.normalProxyID" :options="normalProxyOptions" />
            <p class="input-hint">用于账号的常规请求。</p>
          </div>

          <div>
            <label class="input-label">捕获代理</label>
            <div class="max-h-56 space-y-2 overflow-y-auto rounded-lg border border-gray-200 p-3 dark:border-dark-700">
              <label v-for="proxy in proxies" :key="proxy.id" class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-300">
                <input v-model="form.stateProxyIDs" type="checkbox" :value="proxy.id" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                <span class="min-w-0 truncate">{{ proxyLabel(proxy.id) }}</span>
              </label>
              <div v-if="proxies.length === 0" class="text-sm text-gray-500 dark:text-dark-400">暂无可用代理</div>
            </div>
            <p class="input-hint">为空时跟随正常调用代理。</p>
          </div>
        </div>

        <div v-if="form.mode === 'auto'">
          <label class="input-label" for="codex-state-refresh-before-minutes">提前轮换分钟数</label>
          <input
            id="codex-state-refresh-before-minutes"
            v-model.number="form.refreshBeforeMinutes"
            data-testid="codex-state-refresh-before-minutes"
            type="number"
            min="1"
            max="60"
            class="input w-full"
          />
          <p class="input-hint">state 到期前开始轮换。</p>
        </div>
      </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="showConfig = false">取消</button>
        <button type="button" class="btn btn-primary" data-testid="codex-state-save" :disabled="saving" @click="saveConfig">
          <Icon v-if="saving" name="refresh" size="sm" class="animate-spin" />
          保存
        </button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Select from '@/components/common/Select.vue'
import ModelTagInput from '@/components/admin/channel/ModelTagInput.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  codexStateAPI,
  type CodexStateAccount,
  type CodexStateMode,
  type CodexStateModelStatus,
  type CodexStateProxy,
  type CodexStateStateMeta,
  type UpdateCodexStateAccountRequest,
} from '@/api/admin/codexState'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const appStore = useAppStore()
const loading = ref(false)
const saving = ref(false)
const accounts = ref<CodexStateAccount[]>([])
const proxies = ref<CodexStateProxy[]>([])
const defaultModel = ref('gpt-6-astra')
const showConfig = ref(false)
const editingAccount = ref<CodexStateAccount | null>(null)
const captureKeys = ref(new Set<string>())
const form = reactive({
  mode: 'off' as CodexStateMode,
  models: [] as string[],
  refreshBeforeMinutes: 10,
  normalProxyID: 0,
  stateProxyIDs: [] as number[],
})
let refreshTimer: ReturnType<typeof setInterval> | undefined

const modeOptions: Array<{ value: CodexStateMode; label: string }> = [
  { value: 'off', label: 'Off' },
  { value: 'observe', label: 'Observe' },
  { value: 'manual', label: 'Manual' },
  { value: 'auto', label: 'Auto' },
]

const modeCounts = computed<Record<CodexStateMode, number>>(() => {
  const counts: Record<CodexStateMode, number> = { off: 0, observe: 0, manual: 0, auto: 0 }
  for (const account of accounts.value) counts[account.mode] += 1
  return counts
})

const validStateCount = computed(() => accounts.value.reduce(
  (count, account) => count + account.models.filter(model => model.current).length,
  0,
))

const normalProxyOptions = computed(() => [
  { value: 0, label: '直连' },
  ...proxies.value.map(proxy => ({ value: proxy.id, label: proxyLabel(proxy.id) })),
])

function modeLabel(mode: CodexStateMode) {
  return modeOptions.find(option => option.value === mode)?.label ?? mode
}

function modeBadgeClass(mode: CodexStateMode) {
  const classes: Record<CodexStateMode, string> = {
    off: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300',
    observe: 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-300',
    manual: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
    auto: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
  }
  return classes[mode]
}

function modeSegmentClass(mode: CodexStateMode) {
  return form.mode === mode
    ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-800 dark:text-white'
    : 'text-gray-500 hover:bg-gray-50 hover:text-gray-900 dark:text-dark-400 dark:hover:bg-dark-600 dark:hover:text-white'
}

function modeHint(mode: CodexStateMode) {
  const hints: Record<CodexStateMode, string> = {
    off: '关闭捕获和注入，已有 state 会被清理。',
    observe: '仅观察状态，不提供捕获或注入。',
    manual: '仅在点击模型旁的“捕获一次”后执行一次捕获。',
    auto: '允许手动捕获，并在到期前按设置轮换。',
  }
  return hints[mode]
}

function canCapture(mode: CodexStateMode) {
  return mode === 'manual' || mode === 'auto'
}

function managedModels(account: CodexStateAccount) {
  return account.configured_models.length > 0 ? account.configured_models : [defaultModel.value]
}

function proxyLabel(id?: number) {
  if (!id) return '直连'
  const proxy = proxies.value.find(item => item.id === id)
  return proxy ? `#${proxy.id} ${proxy.name} (${proxy.host}:${proxy.port})` : `#${id}`
}

function formatTime(value?: string) {
  if (!value) return '未知时间'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '未知时间'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(date)
}

function relativeTime(value: string | Date) {
  const time = value instanceof Date ? value.getTime() : new Date(value).getTime()
  if (Number.isNaN(time)) return '时间未知'
  const difference = time - Date.now()
  const future = difference >= 0
  const minutes = Math.max(1, Math.round(Math.abs(difference) / 60000))
  if (minutes < 60) return future ? `${minutes} 分钟后` : `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return future ? `${hours} 小时后` : `${hours} 小时前`
  const days = Math.floor(hours / 24)
  return future ? `${days} 天后` : `${days} 天前`
}

function ageText(value?: string) {
  return value ? `已签发 ${relativeTime(value)}` : '签发时间未知'
}

function plannedRotationText(account: CodexStateAccount, state: CodexStateStateMeta) {
  const expiresAt = new Date(state.expires_at)
  if (Number.isNaN(expiresAt.getTime())) return '时间未知'
  const rotationAt = new Date(expiresAt.getTime() - account.refresh_before_minutes * 60_000)
  return `${formatTime(rotationAt.toISOString())} · ${relativeTime(rotationAt)}`
}

function captureStatusText(model: CodexStateModelStatus) {
  if (model.mint_run.running) return `捕获中 · 第 ${model.mint_run.attempts + 1} 次`
  if (model.mint_run.last_error) return '最近一次捕获失败'
  if (model.current) return `已捕获 · ${relativeTime(model.current.acquired_at)}`
  return '尚未捕获'
}

function captureKey(account: CodexStateAccount, model: CodexStateModelStatus) {
  return `${account.id}:${model.model}`
}

function captureTestId(account: CodexStateAccount, model: CodexStateModelStatus) {
  return `codex-state-capture-${account.id}-${model.model}`
}

function isCapturePending(account: CodexStateAccount, model: CodexStateModelStatus) {
  return captureKeys.value.has(captureKey(account, model))
}

function normalizeModels(models: string[]) {
  const normalized = [...new Set(models.map(model => model.trim()).filter(Boolean))]
  return normalized.length > 0 ? normalized : [defaultModel.value]
}

async function loadOverview() {
  loading.value = true
  try {
    const overview = await codexStateAPI.getOverview()
    accounts.value = overview.accounts
    proxies.value = overview.proxies
    defaultModel.value = overview.default_model || defaultModel.value
    if (editingAccount.value) {
      editingAccount.value = accounts.value.find(account => account.id === editingAccount.value?.id) || null
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '加载防降智状态失败'))
  } finally {
    loading.value = false
  }
}

function openConfig(account: CodexStateAccount) {
  editingAccount.value = account
  form.mode = account.mode
  form.models = account.configured_models.length > 0 ? [...account.configured_models] : [defaultModel.value]
  form.refreshBeforeMinutes = account.refresh_before_minutes || 10
  form.normalProxyID = account.normal_proxy_id || 0
  form.stateProxyIDs = [...account.state_proxy_ids]
  showConfig.value = true
}

async function saveConfig() {
  if (!editingAccount.value) return
  saving.value = true
  try {
    const payload: UpdateCodexStateAccountRequest = {
      mode: form.mode,
      models: normalizeModels(form.models),
      normal_proxy_id: form.normalProxyID,
      state_proxy_ids: [...form.stateProxyIDs],
    }
    if (form.mode === 'auto') payload.refresh_before_minutes = form.refreshBeforeMinutes

    await codexStateAPI.updateAccount(editingAccount.value.id, payload)
    showConfig.value = false
    await loadOverview()
    appStore.showSuccess('Codex state 配置已保存')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '保存 Codex state 配置失败'))
  } finally {
    saving.value = false
  }
}

async function triggerCapture(account: CodexStateAccount, model: CodexStateModelStatus) {
  if (!canCapture(account.mode) || model.mint_run.running || isCapturePending(account, model)) return
  const key = captureKey(account, model)
  captureKeys.value = new Set(captureKeys.value).add(key)
  try {
    await codexStateAPI.triggerMint(account.id, model.model)
    appStore.showSuccess('已启动一次 Codex state 捕获')
    await loadOverview()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, '启动 Codex state 捕获失败'))
  } finally {
    const pending = new Set(captureKeys.value)
    pending.delete(key)
    captureKeys.value = pending
  }
}

onMounted(() => {
  void loadOverview()
  refreshTimer = setInterval(loadOverview, 10000)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>
