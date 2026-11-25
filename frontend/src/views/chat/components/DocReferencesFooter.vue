<template>
  <div class="doc-refs-footer" v-if="docs.length > 0">
    <div class="doc-refs-header">
      <t-icon name="link" class="doc-refs-icon" />
      <span class="doc-refs-title">{{ titleText }}</span>
    </div>
    <div class="doc-refs-list">
      <div
        v-for="doc in docs"
        :key="doc.key"
        class="doc-ref-card"
        :class="{ 'is-expanded': expanded[doc.key] }"
      >
        <button
          type="button"
          class="doc-ref-toggle"
          :aria-expanded="!!expanded[doc.key]"
          @click="toggle(doc.key)"
        >
          <t-icon name="file" size="14px" class="doc-ref-file-icon" />
          <span class="doc-ref-name" :title="doc.title">{{ doc.title }}</span>
          <span class="doc-ref-meta">{{ doc.chunks.length }} 段 · 相似度 {{ doc.topScore.toFixed(2) }}</span>
          <t-icon
            :name="expanded[doc.key] ? 'chevron-up' : 'chevron-down'"
            size="14px"
            class="doc-ref-chevron"
          />
        </button>
        <div v-show="expanded[doc.key]" class="doc-ref-body">
          <div
            v-for="chunk in doc.chunks"
            :key="chunk.id"
            class="doc-ref-chunk"
          >
            <div class="doc-ref-chunk-label">
              <span>第 {{ (chunk.chunk_index ?? 0) + 1 }} 段</span>
              <span class="doc-ref-chunk-score">score {{ (chunk.score ?? 0).toFixed(2) }}</span>
            </div>
            <div class="doc-ref-chunk-text">{{ truncate(chunk.content, 220) }}</div>
          </div>
          <div class="doc-ref-actions">
            <a
              v-if="doc.knowledgeBaseId"
              class="doc-ref-jump"
              :href="getDocumentHref(doc)"
              target="_blank"
              rel="noopener noreferrer"
              @click.stop
            >
              <t-icon name="jump" size="12px" />
              <span>查看完整文档</span>
            </a>
            <span v-else class="doc-ref-jump-disabled">
              <t-icon name="info-circle" size="12px" />
              <span>无知识库定位</span>
            </span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive } from 'vue';
import { useRouter } from 'vue-router';

interface KnowledgeRef {
  id?: string;
  knowledge_id?: string;
  knowledge_title?: string;
  knowledge_filename?: string;
  knowledge_base_id?: string;
  chunk_index?: number;
  chunk_type?: string;
  content?: string;
  score?: number;
}

interface DocGroup {
  key: string;
  title: string;
  filename?: string;
  knowledgeBaseId?: string;
  knowledgeId?: string;
  chunks: KnowledgeRef[];
  topScore: number;
}

const props = defineProps<{
  session: {
    knowledge_references?: KnowledgeRef[];
  };
  title?: string;
  /** 默认展开前 N 个文档 */
  defaultOpen?: number;
}>();

const router = useRouter();
const expanded = reactive<Record<string, boolean>>({});

const knowledgeRefs = computed<KnowledgeRef[]>(
  () => (props.session?.knowledge_references || []).filter(
    (r) => r.chunk_type !== 'web_search'
  )
);

const docs = computed<DocGroup[]>(() => {
  const refs = knowledgeRefs.value;
  if (!refs.length) return [];

  const groupMap = new Map<string, DocGroup>();
  for (const item of refs) {
    const key = item.knowledge_id || item.knowledge_title || item.id || '';
    if (!groupMap.has(key)) {
      groupMap.set(key, {
        key,
        title: item.knowledge_title || item.knowledge_filename || key,
        filename: item.knowledge_filename,
        knowledgeBaseId: item.knowledge_base_id,
        knowledgeId: item.knowledge_id,
        chunks: [],
        topScore: 0,
      });
    }
    const g = groupMap.get(key)!;
    g.chunks.push(item);
    if ((item.score ?? 0) > g.topScore) g.topScore = item.score ?? 0;
  }

  const list = Array.from(groupMap.values());
  // 默认展开前 defaultOpen 个文档
  const openCount = props.defaultOpen ?? 1;
  list.slice(0, openCount).forEach((g) => { expanded[g.key] = true; });
  return list;
});

