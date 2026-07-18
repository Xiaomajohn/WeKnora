<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="visible" class="kb-basic-overlay" @click.self="handleClose">
        <div class="kb-basic-modal">
          <!-- 关闭按钮 -->
          <button class="kb-basic-close" @click="handleClose" :aria-label="$t('general.close')">
            <svg width="20" height="20" viewBox="0 0 20 20" fill="currentColor">
              <path d="M15 5L5 15M5 5L15 15" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
            </svg>
          </button>

          <div class="kb-basic-container">
            <!-- 左侧导航（精简：仅"基本信息"分组） -->
            <div class="kb-basic-sidebar">
              <div class="kb-basic-sidebar-header">
                <h2 class="kb-basic-sidebar-title">{{ $t('knowledgeEditor.titleCreate') }}</h2>
              </div>
              <div class="kb-basic-nav">
                <div class="kb-basic-nav-group-title">
                  {{ $t('knowledgeEditor.navGroups.basic') }}
                </div>
                <div class="kb-basic-nav-item active">
                  <t-icon name="info-circle" class="kb-basic-nav-icon" />
                  <span class="kb-basic-nav-label">{{ $t('knowledgeEditor.basic.title') }}</span>
                </div>
              </div>
            </div>

            <!-- 右侧内容：原"基本信息"section 的全部字段 -->
            <div class="kb-basic-content">
              <div class="kb-basic-content-wrapper">
                <div class="kb-basic-section">
                  <div class="kb-basic-section-header">
                    <h3 class="kb-basic-section-title">{{ $t('knowledgeEditor.basic.title') }}</h3>
                    <p class="kb-basic-section-desc">
                      {{ $t('knowledgeEditor.basic.description') }}
                    </p>
                  </div>
                  <div class="kb-basic-section-body">
                    <!-- 知识库类型（文档 / 问答） -->
                    <div class="kb-basic-form-item">
                      <label class="kb-basic-form-label required">
                        {{ $t('knowledgeEditor.basic.typeLabel') }}
                      </label>
                      <t-radio-group v-model="form.type">
                        <t-radio-button value="document">
                          {{ $t('knowledgeEditor.basic.typeDocument') }}
                        </t-radio-button>
                        <t-radio-button value="faq">
                          {{ $t('knowledgeEditor.basic.typeFAQ') }}
                        </t-radio-button>
                      </t-radio-group>
                      <p class="kb-basic-form-tip">{{ $t('knowledgeEditor.basic.typeDescription') }}</p>
                    </div>

                    <!-- 索引策略（仅文档类型） -->
                    <div v-if="form.type !== 'faq'" class="kb-basic-form-item">
                      <label class="kb-basic-form-label required">
                        {{ $t('knowledgeEditor.indexing.title') }}
                      </label>
                      <p class="kb-basic-form-tip">{{ $t('knowledgeEditor.indexing.description') }}</p>
                      <div class="kb-basic-indexing-checks">
                        <div
                          class="kb-basic-indexing-item"
                          :class="{ 'is-checked': form.indexingStrategy.vectorEnabled }"
                          @click="toggleVectorIndexing"
                        >
                          <t-checkbox
                            :checked="form.indexingStrategy.vectorEnabled"
                            class="kb-basic-indexing-box"
                          >
                            {{ $t('knowledgeEditor.indexing.searchTitle') }}
                          </t-checkbox>
                          <p class="kb-basic-indexing-desc">
                            {{ $t('knowledgeEditor.indexing.searchDesc') }}
                          </p>
                        </div>
                        <div
                          class="kb-basic-indexing-item"
                          :class="{ 'is-checked': form.indexingStrategy.wikiEnabled }"
                          @click="toggleWikiIndexing"
                        >
                          <t-checkbox
                            :checked="form.indexingStrategy.wikiEnabled"
                            class="kb-basic-indexing-box"
                          >
                            <span class="kb-basic-indexing-title">
                              {{ $t('knowledgeEditor.indexing.wikiTitle') }}
                              <span class="kb-basic-indexing-new-badge">NEW</span>
                            </span>
                          </t-checkbox>
                          <p class="kb-basic-indexing-desc">
                            {{ $t('knowledgeEditor.indexing.wikiDesc') }}
                          </p>
                        </div>
                      </div>
                    </div>

                    <!-- 知识库名称 -->
                    <div class="kb-basic-form-item">
                      <label class="kb-basic-form-label required">
                        {{ $t('knowledgeEditor.basic.nameLabel') }}
                      </label>
                      <t-input
                        v-model="form.name"
                        :placeholder="$t('knowledgeEditor.basic.namePlaceholder')"
                        :maxlength="50"
                        :status="errors.name ? 'error' : 'default'"
                        data-guide="kb-basic-name"
                        @blur="validateName"
                      />
                      <p
                        v-if="errors.name"
                        class="kb-basic-form-tip"
                        style="color: var(--td-error-color)"
                      >
                        {{ errors.name }}
                      </p>
                    </div>

                    <!-- 知识库描述 -->
                    <div class="kb-basic-form-item">
                      <label class="kb-basic-form-label">
                        {{ $t('knowledgeEditor.basic.descriptionLabel') }}
                      </label>
                      <t-textarea
                        v-model="form.description"
                        :placeholder="$t('knowledgeEditor.basic.descriptionPlaceholder')"
                        :maxlength="200"
                        :autosize="{ minRows: 3, maxRows: 6 }"
                      />
                    </div>

                    <div class="kb-basic-form-tip" style="margin-top: 16px">
                      {{ $t('knowledgeEditor.basic.basicCreateTip') }}
                    </div>
                  </div>
                </div>
              </div>

              <!-- 操作按钮 -->
              <div class="kb-basic-footer">
                <t-button theme="default" variant="outline" @click="handleClose">
                  {{ $t('common.cancel') }}
                </t-button>
                <t-button theme="primary" :loading="saving" @click="handleSubmit">
                  {{ $t('knowledgeEditor.buttons.create') }}
                </t-button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { createKnowledgeBase } from '@/api/knowledge-base'
