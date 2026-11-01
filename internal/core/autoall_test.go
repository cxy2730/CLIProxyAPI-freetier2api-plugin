package core

import (
	"context"
	"errors"
	"testing"

	"freetier2api-plugin/cpasdk/pluginapi"
)

// stubVendor 只实现 Vendor 的最小可编译面：本文件只关心签到折叠，
// 对话相关方法一律留空，被调用即说明测试用错了路径。
type stubVendor struct{}

func (stubVendor) ID() string                                { return "stub" }
func (stubVendor) Name() string                              { return "Stub" }
func (stubVendor) Region() string                            { return "cn" }
func (stubVendor) ModelPrefix() string                       { return "" }
func (stubVendor) Match(string, string, map[string]any) bool { return false }
func (stubVendor) Parse([]byte, string) (*Credential, error) { return nil, errors.New("stub") }
func (stubVendor) StaticModels(context.Context) ([]pluginapi.ModelInfo, error) {
	return nil, nil
}
func (stubVendor) Execute(context.Context, *Credential, *ExecuteRequest) (*pluginapi.ExecutorResponse, error) {
	return nil, errors.New("stub")
}
func (stubVendor) ExecuteStream(context.Context, *Credential, *ExecuteRequest, StreamSink) error {
	return errors.New("stub")
}

type stubCheckin struct {
	stubVendor
	supports bool
	result   *CheckinResult
	err      error
	called   int
}

func (s *stubCheckin) Checkin(context.Context, *Credential) (*CheckinResult, error) {
	s.called++
	return s.result, s.err
}

func (s *stubCheckin) SupportsCheckin() bool { return s.supports }

type stubCheckinCap struct {
	stubCheckin
	capable bool
}

func (s *stubCheckinCap) SupportsCheckinFor(*Credential) bool { return s.capable }

// CheckinUnsupportedReason 是 CheckinCapability 的第二个方法；少了它
// 这个 stub 根本不满足该接口，As 断言会静默失败、跳过分支就测不到。
func (s *stubCheckinCap) CheckinUnsupportedReason(*Credential) string {
	if s.capable {
		return ""
	}
	return "该账号不支持签到"
}

func TestCheckinAutoTaskSkipsUnsupportedVendor(t *testing.T) {
	vendor := &stubCheckin{supports: false}
	if row := CheckinAutoTask(context.Background(), vendor, &Credential{}); row != nil {
		t.Fatalf("不支持签到的供应商应返回 nil，得到 %+v", row)
	}
	if vendor.called != 0 {
		t.Fatalf("不支持签到不应发起调用，实际调用 %d 次", vendor.called)
	}
}

func TestCheckinAutoTaskSkipsAccountWithoutCapability(t *testing.T) {
	vendor := &stubCheckinCap{stubCheckin: stubCheckin{supports: true, result: &CheckinResult{}}, capable: false}
	if row := CheckinAutoTask(context.Background(), vendor, &Credential{}); row != nil {
		t.Fatalf("账号级不支持应返回 nil，得到 %+v", row)
	}
	if vendor.called != 0 {
		t.Fatalf("账号级不支持不应发起调用，实际调用 %d 次", vendor.called)
	}
}

func TestCheckinAutoTaskMapsStatus(t *testing.T) {
	cases := []struct {
		name       string
		result     *CheckinResult
		wantStatus string
		wantMsg    string
	}{
		{
			name:       "新签到是 done",
			result:     &CheckinResult{Credit: 100, Message: "签到成功 +100"},
			wantStatus: "done",
			wantMsg:    "签到成功 +100",
		},
		{
			name:       "已签到是 skipped",
			result:     &CheckinResult{Already: true},
			wantStatus: "skipped",
			wantMsg:    "今日已签到",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			vendor := &stubCheckin{supports: true, result: testCase.result}
			row := CheckinAutoTask(context.Background(), vendor, &Credential{})
			if row == nil {
				t.Fatal("应产出一行")
			}
			if row.Code != AutoTaskCodeCheckin {
				t.Fatalf("Code = %q", row.Code)
			}
			if row.Status != testCase.wantStatus {
				t.Fatalf("Status = %q, want %q", row.Status, testCase.wantStatus)
			}
			if row.Message != testCase.wantMsg {
				t.Fatalf("Message = %q, want %q", row.Message, testCase.wantMsg)
			}
			if testCase.result.Credit != 0 && row.Credit != testCase.result.Credit {
				t.Fatalf("Credit = %d", row.Credit)
			}
		})
	}
}

func TestCheckinAutoTaskReportsError(t *testing.T) {
	vendor := &stubCheckin{supports: true, err: errors.New("上游 500")}
	row := CheckinAutoTask(context.Background(), vendor, &Credential{})
	if row == nil || row.Status != "error" || row.Message != "上游 500" {
		t.Fatalf("错误应折叠成 error 行，得到 %+v", row)
	}
}
