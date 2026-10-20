<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '@/api'
import { useI18n } from '@/i18n'
import {
  createManagementKeyClient,
  expiresAtUTC,
  keyStatus,
  managementScopes,
  scopeTemplates,
  useManagementKeys,
  type ManagementKey,
  type ManagementScope,
  type ScopeTemplate,
} from '@/managementKeys'
import { managementKeyMessages } from '@/managementKeyMessages'
import DetailDialog from './DetailDialog.vue'
import UiIcon from './UiIcon.vue'

const props = defineProps<{ consolePasswordSession: boolean }>()
const { locale } = useI18n()
const text = computed(() => managementKeyMessages(locale.value))
const state = useManagementKeys({
  client: createManagementKeyClient(),
  session: () => api.session(),
  passwordSession: () => props.consolePasswordSession,
})
const {
  items,
  allowed,
  loading,
  creating,
  revoking,
  busy,
  error,
  notice,
  createOpen,
  revokeTarget,
  token,
  copied,
  confirmedRevocations,
  writeUncertain,
} = state
const name = ref('')
const template = ref<ScopeTemplate | 'custom'>('readonly')
const selectedScopes = ref<ManagementScope[]>([...scopeTemplates.readonly])
const expiry = ref('30')
const localExpiry = ref('')
const formError = ref('')
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
const errorMessage = computed(() => {
  const messages: Record<string, string> = text.value.errors
  return error.value ? (messages[error.value] ?? text.value.errors.REQUEST_FAILED) : ''
})

watch(template, (value) => {
  if (value !== 'custom') selectedScopes.value = [...scopeTemplates[value]]
})
watch(
  () => props.consolePasswordSession,
  (value) => {
    if (!value) state.deny()
    else void state.refresh()
  },
)

