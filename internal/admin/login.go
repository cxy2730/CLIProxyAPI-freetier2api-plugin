package admin

// 本文件实现「粘贴 localhost 授权回调链接」的登录兜底入口。
//
// Trae / CodeArts 的上游只认 127.0.0.1 回调。插件与浏览器不在同一台机器时（远程
// 主机、Docker、WSL），那条回调永远到不了，登录会一直卡在「等待授权中」。
// 控制台把本机回调地址明示给用户，用户在浏览器完成授权后把地址栏里的整条链接
// 粘回来，插件就地解析完成登录。

import (
	"net/http"
	"strings"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/hostbridge"
	"freetier2api-plugin/internal/logger"
)

// loginCallbackRequest 是一次回调粘贴请求。
type loginCallbackRequest struct {
	State string `json:"state"`
	URL   string `json:"url"`
}

// handleLoginCallbackRequest 处理用户粘回来的 localhost 回调链接。
//
// 业务失败（会话过期、链接不匹配、上游还没准备好）一律 200 + ok=false 回给
// 页面：页面要的是一句能直接显示的原因，而不是状态码——错误文案由供应商侧
// 生成，已经刻意去掉了回调链接本身（里面带 refreshToken / 票据密钥）。
func (s *Service) handleLoginCallbackRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[loginCallbackRequest](req.Body)
	if errDecode != nil {
		// 页面按 AuthLoginStatus 契约读 status / message，这里不能套用通用的
		// {ok, error} 形状，否则「请刷新页面后重试」这句提示会丢失。
		return JSONResponse(http.StatusBadRequest, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError),
			"message": "请求体解析失败，请刷新页面后重试",
		})
	}
	// 入参校验（state 非空、URL 非空）统一由 App.SubmitLoginCallback 负责：
	// 它是唯一调用供应商的地方，校验放在这里就得再抄一份。
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()

	result, errSubmit := s.deps.SubmitLoginCallback(ctx, body.State, body.URL)
	if errSubmit != nil {
		return JSONResponse(http.StatusOK, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError),
			"message": errSubmit.Error(),
		})
	}

	status := result.Status
	payload := map[string]any{
		"ok":      status == pluginapi.AuthLoginStatusSuccess,
		"status":  string(status),
		"message": result.Message,
	}
	// CodeArts：票据还没换到凭证时，用户还需要在新标签页打开华为云登录页。
	if result.RedirectURL != "" {
		payload["redirect_url"] = result.RedirectURL
	}

	if status == pluginapi.AuthLoginStatusSuccess {
		// 粘贴路径的凭证只回到**页面**，没人替它落盘——正常轮询路径是宿主在
		// auth.login.poll 之后写文件的，这里没有那个宿主环节。不补这一下，页面
		// 显示「登录成功」而 auth 目录里什么都没有。
		if errSave := saveAuthFile(hostbridge.RequestCallbackID(req), result.Auth.FileName, result.Auth.StorageJSON); errSave != nil {
			payload["ok"] = false
			payload["status"] = string(pluginapi.AuthLoginStatusError)
			payload["message"] = "登录已完成，但保存凭证失败：" + errSave.Error()
			logger.Error("登录回调落盘失败（state=%s）：%v", body.State, errSave)
			return JSONResponse(http.StatusOK, payload)
		}
		payload["auth_id"] = result.Auth.FileName
		payload["label"] = result.Auth.Label
	}

	// 落盘成功时 saveAuthFile 已经记过一笔，这里只补非成功分支的审计行。
	if status != pluginapi.AuthLoginStatusSuccess {
		logger.Info("登录回调已处理（state=%s 状态=%s）", body.State, status)
	}
	return JSONResponse(http.StatusOK, payload)
}

// ---- 手机验证码登录（SMSLoginProvider）----
//
// 交互契约与 login/callback 一致：页面按 {ok, status, message} 读结果，
// 业务失败返回 200 而不是状态码——错误文案由供应商侧生成，页面直接显示。

type loginSMSSendRequest struct {
	VendorID string `json:"vendor_id"`
	Phone    string `json:"phone"`
}

type loginSMSVerifyRequest struct {
	VendorID   string `json:"vendor_id"`
	SessionKey string `json:"session_key"`
	Phone      string `json:"phone"`
	Code       string `json:"code"`
	Label      string `json:"label"`
}

