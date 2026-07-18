import { MessagePlugin } from "tdesign-vue-next";
import i18n from '@/i18n';

// 声明全局运行时配置类型
declare global {
  interface Window {
    __RUNTIME_CONFIG__?: {
      MAX_FILE_SIZE_MB?: number;
      // 品牌定制：docker-entrypoint.sh 会读取 .env 中的同名变量写入 config.js。
      // 留空 BRAND_OFFICIAL_URL / BRAND_GITHUB_URL 即可隐藏对应入口按钮。
      BRAND_NAME?: string;
      BRAND_OFFICIAL_URL?: string;
      BRAND_GITHUB_URL?: string;
    };
  }
}

// 从运行时配置获取最大文件大小(MB)，支持 Docker 环境动态配置
// 优先级：运行时配置 > 构建时环境变量 > 默认值 50MB
export const MAX_FILE_SIZE_MB = window.__RUNTIME_CONFIG__?.MAX_FILE_SIZE_MB
  || Number(import.meta.env.VITE_MAX_FILE_SIZE_MB)
  || 50;
const MAX_FILE_SIZE_BYTES = MAX_FILE_SIZE_MB * 1024 * 1024;

// 品牌定制常量。
// 优先级：运行时配置 (config.js) > 构建时 VITE_* 环境变量 > 硬编码默认值。
// 运行时配置由 frontend/docker-entrypoint.sh 在容器启动时从 .env 写入。
export const BRAND_NAME =
  window.__RUNTIME_CONFIG__?.BRAND_NAME ||
  (import.meta.env.VITE_BRAND_NAME as string | undefined) ||
  'WeKnora';
export const BRAND_OFFICIAL_URL =
  window.__RUNTIME_CONFIG__?.BRAND_OFFICIAL_URL ||
  (import.meta.env.VITE_BRAND_OFFICIAL_URL as string | undefined) ||
  '';
export const BRAND_GITHUB_URL =
  window.__RUNTIME_CONFIG__?.BRAND_GITHUB_URL ||
  (import.meta.env.VITE_BRAND_GITHUB_URL as string | undefined) ||
  '';
// URL 为空字符串则视为"不显示该按钮"。
export const SHOW_OFFICIAL_LINK = BRAND_OFFICIAL_URL.trim().length > 0;
export const SHOW_GITHUB_LINK = BRAND_GITHUB_URL.trim().length > 0;

export function generateRandomString(length: number) {
  let result = "";
  const characters =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const charactersLength = characters.length;
  for (let i = 0; i < length; i++) {
    result += characters.charAt(Math.floor(Math.random() * charactersLength));
  }
  return result;
}

export function formatStringDate(date: any) {
  let data = new Date(date);
  let year = data.getFullYear();
  let month = String(data.getMonth() + 1).padStart(2, '0');
  let day = String(data.getDate()).padStart(2, '0');
  let hour = String(data.getHours()).padStart(2, '0');
  let minute = String(data.getMinutes()).padStart(2, '0');
  let second = String(data.getSeconds()).padStart(2, '0');
  return (
    year + "-" + month + "-" + day + " " + hour + ":" + minute + ":" + second
  );
}
const DEFAULT_VALID_TYPES = new Set(["pdf", "txt", "md", "docx", "doc", "pptx", "ppt", "epub", "mhtml", "jpg", "jpeg", "png", "csv", "xlsx", "xls", "mp3", "wav", "m4a", "flac", "ogg"]);

/**
 * Returns true when the file should be **rejected**.
 * @param validTypes - override the default extension whitelist with a dynamic set (e.g. from engine registry).
 */
export function kbFileTypeVerification(file: any, silent = false, validTypes?: Set<string> | string[]) {
  const provided = validTypes
    ? (validTypes instanceof Set ? validTypes : new Set(validTypes))
    : undefined;
  // An empty whitelist means the engine registry hasn't loaded yet; fall back to
  // the default set rather than rejecting every file.
  const allowed = provided && provided.size > 0 ? provided : DEFAULT_VALID_TYPES;

  const type = file.name.substring(file.name.lastIndexOf(".") + 1).toLowerCase();
  if (!allowed.has(type)) {
    if (!silent) {
      MessagePlugin.error(i18n.global.t('error.unsupportedFileType'));
    }
    return true;
  }
  if (file.size > MAX_FILE_SIZE_BYTES) {
    if (!silent) {
      MessagePlugin.error(i18n.global.t('error.fileSizeExceeded', { size: MAX_FILE_SIZE_MB }));
    }
    return true;
  }
  return false;
}
