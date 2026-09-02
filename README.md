# qoder-proxy

一个小型、可审查的 Qoder → OpenAI Chat Completions 反向代理。第一版只实现：

- `GET /healthz`
- `GET /readyz`
- `GET /v1/models`
- `POST /v1/chat/completions`（流式与非流式）

没有 WebUI、注册机、验证码、临时邮箱、数据库、插件系统，也没有伪装支持 `/v1/responses`、Anthropic 或 Gemini API。

> **重要说明**：Qoder 推理与 COSY 签名属于未公开的内部协议，可能随时变化。本版本基于 [REFERENCES.md](REFERENCES.md) 中固定的三个提交实现。开发和 CI 只使用模拟上游；**未使用真实 Qoder 凭据验证**。使用者只能导入自己合法持有且有权使用的账号与凭据，并遵守 Qoder 的服务规则和限额。

## 设计重点

请求调用链：

```text
OpenAI 客户端
  -> 本地 API Key / 请求大小 / 模型能力校验
  -> 健康账号选择与到期前刷新
  -> 一小时实时模型目录缓存
  -> Bearer 或 COSY 传输（发送前确定，不用失败生成探测）
  -> 统一 SSE 事件层
  -> OpenAI SSE，或同一事件流聚合为非流式 JSON
```

核心原则：

- Bearer 请求保留标准 `messages`、`tools`、`tool_choice`、`stream_options`、`reasoning`、`reasoning_effort`、`enable_thinking` 和 token 上限。
- COSY 请求使用实时目录里的完整 `model_config`；未知字段也会保留。
- 多条 system 消息保留在 `messages` 中，不依赖可能被忽略的顶层 `system`。
- 新版 OpenAI 客户端发送的 `developer` 消息会按原顺序规范化为 Qoder 可识别的 `system` 消息。
- 图片、assistant tool calls、tool result、空参数工具和并行工具都保留。
- reasoning、普通 content、usage/cache token、真实 finish reason 分开处理。
- 一旦获得 200 流响应，解析失败、超时或断线均不换账号、不换协议重放。

## 构建

需要 Go 1.22 或更新版本：

```bash
go build -o qoder-proxy ./cmd/qoder-proxy
```

复制配置示例：

```bash
cp config.example.json config.json
```

默认只监听 `127.0.0.1:8080`。配置文件使用 JSON，凭据独立保存为 `credentials.json`。

## 登录与凭据

### 浏览器 Device Flow（全球区）

```bash
./qoder-proxy login --region global --config config.json
```

程序本地生成 PKCE verifier、challenge、nonce 和 machine ID，只打印浏览器授权 URL；包含 verifier 的轮询 URL不会输出。

### PAT 导入（全球区或中国区）

交互式隐藏输入：

```bash
./qoder-proxy import-pat --region global --config config.json
./qoder-proxy import-pat --region cn --config config.json
```

也可以仅为当前命令设置环境变量：

```bash
QODER_PAT='pt-...' ./qoder-proxy import-pat --region global --config config.json
QODERCN_PAT='pt-...' ./qoder-proxy import-pat --region cn --config config.json
```

PAT 只用于换取 Job Token，默认不写入磁盘。磁盘仅保存 access token、refresh token、用户/机器标识和 Unix 毫秒过期时间。凭据文件采用同目录临时文件、`fsync`、原子 rename，并强制为 `0600`；Unix 下发现更宽松权限会拒绝读取。

本工具不会扫描或导入 `~/.qoder`、其他 QoderGateway 数据库或任何本机已有 Qoder 凭据。

## 启动

```bash
./qoder-proxy serve --config config.json
```

如需监听局域网或容器网卡，必须设置代理自身的 API Key，否则拒绝启动：

```bash
QODER_PROXY_API_KEY='use-a-long-random-value' ./qoder-proxy serve --config config.json
```

客户端可使用任一头：

```http
Authorization: Bearer use-a-long-random-value
```

或：

```http
X-API-Key: use-a-long-random-value
```

比较使用 SHA-256 后的常量时间比较。`/healthz` 和 `/readyz` 保留为探针接口；`/v1/*` 受 API Key 保护。

## 调用示例

查看当前账号实时可用模型：

```bash
curl -s http://127.0.0.1:8080/v1/models \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY'
```

