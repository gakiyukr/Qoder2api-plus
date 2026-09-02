# 协议参考与固定版本

拉取时间：2026-09-02 UTC。

| 仓库 | 实际参考 commit | 参考内容 |
| --- | --- | --- |
| `simonsmh/pi-provider-qoder` | `8dd4cdbefb54bf932a780400f77e1c35f76e353d` | global/CN 区域、PAT、Device Flow、COSY 1.1.38、Body 编码、实时目录、thinking/tool/image/usage、envelope SSE |
| `kaitranntt/CLIProxyAPIPlus` | `f5570ed69c3b82e3ec789b986a7f61396af49180` | Go 结构、Qoder API3/COSY 请求、完整 model_config、动态目录、多账号/冷却设计、统一转换与测试模式 |
| `bzym2/QoderGateway` | `d00376cdb4e74cc0f1714d5a22e94815426c5731` | api2-v2 Bearer 推理、PKCE/刷新/额度研究、标准 SSE 与 raw_usage |

## 采用的证据

- Global PAT：`POST https://openapi.qoder.sh/api/v1/jobToken/exchange`。
- CN PAT：`POST https://openapi.qoder.com.cn/api/v1/jobToken/exchange`。
- Device Flow：global `qoder.com/device/selectAccounts` + `openapi.qoder.sh/api/v1/deviceToken/poll`；404/202 表示等待。
- Device/Job 刷新分别使用 `/api/v1/deviceToken/refresh` 与 `/api/v1/jobToken/refresh`。
- Global Bearer chat：`https://api2-v2.qoder.sh/model/v1/chat/completions`。
- Global COSY chat：`https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?...&Encode=1`。
- CN COSY chat/model：`https://gateway.qoder.com.cn/algo/...`。
- 模型目录必须通过 COSY 获取，并把每个 entry 的完整 JSON 作为 `model_config` 回传。

## 主动修正的参考实现问题

- `expires_in` 在已观察 PAT/Device 响应中表现为毫秒；实现按量级兼容毫秒和旧秒值，并统一存 Unix 毫秒。
- 刷新失败不延长本地过期时间，不伪造可用状态。
- 不使用静态 1M context 或 131072 output 宣称；目录缺字段时只保守使用上游明确返回的值。
- malformed SSE 不静默跳过。
- 不强制 finish reason 为 `stop`，不把 usage 写死为 0。
- system prompt 保留为首部/原位置 `role=system` 消息；COSY 顶层 `system` 置空。
- Bearer 路由存在不代表它支持目录里的所有具体模型；具体模型按已有 live 证据预先选择 COSY，不用一次失败生成来探测。

## 未验证事实

本项目开发期间没有读取任何本机 Qoder 凭据，也没有执行真实 Qoder 请求。三个仓库中的抓包与 live fixture 是二手证据；若官方协议变化，应使用明确 opt-in 的 live test 重新录制脱敏 fixture，并同步更新 `internal/protocol/constants.go` 与测试。
