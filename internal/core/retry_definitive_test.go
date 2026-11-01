package core

import (
	"fmt"
	"testing"
)

func TestIsDefinitiveFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "硬额度耗尽", err: &Failure{Kind: FailureKindHardCredit}, want: true},
		{name: "凭证失效", err: &Failure{Kind: FailureKindCredential}, want: true},
		{name: "内容拦截", err: &Failure{Kind: FailureKindContentBlocked}, want: true},
		{name: "渠道拦截", err: &Failure{Kind: FailureKindChannelRejected}, want: true},
		{name: "参数错误", err: &Failure{Kind: FailureKindBadParams}, want: true},
		{name: "限流", err: &Failure{Kind: FailureKindRateLimit}, want: false},
		{name: "软限流", err: &Failure{Kind: FailureKindSoftRate}, want: false},
		{name: "上游 5xx", err: &Failure{Kind: FailureKindServer}, want: false},
		{name: "网络抖动", err: &Failure{Kind: FailureKindTransient}, want: false},
		{name: "非 Failure", err: errNotFailure{}, want: false},
		// 凭证侧状态码：重试不会自己好转，必须算确定结论，否则页面会一直
		// 显示上次的正常读数（#71 第二轮复审 P2-4）。
		{name: "凭证 401", err: NewPluginError("unauthorized", "invalid token", 401), want: true},
		{name: "凭证 403", err: NewPluginError("forbidden", "no permission", 403), want: true},
		{name: "包装后的 401", err: fmt.Errorf("quota: %w", NewPluginError("unauthorized", "invalid token", 401)), want: true},
		{name: "上游 500", err: NewPluginError("server", "boom", 500), want: false},
		{name: "可重试 429", err: RetryablePluginError("rate", "slow down", 429), want: false},
		{name: "无状态码", err: NewPluginError("weird", "no status", 0), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsDefinitiveFailure(test.err); got != test.want {
				t.Fatalf("IsDefinitiveFailure(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

type errNotFailure struct{}

func (errNotFailure) Error() string { return "network down" }