import { useI18n } from 'vue-i18n'

interface Props {
  visible: boolean
  kbType?: 'document' | 'faq'
}

const props = withDefaults(defineProps<Props>(), {
  kbType: 'document',
})

const emit = defineEmits<{
  'update:visible': [value: boolean]
  'success': [kbId: string]
}>()

const { t } = useI18n()

// 表单数据：保留原"基本信息"section 的所有字段（KB 类型 + 索引策略 + 名称 + 描述）。
// 其余高级配置（模型、向量库、分块、Wiki 指令等）由后端默认值填充。
const form = reactive({
  type: props.kbType,
  name: '',
  description: '',
  indexingStrategy: {
    vectorEnabled: true,
    keywordEnabled: true,
    wikiEnabled: false,
    graphEnabled: false,
  },
})

const errors = reactive({
  name: '',
})

const saving = ref(false)

const resetForm = () => {
  form.type = props.kbType
  form.name = ''
  form.description = ''
  form.indexingStrategy.vectorEnabled = true
  form.indexingStrategy.keywordEnabled = true
  form.indexingStrategy.wikiEnabled = false
  form.indexingStrategy.graphEnabled = false
  errors.name = ''
  saving.value = false
}

const validateName = () => {
  if (!form.name || !form.name.trim()) {
    errors.name = t('knowledgeEditor.messages.nameRequired')
    return false
  }
  errors.name = ''
  return true
}

// 索引策略开关 — 与原 modal 行为一致：开启 RAG 会同步开启 keyword，关闭则同步关闭
const toggleVectorIndexing = () => {
  const next = !form.indexingStrategy.vectorEnabled
  form.indexingStrategy.vectorEnabled = next
  form.indexingStrategy.keywordEnabled = next
}

const toggleWikiIndexing = () => {
  form.indexingStrategy.wikiEnabled = !form.indexingStrategy.wikiEnabled
}

const handleClose = () => {
  emit('update:visible', false)
  setTimeout(resetForm, 300)
}

const handleSubmit = async () => {
  // 校验
  if (!validateName()) {
    MessagePlugin.warning(errors.name)
    return
  }
  if (form.type !== 'faq') {
    const s = form.indexingStrategy
    if (!s.vectorEnabled && !s.keywordEnabled && !s.wikiEnabled) {
      MessagePlugin.warning(t('knowledgeEditor.indexing.atLeastOne'))
      return
    }
  }

  saving.value = true
  try {
    // 链接用户精简创建：传最小字段集，模型 / 向量库 / 分块等由后端默认值填充。
    const payload: any = {
      name: form.name.trim(),
      description: form.description.trim() || undefined,
      type: form.type,
    }
    if (form.type !== 'faq') {
      payload.indexing_strategy = {
        vector_enabled: form.indexingStrategy.vectorEnabled,
        keyword_enabled: form.indexingStrategy.keywordEnabled,
        wiki_enabled: form.indexingStrategy.wikiEnabled,
        graph_enabled: form.indexingStrategy.graphEnabled,
      }
    }

    const result: any = await createKnowledgeBase(payload)
    if (!result?.success || !result?.data?.id) {
      throw new Error(result?.message || t('knowledgeEditor.messages.createFailed'))
    }
    MessagePlugin.success(t('knowledgeEditor.messages.createSuccess'))
    emit('success', result.data.id)
    handleClose()
  } catch (error: any) {
    console.error('KBCreateBasic submit failed:', error)
    MessagePlugin.error(
      error?.message || t('knowledgeEditor.messages.createFailed'),
    )
  } finally {
    saving.value = false
  }
}

