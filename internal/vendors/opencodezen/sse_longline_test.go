package opencodezen

import (
	"strings"
	"testing"
)

// 单条 SSE 行超过 64KB（默认 Scanner 上限）时不得截断。
func TestCollapseSSEToJSONHandlesLongLine(t *testing.T) {
	long := strings.Repeat("a", 200*1024)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + long + "\"}}]}\n\ndata: [DONE]\n\n"
	out, err := CollapseSSEToJSON(strings.NewReader(body), "m")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(out), long[:1000]) || len(out) < 200*1024 {
		t.Fatalf("content truncated, len=%d", len(out))
	}
}
