interface RuntimeBrandConfig {
  BRAND_NAME?: string
  BRAND_FULL_NAME?: string
  BRAND_DESCRIPTION?: string
  BRAND_LOGO_TEXT?: string
  BRAND_OFFICIAL_URL?: string
  BRAND_GITHUB_URL?: string
}

const runtimeConfig = (typeof window !== 'undefined'
  ? (window as Window & { __RUNTIME_CONFIG__?: RuntimeBrandConfig }).__RUNTIME_CONFIG__
  : undefined)

function runtimeOrEnv(key: keyof RuntimeBrandConfig): string | undefined {
  const runtimeValue = runtimeConfig?.[key]?.trim()
  if (runtimeValue) return runtimeValue
  const buildValue = import.meta.env[`VITE_${key}`] as string | undefined
  return buildValue?.trim() || undefined
}

export const BRAND_NAME = runtimeOrEnv('BRAND_NAME') || 'WeKnora'
export const BRAND_FULL_NAME = runtimeOrEnv('BRAND_FULL_NAME') || BRAND_NAME
export const BRAND_DESCRIPTION = runtimeOrEnv('BRAND_DESCRIPTION') || '知识库问答与文档理解平台'
export const BRAND_LOGO_TEXT = runtimeOrEnv('BRAND_LOGO_TEXT') || BRAND_NAME
export const BRAND_OFFICIAL_URL = runtimeOrEnv('BRAND_OFFICIAL_URL') || ''
export const BRAND_GITHUB_URL = runtimeOrEnv('BRAND_GITHUB_URL') || ''
export const SHOW_OFFICIAL_LINK = BRAND_OFFICIAL_URL.length > 0
export const SHOW_GITHUB_LINK = BRAND_GITHUB_URL.length > 0
