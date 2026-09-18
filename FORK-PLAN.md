# Qoder2api-plus 二開計劃

> 基線：[J-York/QoderProxy](https://github.com/J-York/QoderProxy) `a4e0211`（upstream/main）
> 分支策略：`main` 跟隨上游；所有二開在 `plus` 分支進行
> 建立日期：2026-09-18
> 生產驗證：已部署於 hytron（154.88.65.226），實測 `/v1/models`、非流式、串流、reasoning 全通

---

## 0. 上游定位與實測結論

上游是一個**純標準庫 Go** 的 Qoder → OpenAI Chat Completions 反向代理：

- `go.mod` 零第三方依賴（僅 `module` + `go 1.22` 兩行）
- 單二進位 7.27 MB（`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`）
- 運行時記憶體 ~17 MB RSS
- 支援 global / cn 雙區；Bearer（`api2-v2.qoder.sh/model/v1/chat/completions`，OpenAI 原生）與 COSY（`api3.qoder.sh`）雙傳輸，auto 模式自動選擇
- Device Flow（PKCE）+ PAT 匯入兩種登入；憑證原子寫入 0600
- 多帳號池：健康檢查、冷卻、停用、到期前刷新、session 親和（SHA-256 分桶）

**作者明確的範圍裁剪**（README 原文）：

> 沒有 WebUI、註冊機、驗證碼、臨時信箱、資料庫、插件系統，也沒有偽裝支持 `/v1/responses`、Anthropic 或 Gemini API

---

## 1. 生產環境實測記錄（2026-09-18）

| 項目 | 結果 |
|---|---|
| 上游連通 | `openapi.qoder.sh` 401 / `api2-v2.qoder.sh` 405 / `api3.qoder.sh` 403 / `qoder.com` 302 —— 全部可達，無地區封鎖 |
| Device Flow 登入 | ✅ 取得 `dt-`/`drt-`，憑證落盤 0600 |
| `/v1/models` | ✅ 動態返回（實測 `qfmodel` Qwen3.8-Flash、`qmodel_38max` Qwen3.8-Max，ctx 1,000,000） |
| 非流式 chat（`qfmodel`） | ✅ 標準 `chat.completion`，含 `reasoning_content` 與 `usage` |
| 串流 chat（`qfmodel`） | ✅ 標準 OpenAI SSE：reasoning 增量 → content 增量 → `finish_reason:stop` → `[DONE]` |
| `qmodel_38max` | ❌ HTTP 403 + `code 112` + `pricingUrl`（帳號 Free 層無額度） |
| 接入 new-api | ✅ 容器經 `http://172.18.0.1:8080/v1` 訪問宿主 loopback 服務 |

---

## 2. 缺點清單（實際代碼審查，附證據）

### P0 —— 實際咬到人的 bug

#### 2.1 403 一律永久停用帳號（`internal/server/server.go` chat handler）

```go
case 403:
    s.Pool.MarkDisabled(entry, "Qoder returned 403")
```

**問題**：`MarkDisabled` 是永久停用。但實測 `403 + code 112 + pricingUrl` 是「**該模型無額度**」（帳號本身健康——同一帳號 `qfmodel` 正常、`quota/usage` 端點 200）。

**後果**：請求一次付費模型 → 整個帳號被停用 → 免費模型 `qfmodel` 也一起死 → 只能重啟服務。

**修法**：解析 403 body；含 `code 112` / `pricingUrl` 時視為模型級配額錯誤，直接回 403 給客戶端，**不停用帳號**。同時 `MarkDisabled` 應該可恢復（見 2.2）。

### P1 —— 可觀測性與正確性

#### 2.2 停用無恢復路徑

`MarkDisabled` 之後，`Select()` 永遠跳過該帳號（`state != "healthy"`）。全倉沒有任何地方把 `disabled` 改回 `healthy`（`Recover()` 存在但無調用者）。若上線多帳號，一個誤判 403 就永久損失一個帳號。

**修法**：`disabled` 帶時間戳，超過冷卻期（如 5 分鐘）自動回 healthy 重試；或管理端點手動恢復。

#### 2.3 `/v1/models` 不過濾無權限模型

動態 catalog 返回上游全部模型，包括帳號無權使用的（如 Free 層的 `qmodel_38max`）。客戶端選了它，要到 chat 階段才 403。

**修法**：chat 收到 `code 112` 類錯誤後，將該模型標記為「此帳號不可用」，後續 `/v1/models` 對該帳號過濾；或至少在錯誤訊息中返回可替代模型清單。

#### 2.4 觀測能力為零

- `Pool.Statuses()` 已實作但**沒有任何 HTTP 端點暴露**
- 無 token 用量統計、無請求歷史、無 per-model 計數
- 僅 stdout 一行日誌（`request_id/model/account/latency/status`）

**修法**：新增 `/admin/status`（API Key 保護）返回 pool 狀態 + catalog 摘要；可選 `/admin/quota` 直通上游 `GET /api/v2/quota/usage`（`AuthClient.Quota()` 已實作，同樣無調用者——見 2.5）。

#### 2.5 已實作但無調用者的死碼

| 符號 | 位置 | 狀態 |
|---|---|---|
| `AuthClient.Quota()` | `internal/qoder/auth.go` | 無調用者 |
| `Pool.Recover()` | `internal/pool/pool.go` | 無調用者 |
| `Pool.Statuses()` | `internal/pool/pool.go` | 無調用者 |
| `credential.Account.AnonymousID()` | 日誌在用 | 正常 |

二開應把它們接上（quota → `/admin/quota`，Recover → 恢復路徑，Statuses → `/admin/status`），而非重寫。

### P2 —— 多帳號場景（當前單帳號用不到，先記錄）

#### 2.6 無「模型 → 帳號」親和性

`Pool.Select()` 只看 `healthy`，不看帳號是否有該模型權限。多帳號時：請求 A 獨有模型 → 可能選中 B → `findModel` 失敗 → 直接 400，**不換帳號重試**（`findModel` 失敗是 `return` 不是 `continue`）。

**修法**：`findModel` 失敗改 `continue`（換下一帳號）；或 catalog 緩存按帳號記錄模型集合，`Select()` 帶模型參數過濾。

#### 2.7 單一 API Key、無速率限制

`authorize()` 只支援一個 key；無 per-key 計量、無速率限制。**暫不修**——上層已有 new-api 負責多用戶/計費/限流，網關層重複建設無意義。

#### 2.8 `Select()` 的 session 分桶只用 1 byte

`sha256.Sum256(session)[0] % len(candidates)` —— 分佈限於 256 桶且非均勻。帳號少時無感，帳號多時不均。**低優先級**：改用完整 hash 取模即可（一行）。

### P3 —— 範圍擴展（按需求觸發）

| 項目 | 工作量估算 | 觸發條件 |
|---|---|---|
| `/v1/messages`（Anthropic 適配，Claude Code 可用） | ~200 行 + 測試 | 實際需要 Claude Code 直連 |
| `/v1/responses`（Codex CLI） | ~250 行 | 實際需要 Codex |
| Prometheus `/metrics` | ~80 行 | 接監控系統 |
| Web 管理面板 | ~500 行 | 多帳號運營 |

### 明確不做

- **領取活動 Credits / Pro Trial 自動化**——上游無此功能是有意為之；此類功能屬於帳號運營自動化，與反代職責無關，且平台風控明確反對。本倉庫不納入。
- **註冊機 / 硬件指紋偽造**——同上，且與「可審查的小型代理」定位衝突。
- **多用戶計費**——上層 new-api 已承擔。

---

## 3. 二開排程

| 階段 | 內容 | 完成標準 |
|---|---|---|
| **Phase 1（P0）** | 403 細分：`code 112`/`pricingUrl` → 模型級錯誤，不停用帳號；`MarkDisabled` 帶恢復時間 | 請求 `qmodel_38max` 後 `qfmodel` 仍可用；`/readyz` 不降級 |
| **Phase 2（P1）** | `/admin/status` + `/admin/quota`（複用 `Statuses()`/`Quota()`）；`/v1/models` 標記帳號不可用模型 | 兩端點有 API Key 保護；quota 返回真實餘額 |
| **Phase 3（P2，可選）** | `findModel` 失敗改 continue；session 分桶用完整 hash | 多帳號下模型路由正確 |
| **Phase 4（P3，按需）** | Anthropic 適配 / metrics | 觸發條件成立時再議 |

每個 Phase 完成後：`go vet ./... && go test ./... && go test -race ./...` 全綠 → 交叉編譯 `linux/amd64` → 部署 hytron 冒煙 → 更新本文件。

---

## 4. 部署流程（已驗證）

```bash
# 本機（Windows，Go 1.26.5）
cd Qoder2api-plus
go vet ./... && go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o qoder-proxy-linux-amd64 ./cmd/qoder-proxy
scp -P 2333 qoder-proxy-linux-amd64 root@154.88.65.226:/opt/qoder-proxy/qoder-proxy.new

# 伺服器（Debian 13，SSH 端口 2333）
ssh -p 2333 root@154.88.65.226
cd /opt/qoder-proxy
systemctl stop qoder-proxy
mv qoder-proxy qoder-proxy.bak && mv qoder-proxy.new qoder-proxy && chmod +x qoder-proxy
systemctl start qoder-proxy
curl -s http://127.0.0.1:8080/healthz
```

配置：`/opt/qoder-proxy/config.json`（listen `0.0.0.0:8080`）；憑證：`credentials.json`（0600）；
API Key：systemd `Environment=QODER_PROXY_API_KEY=...`；上游：new-api 渠道 `http://172.18.0.1:8080/v1`。

**注意**：hytron 的 SSH 鏈路不穩（直連與代理均可能中途斷線），每條 SSH 命令應短小、幂等、可重入。

---

## 5. 風險與依賴

| 風險 | 說明 | 緩解 |
|---|---|---|
| 上游協議變更 | Qoder 未公開協議；`api2-v2` 端點、模型 catalog 結構可能變 | `internal/protocol/constants.go` 集中管理；變更時只需改常數 + fixtures |
| 上游 0★ 無維護 | J-York/QoderProxy 個人專案，可能棄坑 | 本倉庫即 fork，自持；`main` 可 rebase 上游 |
| Free 層模型變動 | `qfmodel` 免費期至 2026-09-30 23:59（官方文件） | 9/30 後若收費，需訂閱或接受 `qfmodel` 失效 |
| 帳號風控 | 帳號 `personal_standard`、機房 IP 註冊、多地登入已觸發風控標記 | 不做任何規避；活動領取僅桌面端人工操作 |
