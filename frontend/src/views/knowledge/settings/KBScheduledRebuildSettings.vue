<template>
  <div class="kb-scheduled-rebuild-settings">
    <div class="section-header">
      <h2>{{ $t('kbSettings.scheduledRebuild.title') }}</h2>
      <p class="section-description">{{ $t('kbSettings.scheduledRebuild.description') }}</p>
    </div>

    <div class="settings-group">
      <div class="setting-row setting-row--toggle">
        <div class="setting-info">
          <label>{{ $t('kbSettings.scheduledRebuild.enabledLabel') }}</label>
          <p class="desc">{{ $t('kbSettings.scheduledRebuild.enabledDesc') }}</p>
        </div>
        <t-switch v-model="localConfig.enabled" />
      </div>

      <template v-if="localConfig.enabled">
        <div class="setting-row">
          <div class="setting-info">
            <label>{{ $t('kbSettings.scheduledRebuild.modeLabel') }}</label>
            <p class="desc">{{ $t('kbSettings.scheduledRebuild.modeDesc') }}</p>
          </div>
          <div class="mode-control">
            <t-radio-group v-model="localConfig.mode">
              <t-radio-button value="sync">
                {{ $t('kbSettings.scheduledRebuild.modeOptionSync') }}
              </t-radio-button>
              <t-radio-button value="async">
                {{ $t('kbSettings.scheduledRebuild.modeOptionAsync') }}
              </t-radio-button>
            </t-radio-group>
          </div>
        </div>
        <div class="setting-row setting-row--hint">
          <div class="setting-info" />
          <p
            class="mode-hint"
            :class="{ 'mode-hint--sync': localConfig.mode === 'sync', 'mode-hint--async': localConfig.mode === 'async' }"
          >
            <template v-if="localConfig.mode === 'sync'">
              {{ $t('kbSettings.scheduledRebuild.modeDescSync') }}
            </template>
            <template v-else>
              {{ $t('kbSettings.scheduledRebuild.modeDescAsync') }}
            </template>
          </p>
        </div>

        <div class="setting-row">
          <div class="setting-info">
            <label>{{ $t('kbSettings.scheduledRebuild.intervalLabel') }}</label>
            <p class="desc">{{ $t('kbSettings.scheduledRebuild.intervalDesc') }}</p>
          </div>
          <t-input-number v-model="localConfig.interval_seconds" :min="1" :max="86400" suffix="s" />
        </div>
        <div class="setting-row">
          <div class="setting-info">
            <label>{{ $t('kbSettings.scheduledRebuild.batchLabel') }}</label>
            <p class="desc">{{ $t('kbSettings.scheduledRebuild.batchDesc') }}</p>
          </div>
          <t-input-number v-model="localConfig.batch_size" :min="1" :max="32" />
        </div>
        <div class="setting-row setting-row--statuses">
          <div class="setting-info">
            <label>{{ $t('kbSettings.scheduledRebuild.statusesLabel') }}</label>
            <p class="desc">{{ $t('kbSettings.scheduledRebuild.statusesDesc') }}</p>
          </div>
          <t-select v-model="localConfig.statuses" multiple :min-collapsed-num="3" class="status-select">
            <t-option value="failed" label="failed" />
            <t-option value="uploaded" label="uploaded" />
            <t-option value="cancelled" label="cancelled" />
            <t-option value="pending" label="pending" />
            <t-option value="unprocessed" label="unprocessed" />
          </t-select>
        </div>

        <div class="last-triggered-panel">
          <h3 class="last-triggered-title">
            {{ $t('kbSettings.scheduledRebuild.lastTriggeredTitle') }}
          </h3>
          <template v-if="lastTriggeredName">
            <div class="last-triggered-row">
              <span class="last-triggered-label">
                {{ $t('kbSettings.scheduledRebuild.lastTriggeredName') }}
              </span>
              <span class="last-triggered-value last-triggered-name" :title="lastTriggeredName">
                {{ lastTriggeredName }}
              </span>
            </div>
            <div class="last-triggered-row">
              <span class="last-triggered-label">
                {{ $t('kbSettings.scheduledRebuild.lastTriggeredAt') }}
              </span>
              <span class="last-triggered-value">{{ lastTriggeredAtDisplay }}</span>
            </div>
            <p
              v-if="showInFlightTip"
              class="last-triggered-tip"
              role="alert"
            >
              {{ $t('kbSettings.scheduledRebuild.lastTriggeredInFlightTip') }}
            </p>
          </template>
          <p v-else class="last-triggered-empty">
            {{ $t('kbSettings.scheduledRebuild.lastTriggeredEmpty') }}
          </p>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import type { ScheduledRebuildConfig } from '@/api/knowledge-base'

type RebuildMode = 'sync' | 'async'

const props = withDefaults(defineProps<{ config?: ScheduledRebuildConfig | null }>(), {
  config: null,
})
const emit = defineEmits<{ (event: 'update:config', value: ScheduledRebuildConfig): void }>()

const defaults: ScheduledRebuildConfig = {
  enabled: false,
  interval_seconds: 60,
  batch_size: 1,
  statuses: ['failed'],
  mode: 'sync',
}

