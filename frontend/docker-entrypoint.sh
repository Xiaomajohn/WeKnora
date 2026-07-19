#!/bin/sh

# 生成运行时配置文件，注入环境变量到前端
# 注意：所有字符串值必须用引号包起来，否则浏览器 JS 解析失败
# （shell 展开 ${VAR:-} 后是裸字符串，不是合法的 JS 字面量）。
# URL 类（BRAND_OFFICIAL_URL / BRAND_GITHUB_URL）只在非空时输出对应 key，
# 避免出现无效的 `key: ,`。
{
  echo "window.__RUNTIME_CONFIG__ = {"
  echo "  MAX_FILE_SIZE_MB: ${MAX_FILE_SIZE_MB:-50},"
  echo "  BRAND_NAME: \"${BRAND_NAME:-WeKnora}\","
  if [ -n "${BRAND_OFFICIAL_URL:-}" ]; then
    echo "  BRAND_OFFICIAL_URL: \"${BRAND_OFFICIAL_URL}\","
  fi
  if [ -n "${BRAND_GITHUB_URL:-}" ]; then
    echo "  BRAND_GITHUB_URL: \"${BRAND_GITHUB_URL}\","
  fi
  echo "};"
} > /usr/share/nginx/html/config.js

# 处理 nginx 配置
export MAX_FILE_SIZE=${MAX_FILE_SIZE_MB}M
export APP_HOST=${APP_HOST:-app}
export APP_PORT=${APP_PORT:-8080}
export APP_SCHEME=${APP_SCHEME:-http}
envsubst '${MAX_FILE_SIZE} ${APP_HOST} ${APP_PORT} ${APP_SCHEME}' < /etc/nginx/templates/default.conf.template > /etc/nginx/conf.d/default.conf

# 启动 nginx
exec nginx -g 'daemon off;'
