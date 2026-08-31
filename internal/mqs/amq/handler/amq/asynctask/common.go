// Package asynctask 实现异步任务（导入/导出/批量更新）的消费端处理逻辑。
//
// 每个 handler 实现 asynq.Handler 接口，由 mqtask.Register 注册到 asynq.Server。
// 任务执行流程（通用骨架）：
//  1. 解析 payload 拿到 task_id
//  2. 从 async_task 表读取任务详情（file URL/type/bizModule）
//  3. MarkProcessing（CAS pending→processing，避免重复消费）
//  4. 下载 R2 文件 / 解析 Excel / 批量执行业务 SQL（每批次更新进度）
//  5. 失败条目写入结果文件并上传 R2，更新结果 URL
//  6. Finish（写入终态 + 统计 + 错误信息）
package asynctask

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// baseHandler 提供 asynq handler 的通用工具方法（嵌入到具体 handler 中复用）
type baseHandler struct {
	svcCtx *svc.ServiceContext
}

// loadPayload 解析 asynq.Task 的 payload 为 AsyncTaskPayload
func (h *baseHandler) loadPayload(t *asynq.Task) (*asyncjob.AsyncTaskPayload, error) {
	var p asyncjob.AsyncTaskPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return nil, fmt.Errorf("asynctask: unmarshal payload %q: %w", string(t.Payload()), err)
	}
	if p.TaskID == 0 {
		return nil, fmt.Errorf("asynctask: payload task_id is required")
	}
	return &p, nil
}

// markProcessing 将任务从 pending 切换到 processing（CAS），返回 task 详情
// 若任务已被取消或被其他 worker 抢走，返回 error
func (h *baseHandler) markProcessing(ctx context.Context, taskID uint64) (*asyncjob.Task, error) {
	task, err := h.svcCtx.AsyncTaskMgr.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("asynctask: load task %d: %w", taskID, err)
	}
	if task.Status == asyncjob.StatusCanceled {
		return nil, fmt.Errorf("asynctask: task %d already canceled", taskID)
	}
	if err := h.svcCtx.AsyncTaskMgr.MarkProcessing(ctx, taskID); err != nil {
		return nil, fmt.Errorf("asynctask: mark processing: %w", err)
	}
	return task, nil
}

// checkCanceled 检查任务是否已被用户取消（长任务中每批次调用）
func (h *baseHandler) checkCanceled(ctx context.Context, taskID uint64) bool {
	canceled, err := h.svcCtx.AsyncTaskMgr.IsCanceled(ctx, taskID)
	if err != nil {
		logx.WithContext(ctx).Errorf("asynctask: check canceled for task %d: %v", taskID, err)
		return false
	}
	return canceled
}

// downloadInput 从 R2 下载任务的输入文件（Excel 等）
func (h *baseHandler) downloadInput(ctx context.Context, task *asyncjob.Task) ([]byte, error) {
	if h.svcCtx.R2Client == nil {
		return nil, fmt.Errorf("asynctask: R2 client not configured, cannot download input file")
	}
	if task.FileURL == "" {
		return nil, fmt.Errorf("asynctask: task %d has empty file_url", task.ID)
	}
	data, err := helper.DownloadFile(ctx, h.svcCtx.R2Client, h.svcCtx.R2Bucket, task.FileURL)
	if err != nil {
		return nil, fmt.Errorf("asynctask: download input file: %w", err)
	}
	return data, nil
}

// uploadResult 上传结果文件（错误明细/导出文件）到 R2，返回写入后的 URL
// 失败时返回空串与错误，调用方决定是否致命失败
func (h *baseHandler) uploadResult(ctx context.Context, taskID uint64, kind, fileName string, data []byte, contentType string) (string, error) {
	if h.svcCtx.R2Client == nil {
		return "", fmt.Errorf("asynctask: R2 client not configured, cannot upload result file")
	}
	url, err := helper.UploadFile(ctx, h.svcCtx.R2Client, h.svcCtx.R2Bucket, h.svcCtx.R2PublicBaseURL, kind, fileName, data, contentType)
	if err != nil {
		return "", fmt.Errorf("asynctask: upload result file: %w", err)
	}
	if err := h.svcCtx.AsyncTaskMgr.SetResultFile(ctx, taskID, url); err != nil {
		logx.WithContext(ctx).Errorf("asynctask: set result file url for task %d: %v", taskID, err)
	}
	return url, nil
}

// finishSuccess 标记任务全部成功
func (h *baseHandler) finishSuccess(ctx context.Context, taskID uint64, total uint64) {
	if err := h.svcCtx.AsyncTaskMgr.Finish(ctx, asyncjob.FinishInput{
		ID:           taskID,
		Status:       asyncjob.StatusSuccess,
		TotalCount:   total,
		SuccessCount: total,
	}); err != nil {
		logx.WithContext(ctx).Errorf("asynctask: finish success for task %d: %v", taskID, err)
	}
}

// finishPartial 标记任务部分成功，并写入结果文件 URL
func (h *baseHandler) finishPartial(ctx context.Context, taskID uint64, total, success, fail uint64, errMsg, resultURL string) {
	if err := h.svcCtx.AsyncTaskMgr.Finish(ctx, asyncjob.FinishInput{
		ID:            taskID,
		Status:        asyncjob.StatusPartial,
		TotalCount:    total,
		SuccessCount:  success,
		FailCount:     fail,
		ErrorMessage:  errMsg,
		ResultFileURL: resultURL,
	}); err != nil {
		logx.WithContext(ctx).Errorf("asynctask: finish partial for task %d: %v", taskID, err)
	}
}

// finishFailed 标记任务全部失败（致命错误）
func (h *baseHandler) finishFailed(ctx context.Context, taskID uint64, errMsg string) {
	if err := h.svcCtx.AsyncTaskMgr.Finish(ctx, asyncjob.FinishInput{
		ID:           taskID,
		Status:       asyncjob.StatusFailed,
		ErrorMessage: errMsg,
	}); err != nil {
		logx.WithContext(ctx).Errorf("asynctask: finish failed for task %d: %v", taskID, err)
	}
}

// finishCanceled 标记任务因取消而结束
// Cancel 方法已在 async_task 表写入 status=canceled + finished_at + error_message，
// 这里仅记录日志，调用方应直接 return nil 终止 asynq 处理（不触发重试）
func (h *baseHandler) finishCanceled(ctx context.Context, taskID uint64) {
	logx.WithContext(ctx).Infof("asynctask: task %d stopped due to cancellation", taskID)
}

// reportProgress 写入一次进度快照
func (h *baseHandler) reportProgress(ctx context.Context, taskID, total, success, fail uint64, progress uint8, errDetail string) {
	if err := h.svcCtx.AsyncTaskMgr.UpdateProgress(ctx, asyncjob.ProgressUpdate{
		ID:               taskID,
		TotalCount:       total,
		SuccessCount:     success,
		FailCount:        fail,
		Progress:         progress,
		ErrorDetailAppend: errDetail,
	}); err != nil {
		logx.WithContext(ctx).Errorf("asynctask: report progress for task %d: %v", taskID, err)
	}
}

// cell 安全取 row 指定列字符串，越界返回空串
func cell(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return row[col]
}

// strPtr 非空字符串返回指针，空串返回 nil
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
