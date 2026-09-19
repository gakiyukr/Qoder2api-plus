# Qoder2api-plus

Qoder → OpenAI Chat Completions 反向代理。本倉庫是 [J-York/QoderProxy](https://github.com/J-York/QoderProxy) 的 fork，在其之上做了生產驗證與三項修正，二開決策與驗收記錄見 [FORK-PLAN.md](FORK-PLAN.md)。

```text
OpenAI 相容客戶端 / new-api
  -> 本機 API Key 校驗 / 請求大小 / 模型能力校驗
  -> 多帳號池選號與到期前刷新
  -> 一小時即時模型目錄快取
  -> Bearer 或 COSY 傳輸（發送前確定，不用失敗生成探測）
  -> 統一 SSE 事件層
  -> OpenAI SSE，或同一事件流聚合為非串流 JSON
```

## 與上游的差異

| 項目 | 內容 | commit |
|---|---|---|
| **真實憑證驗證** | 上游自述「未使用真實 Qoder 憑證驗證」。本 fork 已在 Debian 13 + 兩個國際版帳號實測：device flow 登入、`/v1/models`、非串流、串流、reasoning 增量全部通過。上游免責聲明已據實改寫。 | — |
| **配額 403 誤判修復** | 上游把任何 403 一律視為帳號級故障並**永久停用**該帳號。但 Free 層帳號請求付費模型時，上游回的是「該模型無額度」（`code 112` + `pricingUrl`），帳號本身健康——原實作會讓免費模型跟著一起死。現已區分兩類；停用亦改為 5 分鐘可自動恢復。 | `461c1ce` |
| **`/admin/status`、`/admin/quota`** | 上游已寫好 `Pool.Statuses()` 與 `AuthClient.Quota()` 但無任何呼叫者。現接為兩個端點：每帳號即時狀態、每帳號真實餘額（含活動贈送的 add-on 資源包）。 | `27743e4` |
| **配置校驗範圍修正** | 「非迴環監聽必須有 API Key」原本寫在全域 `Validate()`，導致 `login` / `import-pat` / `models` / `doctor` 這四個不開監聽的子命令也被擋。現移入 `ValidateServe()`，僅 `serve()` 生效。 | `1685c5c` |

上游其餘能力（雙區、Bearer/COSY 自動路由、PKCE device flow、0600 原子落盤、常量時間比對、上遊 host 白名單、超時分層）**未改動**，仍以其 `REFERENCES.md` 與原實作為準。

## 定位與範圍

刻意保持精簡的單二進位代理，**零第三方 Go 依賴**（`go.mod` 只有 `module` 與 `go` 兩行）。沒有 Web UI、沒有資料庫、沒有插件系統，也**不提供** `/v1/responses`、Anthropic Messages 或 Gemini 格式。

多帳號池是**合法自有帳號**的可用性與故障隔離機制。本倉庫不含、也不會加入任何批量註冊、驗證碼繞過、機器指紋偽造或活動獎勵自動化——那類做法違反平台服務條款。

## 構建

需要 Go 1.22 以上：

```bash
go build -trimpath -ldflags="-s -w" -o qoder-proxy ./cmd/qoder-proxy
cp config.example.json config.json
```

交叉編譯（Linux amd64，靜態）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o qoder-proxy-linux-amd64 ./cmd/qoder-proxy
```

實測產物：7.3 MB 靜態 ELF，空載記憶體約 11 MB。

預設只監聽 `127.0.0.1:8080`。設定為 JSON 檔，憑證獨立存 `credentials.json`。

## 登入與憑證

### 瀏覽器 Device Flow（僅國際版）

```bash
./qoder-proxy login --region global --config config.json
```

本地產生 PKCE verifier、challenge、nonce 與 machine ID，只打印授權 URL；含 verifier 的輪詢 URL 不會輸出。輪詢 5 分鐘逾時。

**多帳號**：重複執行此命令、每次用**不同瀏覽器帳號**（建議無痕視窗）授權即可。新帳號寫入憑證檔後，需重啟 `serve` 讓池子重新載入。

⚠️ 時序提醒：`login` 寫檔與運行中的 `serve` 共用 `credentials.json`。若服務在「寫入後、重啟前」恰好觸發 token 刷新，會用記憶體中的舊清單**整檔覆寫**、洗掉新帳號。憑證未到刷新窗口時風險趨近於零，但**登入完成後請立即重啟服務**。

### PAT 匯入（國際版或中國區）

交錯式隱藏輸入：

```bash
./qoder-proxy import-pat --region global --config config.json
./qoder-proxy import-pat --region cn --config config.json
```

或僅對該命令設環境變數：

```bash
QODER_PAT='pt-...' ./qoder-proxy import-pat --region global --config config.json
QODERCN_PAT='pt-...' ./qoder-proxy import-pat --region cn --config config.json
```

PAT 只用於交換 Job Token，預設不落盤。磁碟僅存 access token、refresh token、使用者／機器識別碼與 Unix 毫秒到期時間。採同目錄暫存檔、`fsync`、原子 rename，強制 `0600`；Unix 下權限更寬鬆會拒絕讀取。

本工具不會掃描或匯入 `~/.qoder`、其他 QoderGateway 資料庫或任何本機既有憑證。

## 啟動

```bash
./qoder-proxy serve --config config.json
```

監聽非迴環位址**必須**提供代理自身的 API Key，否則拒絕啟動：

```bash
QODER_PROXY_API_KEY='use-a-long-random-value' ./qoder-proxy serve --config config.json
```

客戶端可用任一头：`Authorization: Bearer <key>` 或 `X-API-Key: <key>`。比對為 SHA-256 後的常量時間比較。`/healthz`、`/readyz` 為免-Key 探針；`/v1/*` 與 `/admin/*` 受 Key 保護。

### systemd 常駐

```ini
[Unit]
Description=Qoder Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/qoder-proxy
ExecStart=/opt/qoder-proxy/qoder-proxy serve --config /opt/qoder-proxy/config.json
Environment=QODER_PROXY_API_KEY=<long-random-value>
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 以 Docker 運行本代理

先在宿主機完成登入並確保憑證 `0600`：

```bash
docker build -t qoder2api-plus:local .
cp config.docker.example.json config.docker.json
docker run --rm -p 127.0.0.1:8080:8080 \
  -e QODER_PROXY_API_KEY='use-a-long-random-value' \
  -v "$PWD/config.docker.json:/app/config.json:ro" \
  -v "$PWD/credentials.json:/app/credentials.json" \
  qoder2api-plus:local
```

容器內需在配置中監聽 `0.0.0.0:8080`，因此必須提供 `QODER_PROXY_API_KEY`。

## 端點

| 方法 | 路徑 | 說明 |
|---|---|---|
| `GET` | `/healthz` | 存活探針，免 Key |
| `GET` | `/readyz` | 就緒與降級狀態，免 Key |
| `GET` | `/v1/models` | 即時模型目錄（`enable=true`） |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions，串流／非串流 |
| `GET` | `/admin/status` | 每帳號池狀態：`healthy`／`cooldown`／`disabled`／`auth_error`、傳輸方式、冷卻截止 |
| `GET` | `/admin/quota` | 每帳號真實餘額：`total`／`used`／`remaining`／`quota_exceeded`，以及 `add_on_packages`（活動贈送的資源包） |

兩個 admin 端點只回傳**匿名帳號 ID** 與手工構造的視圖；`credential.Account`（含 token 欄位）絕不直接被序列化進響應，測試有釘死此點。非 `healthy` 帳號不打上游，僅標記 `skipped`。

```bash
curl -s http://127.0.0.1:8080/admin/status -H "Authorization: Bearer $PROXY_KEY"
```

## 調用範例

```bash
curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $PROXY_KEY"
```

串流：

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $PROXY_KEY" \
  -H 'X-Session-Key: my-conversation' \
  -d '{
    "model": "lite",
    "messages": [{"role":"user","content":"你好"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'
```

非串流：

```bash
curl -s http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $PROXY_KEY" \
  -d '{"model":"lite","messages":[{"role":"user","content":"只回覆 OK"}],"stream":false}'
```

對外模型 ID 使用即時目錄回傳的穩定 `key`，`display_name` 僅供展示；不把顯示名稱改寫成未經證明的別名。

## Bearer 與 COSY 選擇規則

`transport` 可為 `auto`／`bearer`／`cosy`。`auto` 依現有證據路由：

- `auto`、`ultimate`、`performance`、`efficient`、`lite` 等路由檔位優先 Bearer。
- 即時目錄中的具體模型 key 優先 COSY／API3，並攜帶完整 `model_config`。
- 國際版 Bearer 先以無副作用 `OPTIONS` 探測；只有 404 才判定不可用，405 反而證明路由存在。
- 中國區缺乏 Bearer 推理端點證據，自動走 COSY。
- 選擇結果按帳號與能力類別快取於記憶體。

僅在實際 POST 明確回 404/405、且尚未開始讀取響應流時，才允許 Bearer → COSY。

錯誤處理（本 fork 修正處以 **粗體** 標記）：

- `401` 只刷新一次。
- **`403` 先分類**：含 `code 112` 或 `pricingUrl` 者為**模型級配額拒絕**——帳號保持健康、改試下一候選，全部候選耗盡後回 `403 quota_exceeded`；其餘 403 仍視為帳號級故障並停用。
- **停用可恢復**：`disabled` 帶時間戳，預設 5 分鐘後由 `Snapshot()` 惰性轉回 `healthy`（與 `cooldown` 同機制）；亦可由 `Pool.Recover()` 立即恢復。
- **串流路徑同樣分類**：國際版上游恆以 `stream=true` 開流，配額錯誤實際以 SSE 信封內嵌（HTTP 200 + 流內 `statusCodeValue:403`）抵達，現已型別化為 `*EnvelopeError` 並向上回報 `quota_exceeded`，而非早期的通用 `upstream_stream_error`。
- `429` 依 `Retry-After` 或預設時間冷卻。
- `5xx` 在未出流前有限換帳號。網路錯誤、總timeout與結果不確定的錯誤不跨 transport 重試。
- 一旦取得 200 流響應，解析失敗、超時或斷線均不換帳號、不換協議重放。

## 模型目錄與 readiness

目錄來自各區對應的 COSY `/algo/api/v2/model/list?Encode=1`：

- 只暴露 `enable=true`。
- 保存並回傳完整原始 `model_config`。
- 解析 `display_name`、`is_vl`、`is_reasoning`、`thinking_config`、`source`。
- context window 優先取 `context_config` 最大 `token_count`，否則用 `max_input_tokens`。
- 記憶體快取一小時；刷新失敗續用上次成功快取。
- 從未成功取得時只暴露保守的 `lite / 180000` 靜態項，並在 `/v1/models` 與 `/readyz` 標記 `degraded=true`。

不憑猜測宣稱 1M context 或 128K output。

## 維運命令

```bash
./qoder-proxy models --config config.json
./qoder-proxy doctor --config config.json
```

`doctor` 檢查憑證權限、userinfo、quota、Bearer/COSY 能力與模型目錄，輸出只使用匿名 ID。本 fork 修正配置校驗範圍後，`doctor`／`login`／`import-pat`／`models` 在非迴環 Key 缺失時不再被誤擋。

## 接入 Docker 化的 API 聚合層

若上游是跑在 Docker 裡的 new-api / one-api / CPA 等聚合層，有兩層連通問題（均為本 fork 實測踩過）：

1. **容器內的 `127.0.0.1` 指向容器自身**，不是宿主機。渠道 Base URL 填 `127.0.0.1:8080` 必然失敗（new-api 側報 `do request failed`）。
2. **本代理綁 `127.0.0.1` 時容器同樣打不通**：容器發起的封包到達宿主機的 `docker0` 界面，核心找不到綁在 loopback 上的 socket，直接 `Connection refused`。

正確做法：`config.json` 的 `listen` 改為 `0.0.0.0:8080`（非迴環需一併提供 `QODER_PROXY_API_KEY`，否則 `serve` 拒絕啟動），渠道 Base URL 填宿主機在該容器網絡上的閘道位址：

```bash
docker network inspect <network> --format '{{range .IPAM.Config}}gw={{.Gateway}}{{end}}'
# 例：gw=172.18.0.1  ->  http://172.18.0.1:8080/v1
```

本實測環境中 `172.18.0.1` 僅存在於 docker bridge 內部，外部網路不可路由。若部署有公網界面，`0.0.0.0` 綁定會連同公網一起暴露——出口限制請自行處理，本倉庫不自帶防火牆規則。

> 別指望 `host.docker.internal`：Linux 下需在 compose 加 `extra_hosts: host.docker.internal:host-gateway`，既有堆疊未必有；閘道 IP 最直接。

## 測試

預設測試不觸網：

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...   # 需 CGO
go build ./...
```

本 fork 新增 14 個測試（配額 403 分類與帳號保活、停用自動恢復、`Recover()` 契約、admin 端點含 Token 洩漏防護斷言、SSE 信封型別化、配置校驗範圍回歸）。

⚠️ **race 檢測尚未補跑**——本 fork 的兩處新併發代碼（`AdminEntries` 並行抓 quota、`disabledUntil` 惰性轉移）沿用上游既有鎖模式撰寫，但尚未經 `-race` 驗證。需 `CGO_ENABLED=1` 與 C 工具鏈。

真實測試需顯式開啟並指向專用 `0600` 憑證檔，會實際消耗額度，勿在一般 CI 啟用：

```bash
QODER_LIVE_TEST=1 \
QODER_LIVE_CREDENTIALS=/absolute/path/to/test.credentials.json \
go test ./internal/qoder -run TestLiveBearerOptIn -v
```

## 已知限制

- 國際版上游端點（`api2-v2`／`api3`／`openapi.qoder.sh`）為未公開協定，隨時可能變動；端點常數集中在 `internal/protocol/constants.go`，變更時改此檔與 fixtures。
- 中國區僅支援 PAT 匯入，Device Flow 證據不足。
- 支援 HTTP/HTTPS CONNECT 代理，不支援 SOCKS5（標準庫無內建 dialer）。
- 模型目錄只快取於記憶體，重啟後重新取得。
- 串流已發出 HTTP 200 後遇上游 malformed／斷線，會發送帶 `partial=true` 的 SSE error 與單一 `[DONE]`，HTTP 狀態無法改寫。
- 無 Anthropic／Responses 格式（上游刻意不做，本 fork 經評估後維持不實作）。
- 帳號額度為零時僅免費檔位可用；本倉庫不含任何獎勵領取自動化。

## 授權與來源

⚠️ **上游 [J-York/QoderProxy](https://github.com/J-York/QoderProxy) 未附授權檔**（GitHub 標示 license 為 `NOASSERTION`／無）。在未獲授權前，本倉庫僅為**公開研究與自用**的衍生實作，不構成再授權。如需正式授權使用或分發，請向上游作者取得 LICENSE；取得後本節將隨之更新。

協議逆向成果與其固定 commit 見 [REFERENCES.md](REFERENCES.md)；Qoder 協議知識產權屬其開發者。原始 Go 實作歸 J-York/QoderProxy 作者，本 fork 的修改見 [FORK-PLAN.md](FORK-PLAN.md) 與 commit 歷史。
