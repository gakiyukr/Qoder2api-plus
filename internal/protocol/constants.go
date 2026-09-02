package protocol

// Protocol constants are intentionally centralized. Qoder's internal protocol
// is undocumented and can change without notice; update this file together
// with fixtures and REFERENCES.md.
const (
	UserAgent          = "qoder-proxy/0.1.0"
	BearerUserAgent    = "qoder/1.1.38"
	GatewayCosyVersion = "1.1.38"
	OpenAPICosyVersion = "1.0.1"
	ClientType         = "5"
	LoginVersion       = "v2"
	DataPolicy         = "disagree"
	DeviceClientID     = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
)

type Region string

const (
	Global Region = "global"
	CN     Region = "cn"
)

type Endpoints struct {
	OpenAPI       string
	BearerChat    string
	CosyChat      string
	ModelList     string
	DeviceLogin   string
	DevicePoll    string
	PATExchange   string
	JobRefresh    string
	DeviceRefresh string
	UserInfo      string
	QuotaUsage    string
}

func ForRegion(region Region) (Endpoints, bool) {
	switch region {
	case Global:
		return Endpoints{
			OpenAPI:       "https://openapi.qoder.sh",
			BearerChat:    "https://api2-v2.qoder.sh/model/v1/chat/completions",
			CosyChat:      "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
			ModelList:     "https://api3.qoder.sh/algo/api/v2/model/list?Encode=1",
			DeviceLogin:   "https://qoder.com/device/selectAccounts",
			DevicePoll:    "https://openapi.qoder.sh/api/v1/deviceToken/poll",
			PATExchange:   "https://openapi.qoder.sh/api/v1/jobToken/exchange",
			JobRefresh:    "https://openapi.qoder.sh/api/v1/jobToken/refresh",
			DeviceRefresh: "https://openapi.qoder.sh/api/v1/deviceToken/refresh",
			UserInfo:      "https://openapi.qoder.sh/api/v1/userinfo",
			QuotaUsage:    "https://openapi.qoder.sh/api/v2/quota/usage",
		}, true
	case CN:
		return Endpoints{
			OpenAPI: "https://openapi.qoder.com.cn",
			// No current primary-source evidence proves a CN api2-v2 endpoint.
			// Auto therefore selects COSY for CN unless a future fixture proves it.
			BearerChat:    "",
			CosyChat:      "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
			ModelList:     "https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1",
			PATExchange:   "https://openapi.qoder.com.cn/api/v1/jobToken/exchange",
			JobRefresh:    "https://openapi.qoder.com.cn/api/v1/jobToken/refresh",
			DeviceRefresh: "https://openapi.qoder.com.cn/api/v1/deviceToken/refresh",
			UserInfo:      "https://openapi.qoder.com.cn/api/v1/userinfo",
			QuotaUsage:    "https://openapi.qoder.com.cn/api/v2/quota/usage",
		}, true
	default:
		return Endpoints{}, false
	}
}

var AllowedUpstreamHosts = map[string]struct{}{
	"api2-v2.qoder.sh":     {},
	"api3.qoder.sh":        {},
	"openapi.qoder.sh":     {},
	"qoder.com":            {},
	"gateway.qoder.com.cn": {},
	"openapi.qoder.com.cn": {},
}
