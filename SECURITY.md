# Security Policy

请不要在公开 issue、日志或 fixture 中提交 PAT、access token、refresh token、完整 `Authorization`、`Cosy-Key`、Device Flow poll URL、电子邮箱或用户 ID。

发现漏洞时，请私下向项目维护者提供：受影响版本、最小复现、预期/实际行为以及已经脱敏的日志。不要附带真实账号凭据。

如果怀疑凭据泄露：立即停止服务、撤销 Qoder PAT/会话、轮换 `QODER_PROXY_API_KEY`，删除泄露日志，并重新登录生成新凭据。不要依赖本工具替你撤销上游 token。
