# 测试知识库 小程序

本目录提供“测试知识库”产品的微信小程序插件，让移动端用户能够：

- 配置测试知识库 API 端点与租户 API Key；
- 查看可用知识库列表；
- 将一个网页链接导入到选定的知识库；
- 通过测试知识库的对话能力向选定的知识库提问。

## Getting started

1. Open `miniprogram/` in WeChat DevTools.
2. Copy `project.private.config.json.example` to `project.private.config.json` and set your real Mini Program AppID. The shared `project.config.json` intentionally does not include an AppID to avoid forcing maintainers into a placeholder project.
3. Open the **Settings** tab and fill in:
   - API Base URL, for example `https://knowledge-base.example.com`;
   - API Key from the deployment tenant settings page.
4. Open the **Knowledge** tab, refresh knowledge bases, and select the target knowledge base.
5. Import a URL or switch to **Chat** to ask questions.

## Local development notes

- WeChat DevTools may block `localhost` requests when URL validation is enabled. For local testing, either disable domain validation in DevTools or expose the backend through an HTTPS development domain.
- In production Mini Programs, add the backend API domain to the Mini Program request domain allowlist.
- The chat endpoint returns Server-Sent Events. The Mini Program client parses completed SSE text responses and displays accumulated `answer` chunks.

## Test

```bash
cd miniprogram
npm test
```
