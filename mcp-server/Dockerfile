FROM python:3.11-slim

# 从构建参数接收代理设置
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG no_proxy

# 传递给容器内程序
ENV HTTP_PROXY=${HTTP_PROXY}
ENV HTTPS_PROXY=${HTTPS_PROXY}
ENV NO_PROXY=${NO_PROXY}
ENV no_proxy=${no_proxy}
ENV PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .
RUN pip install --no-cache-dir -e .

ENV MCP_HOST=0.0.0.0
ENV MCP_PORT=8000
ENV WEKNORA_BASE_URL=http://app:8080/api/v1
# Set MCP_SERVER_AUTH_TOKEN at runtime; HTTP transport refuses to start without it.

EXPOSE 8000

CMD ["weknora-mcp-server", "--transport", "http", "--host", "0.0.0.0", "--port", "8000"]
