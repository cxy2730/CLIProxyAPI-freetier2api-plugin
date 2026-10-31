package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/hostbridge"
)

// 本文件实现 Cline 的 chat-completions 转发。
//
// Cline 上游就是标准 OpenAI chat-completions，请求体**不需要改写**——
// 唯一需要动的是请求头（认证 + 客户端标识），以及补上上游要求的
// session_id 与 reasoning_effort 两个字段。
//
// 这两个字段是上游的硬要求（实测缺失会被拒），因此在转发前注入；
// 客户端已经给了值时不覆盖。

// chatHTTPTimeout 是非流式对话的上限。
const chatHTTPTimeout = 10 * time.Minute

// ChatRequest 是一次对话转发的输入。
type ChatRequest struct {
	// BearerToken 是可直接放进 Authorization 的令牌（含 workos: 前缀）。
	BearerToken string
	// Body 是宿主翻译好的 chat-completions 请求体。
	Body []byte
	// Stream 报告是否流式。
	Stream bool
}

// Chat 执行一次非流式对话，返回上游的完整响应体。
func Chat(ctx context.Context, baseURL string, req ChatRequest) ([]byte, error) {
	body, errPrepare := prepareBody(req.Body)
	if errPrepare != nil {
		return nil, errPrepare
	}
	resp, errDo := doChat(ctx, baseURL, req, body, chatHTTPTimeout)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()

	payload, errRead := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if errRead != nil {
		return nil, &core.Failure{Status: resp.StatusCode, Msg: "read chat response: " + errRead.Error(), Kind: core.FailureKindTransient}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, Classify(resp.StatusCode, string(payload))
	}
	return payload, nil
}

// ChatStream 执行一次流式对话，返回上游响应体（调用方负责关闭）。
func ChatStream(ctx context.Context, baseURL string, req ChatRequest) (*http.Response, error) {
	body, errPrepare := prepareBody(req.Body)
	if errPrepare != nil {
		return nil, errPrepare
	}
	// 超时传 0：流式响应的总时长不可预期，一次性超时会把长回答截断。
	resp, errDo := doChat(ctx, baseURL, req, body, 0)
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, Classify(resp.StatusCode, string(payload))
	}
	return resp, nil
}

// doChat 发起一次对话请求。
func doChat(ctx context.Context, baseURL string, req ChatRequest, body []byte, timeout time.Duration) (*http.Response, error) {
	endpoint := BaseURL(baseURL) + ChatPath
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errReq != nil {
		return nil, fmt.Errorf("build chat request: %w", errReq)
	}
	ApplyChatHeaders(httpReq, req.BearerToken, NewSessionID())

	var client *http.Client
	if timeout > 0 {
		client = hostbridge.Client(ctx, timeout)
	} else {
		client = hostbridge.StreamClient(ctx, 0)
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return nil, &core.Failure{Msg: "chat: " + errDo.Error(), Kind: core.FailureKindTransient}
	}
	return resp, nil
}

// prepareBody 注入上游要求的字段。
//
// 注入两个字段：
//   - session_id：上游用它做会话亲和（缺了会被当成新会话，影响缓存命中）；
//   - reasoning_effort：上游对推理档位的默认值有要求，缺失时部分模型拒绝。
//
// **不覆盖客户端已给的值**：用户显式指定了就要尊重，插件只补默认。
func prepareBody(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		// 解析不了就原样转发：上游会给出比我们更准确的错误。
		return raw, nil
	}
	changed := false
	if _, okSession := payload["session_id"]; !okSession {
		payload["session_id"] = NewSessionID()
		changed = true
	}
	if _, okEffort := payload["reasoning_effort"]; !okEffort {
		payload["reasoning_effort"] = "high"
		changed = true
	}
	if !changed {
		return raw, nil
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return raw, nil
	}
	return encoded, nil
}

// 本文件定义 Cline 的上游端点与请求头。
//
// 端点与请求头形态移植自 cline2api 的实测值（auth.go 与 admin.go）：
// Cline 上游是标准 OpenAI chat-completions，但带一整套客户端标识头，
// 缺了会被识别成非官方客户端。

const (
	// DefaultBaseURL 是 Cline 的上游基地址。
	DefaultBaseURL = "https://api.cline.bot/api/v1"
	// ChatPath 是 chat-completions 的路径。
	ChatPath = "/chat/completions"
	// ModelsPath 是推荐模型清单的路径（**无需认证**）。
	ModelsPath = "/ai/cline/recommended-models"
	// RefreshPath 是令牌续期的路径。
	RefreshPath = "/auth/refresh"
	// RegisterPath 是用 WorkOS 令牌换 Cline 令牌的路径。
	RegisterPath = "/auth/register"

	// DefaultClientVersion 是伪装用的 Cline 客户端版本。
	//
	// 2026-10-31 对齐官方最新正式版（github.com/cline/cline releases/latest
	// = v4.1.26，原值 3.0.74 已落后两个大版本）。
	DefaultClientVersion = "4.1.26"
	// DefaultCoreVersion 是伪装用的 Cline 内核版本。
	DefaultCoreVersion = "0.0.66"
)

// BaseURL 返回上游基地址（末尾无斜杠）。
//
// 允许配置覆盖：上游换域名或用户要走自建网关时不必改代码。
func BaseURL(override string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(override), "/")
	if trimmed == "" {
		return DefaultBaseURL
	}
	return trimmed
}

// UserAgent 返回伪装成官方 Cline 客户端的 UA。
func UserAgent() string {
	return "Cline/" + DefaultClientVersion
}

// ApplyChatHeaders 写入 chat-completions 请求所需的全部头。
//
// 除认证外的这一整套 X-CLIENT-* / X-PLATFORM 头是「更像官方客户端」的
// 伪装层。实测上游只硬要求 Authorization 与 Content-Type，但少这些头
// 会更容易被风控盯上，因此照官方客户端的形态补齐。
func ApplyChatHeaders(req *http.Request, bearerToken, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("User-Agent", UserAgent())
	req.Header.Set("HTTP-Referer", "https://cline.bot")
	req.Header.Set("X-Title", "Cline")
	req.Header.Set("X-Task-ID", sessionID)
	req.Header.Set("X-IS-MULTIROOT", "false")
	req.Header.Set("X-CLIENT-TYPE", "cline-cli")
	req.Header.Set("X-CLIENT-VERSION", DefaultClientVersion)
	req.Header.Set("X-PLATFORM", "terminal")
	req.Header.Set("X-PLATFORM-VERSION", DefaultClientVersion)
	req.Header.Set("X-CORE-VERSION", DefaultCoreVersion)
}

// ApplyJSONHeaders 写入普通 JSON 请求的头（续期、注册）。
//
// 登录与续期链路**不带**客户端伪装头：实测上游的 /auth/* 只认 Content-Type，
// 多余的伪装头反而与官方客户端的行为不一致。
func ApplyJSONHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
}

// ApplyFormHeaders 写入表单请求的头（WorkOS 设备码流程）。
func ApplyFormHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
}

// NewSessionID 生成一个会话 ID（上游要求 sess_<毫秒时间戳> 形态）。
func NewSessionID() string {
	return "sess_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// 本文件解析 Cline 的供应商专属配置。
