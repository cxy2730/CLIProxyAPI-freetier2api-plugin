package workbuddy

import "testing"

// 「未挂知识库」字段的类型一致性测试。
//
// 同一个 eventCode（chat_request_send）在四处 payload 里都带 knowledgeId /
// knowledgeName。官方客户端抓包是空字符串，每日活跃信标最初写成了空数组——
// 上游两种都收，但类型不一致意味着任何一次 schema 收紧都只会打断其中一条
// 路径。这里把类型钉死成字符串，避免再次分叉。
func TestKnowledgeFieldsAreStrings(t *testing.T) {
	if _, ok := any(knowledgeIDEmpty).(string); !ok {
		t.Fatal("knowledgeIDEmpty 必须是字符串")
	}
	if _, ok := any(knowledgeNameEmpty).(string); !ok {
		t.Fatal("knowledgeNameEmpty 必须是字符串")
	}
	if knowledgeIDEmpty != "" || knowledgeNameEmpty != "" {
		t.Fatalf("取值应为空串，得到 %q / %q", knowledgeIDEmpty, knowledgeNameEmpty)
	}
}

// TestDailyBonusBeaconKnowledgeFields 验证活跃信标实际发出的类型是字符串。
func TestDailyBonusBeaconKnowledgeFields(t *testing.T) {
	event := DailyBonusBeaconEvent(&Credential{AccessToken: "t", UID: "u-1", Domain: "workbuddy.ai"})
	for _, key := range []string{"knowledgeId", "knowledgeName"} {
		value, exists := event[key]
		if !exists {
			t.Fatalf("信标缺少字段 %s", key)
		}
		if _, isString := value.(string); !isString {
			t.Errorf("%s 类型 = %T, 期望 string", key, value)
		}
	}
}