const titleText = computed(() => {
  const t = props.title || '引入文档';
  return `${t} (${docs.value.length})`;
});

const toggle = (key: string) => {
  expanded[key] = !expanded[key];
};

const truncate = (content?: string, maxLen = 220): string => {
  if (!content) return '';
  const text = content.replace(/\s+/g, ' ').trim();
  if (text.length <= maxLen) return text;
  return text.slice(0, maxLen) + '…';
};

const getDocumentHref = (doc: DocGroup): string => {
  if (!doc.knowledgeBaseId) return '';
  const query: Record<string, string> = {};
  if (doc.knowledgeId) query.knowledge_id = doc.knowledgeId;
  return router.resolve({
    path: `/platform/knowledge-bases/${doc.knowledgeBaseId}`,
    query,
  }).href;
};
</script>

<style lang="less" scoped>
.doc-refs-footer {
  margin-top: 14px;
  padding: 0;
  border-radius: 8px;
  background: var(--td-bg-color-container);
  border: 0.5px solid var(--td-component-stroke);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.03);
  overflow: hidden;
}

.doc-refs-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  background: var(--td-brand-color-light);
  border-bottom: 0.5px solid var(--td-component-stroke);

  .doc-refs-icon {
    color: var(--td-brand-color);
    font-size: 14px;
  }

  .doc-refs-title {
    color: var(--td-text-color-primary);
    font-size: 13px;
    font-weight: 600;
  }
}

.doc-refs-list {
  padding: 8px 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.doc-ref-card {
  background: var(--td-bg-color-container);
  border: 0.5px solid var(--td-component-stroke);
  border-radius: 6px;
  overflow: hidden;
  transition: all 0.2s ease;

  &:hover {
    border-color: var(--td-brand-color);
    background: var(--td-brand-color-light-hover, rgba(0, 100, 255, 0.02));
  }

  &.is-expanded {
    border-color: var(--td-brand-color);
    box-shadow: 0 1px 4px rgba(0, 100, 255, 0.08);
  }
}

.doc-ref-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  background: transparent;
  border: 0;
  cursor: pointer;
  text-align: left;
  font-family: inherit;
  font-size: 13px;
  color: var(--td-text-color-primary);

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: -2px;
  }

  .doc-ref-file-icon {
    color: var(--td-brand-color);
    flex-shrink: 0;
  }

  .doc-ref-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }

  .doc-ref-meta {
    color: var(--td-text-color-placeholder);
    font-size: 11px;
    flex-shrink: 0;
    margin-left: 4px;
  }

  .doc-ref-chevron {
    color: var(--td-text-color-placeholder);
    flex-shrink: 0;
    transition: transform 0.2s ease;
  }
}

.is-expanded .doc-ref-chevron {
  color: var(--td-brand-color);
}

.doc-ref-body {
  padding: 4px 12px 10px;
  border-top: 0.5px dashed var(--td-component-stroke);
  background: var(--td-bg-color-secondarycontainer, #fafbfc);
}

.doc-ref-chunk {
  padding: 8px 0;
  border-bottom: 0.5px dashed var(--td-component-stroke);

  &:last-of-type {
    border-bottom: 0;
  }
}

.doc-ref-chunk-label {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--td-text-color-placeholder);
  margin-bottom: 4px;
}

.doc-ref-chunk-text {
  font-size: 13px;
  line-height: 1.7;
  color: var(--td-text-color-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.doc-ref-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 8px;
}

.doc-ref-jump {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  font-size: 12px;
  color: var(--td-brand-color);
  text-decoration: none;
  border: 0.5px solid var(--td-brand-color);
  border-radius: 4px;
  transition: all 0.15s ease;

  &:hover {
    background: var(--td-brand-color-light);
    text-decoration: none;
  }
}

.doc-ref-jump-disabled {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
  border: 0.5px dashed var(--td-component-stroke);
  border-radius: 4px;
}
</style>