package app

// freetier —— 多供应商免费额度插件。
//
// 本文件包含插件身份、RPC 方法分发与注册响应。C ABI 在 cgo.go；
// 供应商注册表由 App 构造，组合根在 vendors.go。

import (
	"encoding/json"
	"fmt"
	"strings"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/logger"
)

const (
	pluginID          = "freetier"
	pluginDisplayName = "FreeTier 2API"
	pluginAuthor      = "cxy2730"
	pluginRepository  = "https://github.com/cxy2730/CLIProxyAPI-freetier2api-plugin"

	providerKey           = core.ProviderKey
	managementRoutePrefix = "/plugins/" + pluginID
	jsonContentType       = "application/json; charset=utf-8"
	htmlContentType       = "text/html; charset=utf-8"
	formatChatCompletions = core.FormatChatCompletions
)

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ResponseBeforeTranslator bool `json:"response_before_translator"`

	ModelProvider      bool   `json:"model_provider"`
	AuthProvider       bool   `json:"auth_provider"`
	Executor           bool   `json:"executor"`
	QuotaProvider      bool   `json:"quota_provider"`
	ManagementAPI      bool   `json:"management_api"`
	ExecutorModelScope string `json:"executor_model_scope"`
	// ExecutorAuthProviders 留空：本插件的凭证全部由插件自己管理，
	// 不需要宿主把 Home（antigravity）选中的凭证透传进 Executor。
	// 宿主侧语义：非空时该插件参与 Home 分发（internal/pluginhost/executor_route.go）。
	ExecutorAuthProviders []string `json:"executor_auth_providers,omitempty"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

// HandleMethod 是插件 ABI 的总入口。
func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		return a.handlePluginRegister(request)
	case pluginabi.MethodPluginReconfigure:
		return a.handlePluginReconfigure(request)
	case pluginabi.MethodPluginQuiesce:
		a.StopBackgroundWork()
		return core.OKEnvelope(struct{}{})

	case pluginabi.MethodModelRegister:
		return a.handleModelRegister(request)
	case pluginabi.MethodModelStatic:
		return a.handleModelStatic(request)
	case pluginabi.MethodModelForAuth:
		return a.handleModelForAuth(request)

	case pluginabi.MethodAuthIdentifier:
		// 面板「OAuth 登录」列表只渲染 oauth_provider 非空的插件（前端过滤
		// `!r` 直接跳过），所以这里不发布任何 OAuth 标识：FreeTier 2API 不出现
		// 在该列表里，登录/添加账号只走插件自己的控制台。
		// 但必须同时用 credential_providers 认领 auth 文件里的 type=freetier，
		// 否则宿主的 authProviderEntry 会认不出这些凭证，模型与执行器全部掉线。
		return core.OKEnvelope(identifierResponse{CredentialProviders: []string{providerKey}})
	case pluginabi.MethodAuthParse:
		return a.handleAuthParse(request)
	case pluginabi.MethodAuthLoginStart:
		return a.handleAuthLoginStart(request)
	case pluginabi.MethodAuthLoginPoll:
		return a.handleAuthLoginPoll(request)
	case pluginabi.MethodAuthRefresh:
		return a.handleAuthRefresh(request)

	case pluginabi.MethodExecutorIdentifier:
		return core.OKEnvelope(identifierResponse{Identifier: providerKey})
	case pluginabi.MethodExecutorExecute:
		return a.handleExecutorExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return a.handleExecutorExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return a.handleExecutorCountTokens(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return a.handleExecutorHTTPRequest(request)
	case pluginabi.MethodResponseNormalizeBefore:
		return a.handleResponseNormalizeBefore(request)

	case pluginabi.MethodQuotaIdentifier:
		return core.OKEnvelope(identifierResponse{Identifier: providerKey})
	case pluginabi.MethodQuotaDescribe:
		return a.handleQuotaDescribe(request)
	case pluginabi.MethodQuotaFetch:
		return a.handleQuotaFetch(request)
	case pluginabi.MethodQuotaReset:
		return a.handleQuotaReset(request)

	case pluginabi.MethodManagementRegister:
		return core.OKEnvelope(a.managementRegistration())
	case pluginabi.MethodManagementHandle:
		return a.handleManagement(request)

	default:
		return core.ErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type identifierResponse struct {
	Identifier          string   `json:"identifier"`
	Identifiers         []string `json:"identifiers,omitempty"`
	CredentialProviders []string `json:"credential_providers,omitempty"`
}

func (a *App) handlePluginRegister(request []byte) ([]byte, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	cfg, errConfig := a.decodeConfig(configYAMLFrom(request))
	if errConfig != nil {
		return nil, fmt.Errorf("plugin.register: %w", errConfig)
	}
	if errApply := a.ApplyConfig(cfg); errApply != nil {
		return nil, fmt.Errorf("plugin.register: %w", errApply)
	}
	if _, errState := a.loadState(cfg); errState != nil {
		logger.Error("加载状态失败：%v", errState)
	}
	a.noteHostSchema(hostSchemaFrom(request))
	a.applyModelCatalog(cfg)
	if !a.registered {
		a.registered = true
		a.StartBackgroundWork()
	}
	logger.Info("已注册：区域=%v 状态目录=%s", cfg.EnabledRealms, cfg.StateDir)
	return core.OKEnvelope(a.Registration())
}

func (a *App) handlePluginReconfigure(request []byte) ([]byte, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	cfg, errConfig := a.decodeConfig(configYAMLFrom(request))
	if errConfig != nil {
		return nil, fmt.Errorf("plugin.reconfigure: %w", errConfig)
	}
	a.noteHostSchema(hostSchemaFrom(request))
	previous := a.Config()
	if errApply := a.ApplyConfig(cfg); errApply != nil {
		return nil, fmt.Errorf("plugin.reconfigure: %w", errApply)
	}
	if previous.StateDir != cfg.StateDir {
		if _, errState := a.loadState(cfg); errState != nil {
			logger.Error("重新加载状态失败：%v", errState)
		}
	}
	a.applyModelCatalog(cfg)
	logger.Info("已重新配置：区域=%v 状态目录=%s", cfg.EnabledRealms, cfg.StateDir)
	return core.OKEnvelope(a.Registration())
}

// hostSchemaFrom 读取宿主在注册/重配置请求里声明的契约版本。
//
// 宿主字段缺失（很老的版本）时返回 0，由协商函数按最低契约处理。
func hostSchemaFrom(raw []byte) uint32 {
	if len(raw) == 0 {
		return 0
	}
	var req lifecycleRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return 0
	}
	return req.SchemaVersion
}

// noteHostSchema 记录宿主声明的契约版本，供 Registration 协商。
//
// 宿主只在 plugin.register / plugin.reconfigure 里送这个字段；重配置时重记一次，
// 免得热重载后回落到默认值。
func (a *App) noteHostSchema(host uint32) {
	a.hostSchema.Store(host)
}

// negotiatedSchemaVersion 把本插件支持的契约版本压到宿主能接受的范围内。
//
// 宿主侧的校验是「插件声明的版本 > 宿主版本就拒绝加载」，所以插件不能无条件
// 报最新版本：CPA 8.0.0 的契约是 6，8.1.0 是 10，跨版本分发同一份插件时必须
// 按宿主降级，否则老宿主直接判定「schema version not supported」。
func negotiatedSchemaVersion(host uint32) uint32 {
	if host == 0 {
		// 字段缺失＝最老的宿主，只认最初的契约。
		return 1
	}
	if host > pluginabi.SchemaVersion {
		return pluginabi.SchemaVersion
	}
	return host
}

func configYAMLFrom(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var req lifecycleRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil
	}
	return req.ConfigYAML
}

// Registration 构造注册响应。
func (a *App) Registration() registration {
	return registration{
		SchemaVersion: negotiatedSchemaVersion(a.hostSchema.Load()),
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          a.version,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepository,
		},
		Capabilities: registrationCapabilities{
			ResponseBeforeTranslator: true,

			ModelProvider: true,
			AuthProvider:  true,
			Executor:      true,
			QuotaProvider: true,
			ManagementAPI: true,
			// 本插件的模型全部按凭证下发（免费福利额度是按账号、按模型授予的），
			// 没有「无凭证也可以用的静态目录」。声明 both 会让宿主把 static 目录
			// 额外注册成一个 model-registry 客户端（plugin:<id>:<provider>:executor），
			// 与 model.for_auth 的客户端各记一次引用；删除凭证只注销后者，前者留在
			// 注册表里就是 /v1/models 上永不消失的幽灵模型（issue #58）。
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  []string{formatChatCompletions},
			ExecutorOutputFormats: []string{formatChatCompletions},
		},
	}
}

func decodeRequest(raw []byte, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if errUnmarshal := json.Unmarshal(raw, target); errUnmarshal != nil {
		return fmt.Errorf("decode request: %w", errUnmarshal)
	}
	return nil
}

func isChatFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return true
	}
	return false
}
