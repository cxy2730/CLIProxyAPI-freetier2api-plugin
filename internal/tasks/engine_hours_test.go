package tasks

import (
	"reflect"
	"testing"
)

func TestMergeHourUsesNormalizeRules(t *testing.T) {
	tests := []struct {
		name  string
		hours []int
		hour  int
		want  []int
	}{
		{name: "空列表", hours: nil, hour: 9, want: []int{9}},
		{name: "去重", hours: []int{9}, hour: 9, want: []int{9}},
		{name: "排序", hours: []int{21, 9}, hour: 15, want: []int{9, 15, 21}},
		// 自己写一份去重排序时不会校验边界，24 点会静默留在排程里，永远不触发。
		{name: "越界被丢弃", hours: []int{9}, hour: 24, want: []int{9}},
		{name: "负值被丢弃", hours: []int{9}, hour: -1, want: []int{9}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mergeHour(test.hours, test.hour)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mergeHour(%v, %d) = %v, want %v", test.hours, test.hour, got, test.want)
			}
		})
	}
}

// mergeHour 不能改动入参切片背后的数组：effectiveSchedule 会把配置里的切片
// 直接传进来，原地排序会污染后续调用。
func TestMergeHourDoesNotMutateInput(t *testing.T) {
	hours := make([]int, 2, 4)
	hours[0], hours[1] = 21, 9
	backing := hours[:cap(hours)]
	_ = mergeHour(hours, 15)
	if backing[0] != 21 || backing[1] != 9 {
		t.Fatalf("入参被就地修改: %v", backing[:2])
	}
}
