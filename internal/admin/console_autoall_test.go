package admin

// 本文件锁住 #75 复审里「一键做任务结果展示」的几条判据。
//
// 这些点共同的坑是「代码能跑、测试不写也全绿」：
//   - P1-1：明细只报「跳过 3」不报原因，读者无从判断是已领过、窗口没开
//     还是渠道不支持；而原因本来就由各 vendor 写进 message。
//   - P2-1：后端在关停/超时会写 cancelled=true + message，前端原样忽略，
//     横幅照样按「有结果」渲染。
//   - P2-5：头部「N 个账号有执行结果」与 summary 里的完成数口径对不上。
//   - P2-6：局部变量 head 遮蔽 document.head。
//   - P2-7：常驻横幅没有关闭入口，只能等下一次操作替换。

import (
	"strings"
	"testing"
)

func TestConsoleAutoAllShowsUpstreamReasons(t *testing.T) {
	page := Page("/plugins/freetier2api")

	// 跳过与失败都必须带上游原因。
	if !strings.Contains(page, `parts.push("跳过 " + skips + reasonText(results, "skipped"));`) {
		t.Fatal("跳过数必须附上游原因，否则「完成 0 / 跳过 3」读不出为什么")
	}
	if !strings.Contains(page, `parts.push("失败 " + errs + reasonText(results, "error"));`) {
		t.Fatal("失败数必须附上游原因")
	}
	if !strings.Contains(page, "function reasonText(results, status)") {
		t.Fatal("reasonText 必须存在，且对同一原因去重")
	}
	// 前端不得再自己编原因。
	if strings.Contains(page, "任务已全部领过或窗口未开") {
		t.Fatal("不得硬编码猜测原因，原因只能来自上游 message")
	}
}

func TestConsoleAutoAllHandlesCancelled(t *testing.T) {
	page := Page("/plugins/freetier2api")

	if !strings.Contains(page, `var cancelledNote = (result.cancelled && result.message) ? result.message + "\n" : "";`) {
		t.Fatal("cancelled 的 message 必须置顶展示，它比逐账号明细更重要")
	}
	// 中断既不是成功也不是失败，不能落绿。
	if !strings.Contains(page, `var kind = errTotal ? "err" : (result.cancelled ? "warn" : (doneTotal ? "ok" : "warn"));`) {
		t.Fatal("cancelled 必须落 warn，不能因为 doneTotal>0 就显示绿色")
	}
}

func TestConsoleAutoAllHeadMatchesCounts(t *testing.T) {
	page := Page("/plugins/freetier2api")

	if !strings.Contains(page, `var summaryHead = okAccounts + " 个账号全部成功";`) {
		t.Fatal("头部三个计数必须各自对应一句，不能写「有执行结果」")
	}
	// 遮蔽 document.head 的写法必须消失。
	if strings.Contains(page, `var head = okAccounts + " 个账号`) {
		t.Fatal("局部变量 head 会遮蔽 document.head，必须改名")
	}
}

func TestConsoleBannerHasManualClose(t *testing.T) {
	page := Page("/plugins/freetier2api")

	if !strings.Contains(page, `el.className = "banner show " + kind + (timeout > 0 ? "" : " persist");`) {
		t.Fatal("常驻横幅必须打上 persist 标记")
	}
	if !strings.Contains(page, `close.className = "banner-close";`) {
		t.Fatal("常驻横幅必须提供手动关闭按钮")
	}
	if !strings.Contains(page, ".banner.persist .banner-close { display: flex; }") {
		t.Fatal("关闭按钮只在常驻时显示，普通横幅不该多出 ×")
	}
}