function normalizeMode(raw: unknown): RebuildMode {
  return raw === 'async' ? 'async' : 'sync'
}

// Initialise from the incoming prop ONCE. We deliberately do NOT install
// a `watch(() => props.config, ...)` here: this component is the sole
// writer of scheduledRebuildConfig while the modal is open, so any
// prop change that arrives comes from this component's own emit. A
// bidirectional watch with `Object.assign` would create a fresh
// `statuses` array on every fire and re-trigger the emission watcher,
// producing an infinite reactive update loop that freezes the page.
const localConfig = reactive<ScheduledRebuildConfig>({
  ...defaults,
  ...(props.config || {}),
  statuses: [...(props.config?.statuses?.length ? props.config.statuses : defaults.statuses)],
  mode: normalizeMode(props.config?.mode),
})

// Snapshot of the last-triggered fields as read-only computed views of
// the prop. Updating the local config never clears or overwrites
// these — they are owned by the scheduler and the backend.
const lastTriggeredName = computed<string>(
  () => props.config?.last_triggered_knowledge_name || '',
)
const lastTriggeredAtRaw = computed<string>(
  () => props.config?.last_triggered_at || '',
)
const lastTriggeredAtDisplay = computed<string>(() => {
  const raw = lastTriggeredAtRaw.value
  if (!raw) return '—'
  const dt = new Date(raw)
  if (Number.isNaN(dt.getTime())) return raw
  return dt.toLocaleString()
})
// Show the "waiting" tip only when the panel has a name AND we're in
// sync mode (async mode doesn't gate on the previous trigger, so the
// tip would mislead operators about the actual scheduler behaviour).
const showInFlightTip = computed<boolean>(
  () => localConfig.mode === 'sync' && !!lastTriggeredName.value,
)

watch(localConfig, (config) => {
  emit('update:config', {
    enabled: !!config.enabled,
    interval_seconds: Math.max(1, Number(config.interval_seconds) || 60),
    batch_size: Math.min(32, Math.max(1, Number(config.batch_size) || 1)),
    statuses: config.statuses.length ? [...config.statuses] : ['failed'],
    mode: normalizeMode(config.mode),
    // Echo last-triggered fields through so the parent form has the
    // full picture for save (in case the editor opens an existing KB
    // and the user toggles "enabled" without rebuilding the row).
    last_triggered_knowledge_id: props.config?.last_triggered_knowledge_id || '',
    last_triggered_knowledge_name: props.config?.last_triggered_knowledge_name || '',
    last_triggered_at: props.config?.last_triggered_at || null,
  })
}, { deep: true })
</script>

<style scoped lang="less">
.kb-scheduled-rebuild-settings { width: 100%; }
.section-header { margin-bottom: 20px; }
.section-header h2 { margin: 0 0 6px; font-size: 20px; font-weight: 600; }
.section-description, .desc { color: var(--td-text-color-secondary); line-height: 1.5; }
.section-description { margin: 0; font-size: 14px; }
.settings-group { display: flex; flex-direction: column; }
.setting-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; padding: 18px 0; border-bottom: 1px solid var(--td-component-stroke); }
.setting-info { flex: 1; }
.setting-info label { display: block; font-weight: 500; }
.desc { margin: 6px 0 0; font-size: 13px; }
.setting-row > :last-child { flex: 0 0 220px; }
.setting-row--hint {
  padding-top: 4px;
  padding-bottom: 12px;
  border-bottom: 1px dashed var(--td-component-stroke);
  align-items: center;
}
.setting-row--hint > :last-child { flex: 1; }
.mode-control { display: flex; align-items: center; justify-content: flex-end; }
.mode-hint {
  margin: 0;
  padding: 10px 12px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  background-color: var(--td-bg-color-secondarycomponent);
  color: var(--td-text-color-secondary);
  width: 100%;
}
.mode-hint--sync {
  background-color: var(--td-warning-color-1);
  color: var(--td-warning-color);
}
.mode-hint--async {
  background-color: var(--td-brand-color-1);
  color: var(--td-brand-color);
}
.status-select { min-width: 220px; }
.last-triggered-panel {
  margin-top: 18px;
  padding: 16px;
  background-color: var(--td-bg-color-secondarycomponent);
  border-radius: 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.last-triggered-title {
  margin: 0 0 6px;
  font-size: 14px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}
.last-triggered-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 13px;
}
.last-triggered-label {
  color: var(--td-text-color-secondary);
  flex: 0 0 auto;
}
.last-triggered-value {
  color: var(--td-text-color-primary);
  text-align: right;
  max-width: 60%;
  word-break: break-all;
}
.last-triggered-name {
  font-weight: 500;
}
.last-triggered-tip {
  margin: 6px 0 0;
  padding: 8px 10px;
  border-radius: 4px;
  background-color: var(--td-warning-color-1);
  color: var(--td-warning-color);
  font-size: 12px;
  line-height: 1.5;
}
.last-triggered-empty {
  margin: 0;
  font-size: 13px;
  color: var(--td-text-color-placeholder);
}
</style>
