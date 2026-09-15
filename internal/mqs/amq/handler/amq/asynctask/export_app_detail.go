package asynctask

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// ExportAppDetailHandler 批量导出 APP 详情（biz_module=app_detail + type=export）
//
// 导出无输入文件（task.FileURL 为空）；生产端约定：过滤参数 JSON
// （appdetail.ExportFilter，如 {"siteId":1}）存放于 task.FileName 字段，
// 解析失败时降级为导出全部
type ExportAppDetailHandler struct {
	baseHandler
}

// NewExportAppDetailHandler 创建导出 handler
func NewExportAppDetailHandler(svcCtx *svc.ServiceContext) *ExportAppDetailHandler {
	return &ExportAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *ExportAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	task, retry := h.claimTask(ctx, t, asyncjob.BizAppDetail, asyncjob.TypeExport)
	if retry != nil {
		return retry
	}
	if task == nil {
		return nil
	}

	// 解析过滤参数（容错：解析失败时 SiteID=0，导出全部）
	var filter appdetail.ExportFilter
	if task.FileName != "" {
		_ = json.Unmarshal([]byte(task.FileName), &filter)
	}

	total, err := appdetail.Count(ctx, h.svcCtx.SitehubDB, filter.SiteID)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("统计 app_detail 总数失败: %v", err))
		return nil
	}
	if total == 0 {
		// 仍然上传一个只有表头的 Excel，方便用户确认
		url, e := h.writeEmptyExport(ctx, task.ID)
		if e != nil {
			logger.Error(e)
		}
		h.finishPartial(ctx, task.ID, 0, 0, 0, "无符合条件的数据", url)
		return nil
	}

	// 分页查询并写入 Excel
	w := helper.NewExcelWriter("APP详情", appdetail.ExportHeaders())
	defer func() { _ = w.Close() }()

	const pageSize = 500
	var success uint64
	for offset := 0; offset < int(total); offset += pageSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		rows, err := appdetail.ListForExport(ctx, h.svcCtx.SitehubDB, filter.SiteID, pageSize, offset)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return nil
		}
		for _, r := range rows {
			if e := w.WriteRow(appdetail.ExportRowStrings(r)); e != nil {
				logger.Errorf("write export row: %v", e)
				continue
			}
			success++
		}
		h.reportProgress(ctx, task.ID, total, success, 0, calcProgress(success, total), "")
	}

	// 序列化并上传
	data, err := w.Bytes()
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("序列化 Excel 失败: %v", err))
		return nil
	}
	fileName := fmt.Sprintf("app_detail_export_task_%d.xlsx", task.ID)
	url, err := h.uploadResult(ctx, task.ID, "async_export", fileName, data, excelContentType)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("上传导出文件失败: %v", err))
		return nil
	}

	if success == total {
		// 全部导出成功（结果文件 URL 已由 uploadResult 写入）
		h.finishSuccessWithMsg(ctx, task.ID, total, success,
			fmt.Sprintf("导出完成：共 %d 条", success))
	} else {
		fail := total - success
		h.finishPartial(ctx, task.ID, total, success, fail,
			fmt.Sprintf("导出完成：成功 %d 条，失败 %d 条", success, fail), url)
	}
	return nil
}

// writeEmptyExport 写一个只有表头的空 Excel 并上传
func (h *ExportAppDetailHandler) writeEmptyExport(ctx context.Context, taskID uint64) (string, error) {
	w := helper.NewExcelWriter("APP详情", appdetail.ExportHeaders())
	defer func() { _ = w.Close() }()
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("app_detail_export_task_%d.xlsx", taskID)
	return h.uploadResult(ctx, taskID, "async_export", fileName, data, excelContentType)
}

// 编译期断言：保证 handler 实现 asynq.Handler
var _ asynq.Handler = (*ExportAppDetailHandler)(nil)
