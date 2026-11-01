package codearts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"freetier2api-plugin/internal/core"
)

// 额度查询：获取 CodeArts 账号可用状态与额度概览。

// 本文件实现 CodeArts 凭证的解析、特征识别与序列化。
// 参考：/Users/jiandan/Workspaces/codearts2api/internal/auth/auth.go

type Credential struct {
	Vendor          string            `json:"vendor,omitempty"`
	UserID          string            `json:"user_id,omitempty"`
	UserName        string            `json:"user_name,omitempty"`
	DomainID        string            `json:"domain_id,omitempty"`
	AccessKeyID     string            `json:"access_key_id,omitempty"`
	SecretAccessKey string            `json:"secret_access_key,omitempty"`
	SecurityToken   string            `json:"security_token,omitempty"`
	RefreshToken    string            `json:"refresh_token,omitempty"`
	CodeVerifier    string            `json:"code_verifier,omitempty"`
	ClientID        string            `json:"client_id,omitempty"`
	Expiration      string            `json:"expiration,omitempty"`
	ExpiresAt       int64             `json:"expires_at,omitempty"`
	DPoPPrivateKey  map[string]string `json:"dpop_private_key,omitempty"`
	Label           string            `json:"label,omitempty"`
}

// ParseCredential 解析 JSON 凭证。
func ParseCredential(raw []byte) (*Credential, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("codearts credential is empty")
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode codearts credential json: %w", err)
	}

	c := &Credential{
		Vendor:          core.FirstNonEmpty(core.GetString(m, "vendor"), VendorID),
		UserID:          core.FirstNonEmpty(core.GetString(m, "user_id"), core.GetString(m, "userId")),
		UserName:        core.FirstNonEmpty(core.GetString(m, "user_name"), core.GetString(m, "userName")),
		DomainID:        core.FirstNonEmpty(core.GetString(m, "domain_id"), core.GetString(m, "domainId")),
		AccessKeyID:     core.FirstNonEmpty(core.GetString(m, "access_key_id"), core.GetString(m, "accessKeyId"), core.GetString(m, "ak")),
		SecretAccessKey: core.FirstNonEmpty(core.GetString(m, "secret_access_key"), core.GetString(m, "secretAccessKey"), core.GetString(m, "sk")),
		SecurityToken:   core.FirstNonEmpty(core.GetString(m, "security_token"), core.GetString(m, "securityToken"), core.GetString(m, "cloud_dragon_token")),
		RefreshToken:    core.FirstNonEmpty(core.GetString(m, "refresh_token"), core.GetString(m, "refreshToken")),
		CodeVerifier:    core.FirstNonEmpty(core.GetString(m, "code_verifier"), core.GetString(m, "codeVerifier")),
		ClientID:        core.FirstNonEmpty(core.GetString(m, "client_id"), core.GetString(m, "clientId"), ClientID),
		Expiration:      core.GetString(m, "expiration"),
		ExpiresAt:       core.GetInt64(m, "expires_at", "expiresAt"),
		Label:           core.GetString(m, "label"),
	}
	if c.ExpiresAt == 0 && c.Expiration != "" {
		c.ExpiresAt = parseExpiration(c.Expiration)
	}

	if jwkObj := core.GetMap(m, "dpop_private_key"); jwkObj != nil {
		c.DPoPPrivateKey = make(map[string]string)
		for k, v := range jwkObj {
			if s, ok := v.(string); ok {
				c.DPoPPrivateKey[k] = s
			}
		}
	}

	if c.AccessKeyID == "" && c.RefreshToken == "" {
		return nil, fmt.Errorf("codearts credential missing access_key_id and refresh_token")
	}

	return c, nil
}

// LooksLikeCredential 判断文件或数据是否属于 CodeArts。
func LooksLikeCredential(raw map[string]any, fileName, provider string) bool {
	// 显式 vendor 字段优先：声明了别家就绝不认领。
	if decided, claimed := core.VendorFieldVerdict(raw, VendorID); decided {
		return claimed
	}

	// 文件名前缀匹配（约定集中由 core 维护）。
	if core.FileNameMatchesConvention(fileName, VendorID) {
		return true
	}

	// 特征字段匹配：华为云 AK/SK + SecurityToken 或 DPoP
	if (core.GetString(raw, "access_key_id") != "" && core.GetString(raw, "secret_access_key") != "") || core.GetMap(raw, "dpop_private_key") != nil {
		return true
	}

	return false
}

// StorageJSON 序列化为规范的凭证存储格式。
func (c *Credential) StorageJSON() ([]byte, error) {
	m := map[string]any{
		"type":              "freetier",
		"vendor":            VendorID,
		"user_id":           c.UserID,
		"user_name":         c.UserName,
		"domain_id":         c.DomainID,
		"access_key_id":     c.AccessKeyID,
		"secret_access_key": c.SecretAccessKey,
		"security_token":    c.SecurityToken,
		"refresh_token":     c.RefreshToken,
		"code_verifier":     c.CodeVerifier,
		"client_id":         c.ClientID,
		"expiration":        c.Expiration,
		"expires_at":        c.ExpiresAt,
	}
	if c.DPoPPrivateKey != nil {
		m["dpop_private_key"] = c.DPoPPrivateKey
	}
	if c.Label != "" {
		m["label"] = c.Label
	}
	return json.MarshalIndent(m, "", "  ")
}

// Valid 检查凭据是否在指定的提前量内仍有效。
func (c *Credential) Valid(margin time.Duration) bool {
	if c == nil || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return false
	}
	if c.ExpiresAt <= 0 {
		return true
	}
	return time.Now().Add(margin).Unix() < c.ExpiresAt
}

// parseExpiration 解析华为云 STS 返回的 expiration 时间字符串为 Unix 秒。
func parseExpiration(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}