// handleLoginSMSSend 处理「发送验证码」。
func (s *Service) handleLoginSMSSend(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[loginSMSSendRequest](req.Body)
	if errDecode != nil {
		return JSONResponse(http.StatusBadRequest, map[string]any{"ok": false, "message": "请求体解析失败，请刷新页面后重试"})
	}
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()
	sessionKey, errSend := s.deps.SendLoginSMS(ctx, strings.TrimSpace(body.VendorID), strings.TrimSpace(body.Phone))
	if errSend != nil {
		return JSONResponse(http.StatusOK, map[string]any{"ok": false, "message": errSend.Error()})
	}
	return JSONResponse(http.StatusOK, map[string]any{
		"ok": true, "session_key": sessionKey,
		"message": "验证码已发送，请查看手机短信",
	})
}

// handleLoginSMSVerify 处理「验证码登录」：换票 + 落盘 + 重载。
func (s *Service) handleLoginSMSVerify(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[loginSMSVerifyRequest](req.Body)
	if errDecode != nil {
		return JSONResponse(http.StatusBadRequest, map[string]any{"ok": false, "message": "请求体解析失败，请刷新页面后重试"})
	}
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()
	fileName, label, storageJSON, errVerify := s.deps.VerifyLoginSMS(ctx,
		strings.TrimSpace(body.VendorID), strings.TrimSpace(body.SessionKey),
		strings.TrimSpace(body.Phone), strings.TrimSpace(body.Code))
	if errVerify != nil {
		return JSONResponse(http.StatusOK, map[string]any{"ok": false, "message": errVerify.Error()})
	}
	if body.Label != "" {
		label = body.Label
	}
	if errSave := saveAuthFile(hostbridge.RequestCallbackID(req), fileName, []byte(storageJSON)); errSave != nil {
		return JSONResponse(http.StatusOK, map[string]any{"ok": false, "message": "登录成功，但保存凭证失败：" + errSave.Error()})
	}
	return JSONResponse(http.StatusOK, map[string]any{
		"ok": true, "auth_id": fileName, "label": label,
		"message": "登录成功，账号已添加",
	})
}

// ---- 页面内登录（人机验证 → 授权链接）----
//
// 有些供应商的 OAuth 授权地址接口强制要求浏览器端人机验证（AutoClaw 国际版
// 的阿里云滑块），令牌只能在用户的浏览器里生成。控制台因此先取控件参数、
// 让用户拖一次滑块，再把得到的令牌交给 /login/start 换授权地址。
//
// 与宿主自带的 <provider>-auth-url 路径的差别：那条路径把上游错误统一折成
// 一句 500（「failed to generate authorization url」），而这里最需要让用户
// 看懂的恰恰是「滑块过期了，重拖一次」——业务失败一律 200 + message 回给页面。

type loginStartRequest struct {
	VendorID string         `json:"vendor_id"`
	Metadata map[string]any `json:"metadata"`
}

// handleLoginStartRequest 发起一次登录并返回授权链接。
func (s *Service) handleLoginStartRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[loginStartRequest](req.Body)
	if errDecode != nil {
		return JSONResponse(http.StatusBadRequest, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError),
			"message": "请求体解析失败，请刷新页面后重试",
		})
	}
	vendorID := strings.TrimSpace(body.VendorID)
	if vendorID == "" {
		return JSONResponse(http.StatusOK, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError), "message": "缺少供应商标识",
		})
	}
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()
	response, errStart := s.deps.StartLogin(ctx, vendorID, body.Metadata)
	if errStart != nil {
		return JSONResponse(http.StatusOK, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError), "message": errStart.Error(),
		})
	}
	if response == nil || strings.TrimSpace(response.URL) == "" {
		return JSONResponse(http.StatusOK, map[string]any{
			"ok": false, "status": string(pluginapi.AuthLoginStatusError),
			"message": "上游没有返回授权链接，请重试",
		})
	}
	payload := map[string]any{
		"ok":    true,
		"url":   response.URL,
		"state": response.State,
	}
	if !response.ExpiresAt.IsZero() {
		payload["expires_at"] = response.ExpiresAt.Unix()
	}
	if response.Metadata != nil {
		if hint, okHint := response.Metadata["callback_hint"].(string); okHint && strings.TrimSpace(hint) != "" {
			payload["callback_hint"] = strings.TrimSpace(hint)
		}
	}
	return JSONResponse(http.StatusOK, payload)
}

// handleLoginCaptchaConfigRequest 取登录前的人机验证控件参数。
func (s *Service) handleLoginCaptchaConfigRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	vendorID := queryString(req.Query, "vendor")
	if vendorID == "" {
		return JSONResponse(http.StatusOK, map[string]any{"ok": false, "message": "缺少 vendor 参数"})
	}
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()
	config, errConfig := s.deps.LoginCaptchaConfig(ctx, vendorID)
	if errConfig != nil {
		return JSONResponse(http.StatusOK, map[string]any{"ok": false, "message": errConfig.Error()})
	}
	return JSONResponse(http.StatusOK, map[string]any{"ok": true, "captcha": config})
}