流式：

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY' \
  -H 'X-Session-Key: my-conversation' \
  -d '{
    "model": "lite",
    "messages": [{"role":"user","content":"你好"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'
```

非流式：

```bash
curl -s http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY' \
  -d '{
    "model": "lite",
    "messages": [{"role":"user","content":"只回复 OK"}],
    "stream": false
  }'
```

对外模型 ID 使用实时目录返回的稳定 `key`，`display_name` 仅用于展示。不会把显示名称改写成未经证明的别名。

## Bearer 与 COSY 选择规则

`transport` 可设为 `auto`、`bearer` 或 `cosy`。

`auto` 使用当前证据支持的能力路由：

- `auto`、`ultimate`、`performance`、`efficient`、`lite` 路由档位优先 Bearer。
- 实时目录里的具体模型 key 优先 COSY/API3，并携带完整 `model_config`。
- 全球区 Bearer 先用无副作用 `OPTIONS` 探测；404 才判为不可用。OPTIONS 的 405 反而说明路由存在。
- 中国区没有足够证据证明存在对应 Bearer 推理地址，自动使用 COSY。
- 选择结果按账号和能力类别缓存在内存。

只有实际 POST 明确返回 404/405，且尚未开始读取响应流时，才允许 Bearer → COSY。401 只刷新一次；403 标记账号异常；429 按 `Retry-After` 或默认时间冷却；5xx 在未出流前有限换账号。网络错误、总超时和结果不确定的错误不跨 transport 重试。

## 模型目录与 readiness

模型目录来自区域对应的 COSY `/algo/api/v2/model/list?Encode=1`：

- 只暴露 `enable=true`。
- 保存并回传完整原始 `model_config`。
- 解析 `display_name`、`is_vl`、`is_reasoning`、`thinking_config`、`source`。
- context window 优先取 `context_config` 最大 `token_count`，否则用 `max_input_tokens`。
- 内存缓存一小时；刷新失败继续用上次成功缓存。
- 从未成功获取时只暴露保守的 `lite / 180000` 静态项，并在 `/v1/models` 和 `/readyz` 标记 `degraded=true`。

本工具不会凭猜测宣称 1M context 或 128K output。

## 运维命令

```bash
./qoder-proxy models --config config.json
./qoder-proxy doctor --config config.json
```

`doctor` 检查凭据权限、userinfo、quota、Bearer/COSY 能力和模型目录。输出只使用账号匿名 ID，不打印 token 或完整授权头。

## Docker

先在宿主机完成登录并确保凭据为 `0600`，然后：

```bash
docker build -t qoder-proxy:local .
cp config.docker.example.json config.docker.json
docker run --rm -p 127.0.0.1:8080:8080 \
  -e QODER_PROXY_API_KEY='use-a-long-random-value' \
  -v "$PWD/config.docker.json:/app/config.json:ro" \
  -v "$PWD/credentials.json:/app/credentials.json" \
  qoder-proxy:local
```

容器内需要在配置中监听 `0.0.0.0:8080`，因此必须提供 `QODER_PROXY_API_KEY`。

## 测试

默认测试不会访问 Qoder：

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

真实测试必须显式开启，并指向专用的 `0600` 凭据文件：

```bash
QODER_LIVE_TEST=1 \
QODER_LIVE_CREDENTIALS=/absolute/path/to/test.credentials.json \
go test ./internal/qoder -run TestLiveBearerOptIn -v
```

该测试会实际获取目录并发送一次很小的 Bearer 推理请求，会消耗账号额度。不要在普通 CI 中启用。

## 安全边界

- 默认 loopback；非 loopback 无 API Key 拒绝启动。
- 上游 host 固定白名单，不接受客户端提供 URL，不构成通用转发器。
- 请求体默认上限 8 MiB；连接、响应头、流空闲和总请求均有超时。
- CORS 默认关闭，仅可列出精确 origin。
- 日志只含 request ID、模型、账号匿名 ID、耗时和状态。
- 上游错误正文先脱敏再截断，PAT、access/refresh token、Authorization 和 Cosy-Key 不得进入日志。
- 多账号只是合法账号的可用性与故障隔离机制，不得用于绕过限额或服务规则。

更多漏洞报告方式见 [SECURITY.md](SECURITY.md)。

## 已知限制

- 没有真实账号 live test，因此不能承诺 2026-09-02 之后未公开协议仍兼容。
- Bearer 的具体模型能力来自参考仓库实测，而非 Qoder 公共文档；具体模型默认走 API3/COSY。
- CN Device Flow 未获得足够证据，只支持 CN PAT 导入。
- 不支持 SOCKS5 代理（标准库无内置 SOCKS5 dialer）；支持 HTTP/HTTPS CONNECT 代理。
- 模型目录只缓存在内存，重启后会重新获取；这不是凭据或模型数据库。
- 流式响应已经发出 HTTP 200 后，如上游 malformed/断线，会发送带 `partial=true` 的 SSE error 和单个 `[DONE]`；HTTP 状态无法再改写。