watch(
  () => props.visible,
  (newVal) => {
    if (newVal) {
      resetForm()
    }
  },
)
</script>

<style lang="less" scoped>
.kb-basic-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  backdrop-filter: blur(4px);
}

.kb-basic-modal {
  position: relative;
  width: 90vw;
  max-width: 720px;
  height: auto;
  min-height: 480px;
  max-height: 85vh;
  background: var(--td-bg-color-container);
  border-radius: 12px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.12);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.kb-basic-close {
  position: absolute;
  top: 20px;
  right: 20px;
  width: 32px;
  height: 32px;
  border: none;
  background: var(--td-bg-color-secondarycontainer);
  border-radius: 6px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--td-text-color-secondary);
  transition: all 0.2s ease;
  z-index: 10;

  &:hover {
    color: var(--td-text-color-primary);
  }
}

.kb-basic-container {
  display: flex;
  height: 100%;
  width: 100%;
  overflow: hidden;
}

.kb-basic-sidebar {
  width: 208px;
  background-color: var(--td-bg-color-settings-modal);
  border-right: 1px solid var(--td-component-stroke);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
}

.kb-basic-sidebar-header {
  padding: 16px 14px 12px;
  border-bottom: 1px solid var(--td-component-stroke);
  flex-shrink: 0;
}

.kb-basic-sidebar-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.kb-basic-nav {
  padding: 8px;
}

.kb-basic-nav-group-title {
  padding: 6px 12px 2px;
  color: var(--td-text-color-placeholder);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.kb-basic-nav-item {
  display: flex;
  align-items: center;
  padding: 6px 12px;
  margin-bottom: 2px;
  border-radius: 6px;
  font-size: 14px;
  color: var(--td-text-color-primary);
  user-select: none;

  &.active {
    background-color: var(--td-bg-color-secondarycontainer);
    color: var(--td-brand-color);
    font-weight: 500;
  }
}

.kb-basic-nav-icon {
  margin-right: 9px;
  font-size: 16px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: inherit;
}

.kb-basic-nav-label {
  flex: 1;
}

.kb-basic-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.kb-basic-content-wrapper {
  flex: 1;
  overflow-y: auto;
  padding: 24px 32px;
}

.kb-basic-section {
  margin-bottom: 32px;

  &:last-child {
    margin-bottom: 0;
  }
}

.kb-basic-section-header {
  margin-bottom: 16px;
}

.kb-basic-section-title {
  margin: 0 0 6px 0;
  font-family: var(--app-font-family);
  font-size: 20px;
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.kb-basic-section-desc {
  margin: 0;
  font-family: var(--app-font-family);
  font-size: 14px;
  color: var(--td-text-color-placeholder);
  line-height: 22px;
}

.kb-basic-section-body {
  background: var(--td-bg-color-container);
}

.kb-basic-form-item {
  margin-bottom: 16px;

  &:last-child {
    margin-bottom: 0;
  }
}

.kb-basic-form-label {
  display: block;
  margin-bottom: 8px;
  font-family: var(--app-font-family);
  font-size: 15px;
  font-weight: 500;
  color: var(--td-text-color-primary);

  &.required::after {
    content: '*';
    color: var(--td-error-color);
    margin-left: 4px;
  }
}

.kb-basic-form-tip {
  margin-top: 6px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
  line-height: 1.5;
}

// 索引策略卡片样式（复用原 modal 的视觉）
.kb-basic-indexing-checks {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 12px;
  margin-top: 10px;
}

.kb-basic-indexing-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 14px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 8px;
  background: var(--td-bg-color-container);
  cursor: pointer;
  user-select: none;
  transition: border-color 0.2s ease, background 0.2s ease;

  &:hover {
    border-color: var(--td-brand-color);
  }

  &.is-checked {
    border-color: var(--td-brand-color);
    background: var(--td-brand-color-light);
  }
}

.kb-basic-indexing-box {
  pointer-events: none;
}

.kb-basic-indexing-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.kb-basic-indexing-new-badge {
  display: inline-flex;
  align-items: center;
  padding: 0 6px;
  height: 16px;
  border-radius: 3px;
  font-size: 10px;
  font-weight: 600;
  line-height: 1;
  letter-spacing: 0.4px;
  color: var(--td-brand-color);
  background: var(--td-brand-color-light);
}

.kb-basic-indexing-desc {
  margin: 0;
  padding-left: 24px;
  font-size: 12px;
  line-height: 18px;
  color: var(--td-text-color-placeholder);
}

.kb-basic-footer {
  padding: 16px 32px;
  border-top: 1px solid var(--td-component-stroke);
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  flex-shrink: 0;
}

// 过渡动画
.modal-enter-active,
.modal-leave-active {
  transition: all 0.3s ease;
}

.modal-enter-from,
.modal-leave-to {
  opacity: 0;

  .kb-basic-modal {
    transform: scale(0.95);
  }
}
</style>