function openCreate() {
  name.value = ''
  template.value = 'readonly'
  selectedScopes.value = [...scopeTemplates.readonly]
  expiry.value = '30'
  localExpiry.value = ''
  formError.value = ''
  state.openCreate()
}
async function submitCreate() {
  formError.value = ''
  if (!name.value.trim() || selectedScopes.value.length === 0) {
    formError.value = text.value.nameRequired
    return
  }
  let expiresAt: string | undefined
  try {
    if (expiry.value === 'custom') {
      if (!localExpiry.value) throw new Error('Missing expiration')
      expiresAt = expiresAtUTC(localExpiry.value)
    } else if (expiry.value !== 'never') {
      expiresAt = new Date(Date.now() + Number(expiry.value) * 86400000).toISOString()
    }
  } catch {
    formError.value = text.value.invalidExpiry
    return
  }
  await state.create({
    name: name.value.trim(),
    scopes: [...selectedScopes.value],
    ...(expiresAt === undefined ? {} : { expiresAt }),
  })
}
function status(key: ManagementKey) {
  return confirmedRevocations.value.has(key.id) ? 'revoked' : keyStatus(key, now.value)
}
function dateLabel(value: string) {
  return new Date(value).toLocaleString(locale.value, { dateStyle: 'medium', timeStyle: 'short' })
}
function copyToken() {
  void state.copyToken(async (value) => {
    if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable')
    await navigator.clipboard.writeText(value)
  })
}
function pageHide() {
  state.clearSensitive()
}
function pageShow(event: PageTransitionEvent) {
  if (event.persisted) void state.refresh()
}
onMounted(() => {
  void state.refresh()
  clock = setInterval(() => {
    now.value = Date.now()
  }, 1000)
  window.addEventListener('pagehide', pageHide)
  window.addEventListener('pageshow', pageShow)
})
onBeforeUnmount(() => {
  state.dispose()
  if (clock !== undefined) clearInterval(clock)
  window.removeEventListener('pagehide', pageHide)
  window.removeEventListener('pageshow', pageShow)
})
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col overflow-y-auto">
    <header
      class="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-4 sm:px-6"
    >
      <div>
        <h2 class="text-base font-semibold text-white">{{ text.title }}</h2>
        <p class="mt-1 text-xs text-gray-400">{{ text.description }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="key-button" :disabled="loading || busy" @click="state.refresh">
          <UiIcon
            :name="loading ? 'spinner' : 'refresh'"
            :size="14"
            :class="{ 'animate-spin': loading }"
          />
          {{ text.refresh }}
        </button>
        <button
          type="button"
          class="key-button key-primary"
          :disabled="!allowed || busy || writeUncertain"
          @click="openCreate"
        >
          <UiIcon name="security" :size="14" />
          {{ text.create }}
        </button>
      </div>
    </header>

    <div class="space-y-4 p-4 sm:p-6">
      <div
        v-if="!loading && (!consolePasswordSession || error === 'CONSOLE_PASSWORD_REQUIRED')"
        role="status"
        class="rounded border border-yellow-500/40 bg-panel p-4"
      >
        <h3 class="font-medium text-yellow-400">{{ text.unavailable }}</h3>
        <p class="mt-2 text-sm text-gray-300">{{ text.passwordRequired }}</p>
      </div>
      <div
        v-if="errorMessage"
        role="alert"
        class="rounded border border-red-500/40 bg-panel p-4 text-sm text-red-300"
      >
        {{ errorMessage }}
        <span v-if="writeUncertain" class="mt-1 block">{{ text.retryHint }}</span>
      </div>
      <p v-if="notice" role="status" class="text-sm text-green-300">
        {{ notice === 'created' ? text.created : text.revokedNotice }}
      </p>
      <p
        v-if="loading && items.length === 0"
        role="status"
        class="py-8 text-center text-sm text-gray-400"
      >
        {{ text.loading }}
      </p>
      <div
        v-else-if="allowed && items.length === 0 && !error"
        class="rounded border border-line bg-panel p-8 text-center"
      >
        <UiIcon name="security" :size="24" class="mx-auto mb-3 text-gray-500" />
        <p class="font-medium text-white">{{ text.empty }}</p>
        <p class="mt-2 text-sm text-gray-400">{{ text.emptyDescription }}</p>
      </div>
      <ul v-if="allowed && items.length" class="space-y-3" :aria-busy="loading">
        <li
          v-for="key in items"
          :key="key.id"
          class="min-w-0 rounded border border-line bg-panel p-4"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <h3 class="font-medium break-words text-white">{{ key.name }}</h3>
              <p class="mt-1 font-mono text-xs break-all text-gray-500">{{ key.id }}</p>
            </div>
            <span
              class="rounded border px-2 py-1 text-xs"
              :class="
                status(key) === 'active'
                  ? 'border-green-500/30 text-green-300'
                  : 'border-line text-gray-400'
              "
            >
              {{ text[status(key)] }}
            </span>
            <button
              type="button"
              class="key-button text-red-300"
              :disabled="status(key) === 'revoked' || busy || writeUncertain"
              :aria-label="`${text.revoke} ${key.name}`"
              @click="state.askRevoke(key)"
            >
              {{ revoking === key.id ? text.revoking : text.revoke }}
            </button>
          </div>
          <dl class="mt-4 grid gap-3 text-xs sm:grid-cols-3">
            <div>
              <dt class="text-gray-500">{{ text.createdAt }}</dt>
              <dd class="mt-1 text-gray-300">
                <time :datetime="key.createdAt">{{ dateLabel(key.createdAt) }}</time>
              </dd>
            </div>
            <div>
              <dt class="text-gray-500">{{ text.expiresAt }}</dt>
              <dd class="mt-1 text-gray-300">
                <time v-if="key.expiresAt" :datetime="key.expiresAt">{{
                  dateLabel(key.expiresAt)
                }}</time
                ><span v-else>{{ text.never }}</span>
              </dd>
            </div>
            <div v-if="key.revokedAt">
              <dt class="text-gray-500">{{ text.revokedAt }}</dt>
              <dd class="mt-1 text-gray-300">
                <time :datetime="key.revokedAt">{{ dateLabel(key.revokedAt) }}</time>
              </dd>
            </div>
          </dl>
          <div class="mt-4 flex flex-wrap gap-1.5" :aria-label="text.scopes">
            <span
              v-for="scope in key.scopes"
              :key="scope"
              class="rounded border border-line bg-canvas px-2 py-1 text-xs text-gray-300"
              :title="scope"
              >{{ text.scopeLabels[scope] }}</span
            >
          </div>
        </li>
      </ul>
    </div>

    <DetailDialog
      :open="createOpen"
      :title="text.create"
      :description="text.tokenWarning"
      @close="!creating && (createOpen = false)"
    >
      <form class="space-y-5" @submit.prevent="submitCreate">
        <label class="block text-gray-300"
          >{{ text.name
          }}<input
            v-model="name"
            class="key-input mt-2 w-full"
            required
            autocomplete="off"
            :placeholder="text.namePlaceholder"
            :disabled="creating"
        /></label>
        <label class="block text-gray-300"
          >{{ text.template
          }}<select v-model="template" class="key-input mt-2 w-full" :disabled="creating">
            <option value="readonly">{{ text.readonly }}</option>
            <option value="operator">{{ text.operator }}</option>
            <option value="admin">{{ text.admin }}</option>
            <option value="custom">{{ text.custom }}</option>
          </select></label
        >
        <fieldset :disabled="creating">
          <legend class="text-gray-300">{{ text.scopes }}</legend>
          <p class="mt-1 text-xs text-gray-500">{{ text.scopeHint }}</p>
          <div class="mt-3 grid gap-2 sm:grid-cols-2">
            <label
              v-for="scope in managementScopes"
              :key="scope"
              class="flex items-start gap-2 rounded border border-line p-2 text-gray-300"
            >
              <input
                v-model="selectedScopes"
                type="checkbox"
                :value="scope"
                class="mt-0.5 accent-blue-500"
                @change="template = 'custom'"
              />
              <span class="min-w-0"
                >{{ text.scopeLabels[scope]
                }}<code class="mt-0.5 block text-xs text-gray-500">{{ scope }}</code></span
              >
            </label>
          </div>
        </fieldset>
        <label class="block text-gray-300"
          >{{ text.expiry
          }}<select v-model="expiry" class="key-input mt-2 w-full" :disabled="creating">
            <option value="7">{{ text.days7 }}</option>
            <option value="30">{{ text.days30 }}</option>
            <option value="90">{{ text.days90 }}</option>
            <option value="never">{{ text.never }}</option>
            <option value="custom">{{ text.customExpiry }}</option>
          </select></label
        >
        <label v-if="expiry === 'custom'" class="block text-gray-400"
          ><span class="text-xs">{{ text.localTime }}</span
          ><input
            v-model="localExpiry"
            type="datetime-local"
            class="key-input mt-2 w-full"
            required
            :disabled="creating"
        /></label>
        <p v-if="formError || errorMessage" role="alert" class="text-sm text-red-300">
          {{ formError || errorMessage }}
        </p>
        <div class="flex flex-wrap justify-end gap-2 border-t border-line pt-4">
          <button type="button" class="key-button" :disabled="creating" @click="createOpen = false">
            {{ text.cancel }}
          </button>
          <button
            type="submit"
            class="key-button key-primary"
            :disabled="creating || writeUncertain"
          >
            {{ creating ? text.creating : text.create }}
          </button>
        </div>
      </form>
    </DetailDialog>

    <DetailDialog
      :open="token !== ''"
      :title="text.tokenTitle"
      :description="text.tokenDescription"
      @close="state.closeToken"
    >
      <pre
        class="rounded border border-line bg-canvas p-4 font-mono text-sm whitespace-pre-wrap break-all select-text text-white"
        :aria-label="text.tokenLabel"
        >{{ token }}</pre>
      <p class="mt-3 text-xs text-gray-400">{{ text.clipboardHint }}</p>
      <p v-if="error === 'COPY_FAILED'" role="alert" class="mt-2 text-sm text-red-300">
        {{ errorMessage }}
      </p>
      <div class="mt-5 flex flex-wrap justify-end gap-2">
        <button type="button" class="key-button key-primary" @click="copyToken">
          <UiIcon :name="copied ? 'check' : 'copy'" :size="14" />{{
            copied ? text.copied : text.copy
          }}
        </button>
        <button type="button" class="key-button" @click="state.closeToken">{{ text.close }}</button>
      </div>
    </DetailDialog>

    <DetailDialog
      :open="revokeTarget !== null"
      :title="text.revokeTitle"
      :description="text.revokeDescription"
      @close="!busy && (revokeTarget = null)"
    >
      <p class="font-medium break-words text-white">{{ revokeTarget?.name }}</p>
      <p class="mt-1 font-mono text-xs break-all text-gray-500">{{ revokeTarget?.id }}</p>
      <p v-if="errorMessage" role="alert" class="mt-3 text-sm text-red-300">{{ errorMessage }}</p>
      <div class="mt-5 flex flex-wrap justify-end gap-2">
        <button type="button" class="key-button" :disabled="busy" @click="revokeTarget = null">
          {{ text.cancel }}
        </button>
        <button
          type="button"
          class="key-button border-red-500/50 text-red-300"
          :disabled="busy || writeUncertain"
          @click="state.revoke"
        >
          {{ busy ? text.revoking : text.revokeConfirm }}
        </button>
      </div>
    </DetailDialog>
  </section>
</template>

<style scoped>
@reference '../style.css';
.key-button {
  @apply inline-flex items-center justify-center gap-2 rounded border border-line px-3 py-2 text-xs text-gray-300 transition hover:bg-raised disabled:cursor-not-allowed disabled:opacity-50;
}
.key-primary {
  @apply border-blue-600 bg-blue-600 text-white hover:bg-blue-500;
}
.key-input {
  @apply rounded border border-line bg-canvas px-3 py-2 text-sm text-white outline-none focus:border-blue-500 disabled:opacity-50;
}
</style>
