package admin

// 本文件是活动状态的管理接口适配层：解析请求、调用 tasks.Service、封装响应。
//
// 任务中心（scan / run / queue / auto）已下线：控制台只保留账号、额度、模型与
// 设置，后台签到与保活调度仍在 tasks 引擎里按计划运行。
//
// 唯一保留的任务入口是「一键任务」：对全部渠道的全部凭证逐个执行各供应商的
// RunAllTasks（签到 → 成长任务 → 活动领取）。它是后台作业（上限 30 分钟），
// 启动与查询进度分成两个端点，控制台提交后轮询 status 拿快照。

import (
	"net/http"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/hostbridge"
)

func (s *Service) handleSchoolStatus(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	ctx, cancel := hostbridge.ManagementContext(req, requestTimeout)
	defer cancel()
	return JSONResponse(http.StatusOK, s.tasks.ActivityStatus(ctx, hostbridge.RequestCallbackID(req)))
}

// handleTasksAutoAllStart 启动「一键任务」。
//
// 请求体 {"account_ids": ["..."]}：为空表示所有渠道的所有账号；非空则只跑
// 选中的账号（可跨渠道，按账号 id 匹配）。作业已在运行时返回 409，控制台据此
// 转去读进度而不是报错。
func (s *Service) handleTasksAutoAllStart(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[accountBatchRequest](req.Body)
	if errDecode != nil {
		return BadJSONRequestResponse()
	}
	payload, status := s.tasks.StartAutoAll(body.AccountIDs)
	return JSONResponse(status, payload)
}

// handleTasksAutoAllStatus 返回一键任务的进度快照：running / progress / total，
// 结束后 result 里带每个账号、每个任务的上游原始结果。
func (s *Service) handleTasksAutoAllStatus(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return JSONResponse(http.StatusOK, s.tasks.AutoAllStatus())
}
