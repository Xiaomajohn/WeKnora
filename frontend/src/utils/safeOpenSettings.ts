import type { Router } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import i18n from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

/**
 * 用户级角色 gate：调用 uiStore.openSettings(...) 前必须经过此函数。
 *
 * 该函数集中处理"普通用户（user_role=normal）禁止跳转到任何 Settings 入口"
 * 的策略，所有跨子组件的"前往设置"handler 共用同一份拦截逻辑，避免每处
 * 单点实现带来遗漏风险（plan 中嗅探出 25 处跳转入口，单点遗漏会让
 * normal 用户仍然能绕过 UI 路由层的 beforeEach 拦截）。
 *
 * 调用流程：
 *  1. 通过 authStore.isAllowedToEnterSettings（user_role !== 'normal'）
 *     进行 gate 判断；超管（is_system_admin）和跨租户 superuser（canAccessAllTenants）
 *     通过 authStore 内的同一 computed 自然豁免。
 *  2. 不允许：弹 i18n 文案 toast，返回 false，调用方应直接 return，不再
 *     执行后续的 uiStore.openSettings / router.push。
 *  3. 允许：uiStore.openSettings(section, subSection) → router.push 到
 *     `/platform/settings`（仅当 router 已传入），并返回 true。
 *
 * 用法 A —— 直接路由到 /platform/settings（多数跳入设置入口的子组件）：
 *   const opened = safeOpenSettings(router, 'models')
 *   if (!opened) return
 *
 * 用法 B —— 仅打开 Settings 抽屉、不路由（嵌入 KB 编辑器中的子组件，
 *           其上下文已被 KB 编辑器 modal 锚定，不希望改路由）：
 *   if (!safeOpenSettings(undefined, 'parser')) return
 *
 * @returns true 表示已开门；false 表示被 gate 拦截、调用方应停止后续流程。
 */
export function safeOpenSettings(
  router: Router | undefined,
  section?: string,
  subSection?: string,
): boolean {
  const authStore = useAuthStore()
  if (!authStore.isAllowedToEnterSettings) {
    // i18n 在路由切换早期可能尚未完全初始化，按键取多兜一层保护
    const t = i18n.global.t as unknown as (key: string) => string
    MessagePlugin.warning(t('settingsVisibility.userLevelRestricted.title'))
    return false
  }
  const uiStore = useUIStore()
  uiStore.openSettings(section, subSection)
  // 仅当调用方显式提供 router 时才同步 push URL；嵌入式子组件
  // （如 KB 编辑器内的 settings link）只在 uiStore 上开门，路由保持
  // 不变以便 KB editor modal 仍能保留上下文。
  if (router) {
    const query: Record<string, string> = {}
    if (section) query.section = section
    if (subSection) query.subSection = subSection
    router.push({ path: '/platform/settings', query })
  }
  return true
}