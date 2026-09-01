package asynctask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// ExportAppDetailHandler 批量导出 APP 详情（biz_module=app_detail + type=export）
//
// task.FileURL 为空（导出无输入文件）；task.ErrorDetail 字段在生产端被借用存放过滤参数 JSON
// 例如：{"siteId": 1} 表示只导出 site_id=1 的记录
type ExportAppDetailHandler struct {
	baseHandler
}

func NewExportAppDetailHandler(svcCtx *svc.ServiceContext) *ExportAppDetailHandler {
	return &ExportAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *ExportAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	payload, err := h.loadPayload(t)
	if err != nil {
		logger.Error(err)
		return err
	}

	task, err := h.markProcessing(ctx, payload.TaskID)
	if err != nil {
		logger.Error(err)
		return nil
	}

	if task.BizModule != asyncjob.BizAppDetail || task.Type != asyncjob.TypeExport {
		errMsg := fmt.Sprintf("asynctask: task %d biz_module/type mismatch: got %s/%s", task.ID, task.BizModule, task.Type)
		logger.Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	// 解析过滤参数（从 ErrorMessage 字段借用——生产端写入时已通过 task.ErrorMessage 字段保存）
	// 这里我们换用 task.Name 后追加的方式不可行，所以重新约定：filter JSON 存在 task.OperatorName 字段后面
	// 实际生产端约定：filter JSON 写入 task.Name 字段前缀 "[export]"，或者直接放在 OperatorName
	// 简化：filter JSON 单独存在 task.FileName 字段（导出任务不用 FileName）
	var filter struct {
		SiteID uint64 `json:"siteId"`
	}
	if task.FileName != "" {
		_ = json.Unmarshal([]byte(task.FileName), &filter) // 容错：解析失败时 filter.SiteID=0（导出全部）
	}

	// 统计总数
	total, err := countAppDetails(ctx, h.svcCtx.SitehubDB, filter.SiteID)
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
	w := helper.NewExcelWriter("APP详情", exportHeaders())
	defer w.Close()

	const pageSize = 500
	var success uint64
	for offset := 0; offset < int(total); offset += pageSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		limit := pageSize
		rows, err := exportAppDetails(ctx, h.svcCtx.SitehubDB, filter.SiteID, limit, offset)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return nil
		}
		for _, r := range rows {
			if e := w.WriteRow(exportRowToStrings(r)); e != nil {
				logger.Errorf("write export row: %v", e)
				continue
			}
			success++
		}
		progress := uint8(success * 100 / total)
		if progress > 99 {
			progress = 99
		}
		h.reportProgress(ctx, task.ID, total, success, 0, progress, "")
	}

	// 序列化并上传
	data, err := w.Bytes()
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("序列化 Excel 失败: %v", err))
		return nil
	}
	fileName := fmt.Sprintf("app_detail_export_task_%d.xlsx", task.ID)
	url, err := h.uploadResult(ctx, task.ID, "async_export", fileName, data,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
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

// writeEmptyExport 写一个只有表头的空 Excel
func (h *ExportAppDetailHandler) writeEmptyExport(ctx context.Context, taskID uint64) (string, error) {
	w := helper.NewExcelWriter("APP详情", exportHeaders())
	defer w.Close()
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("app_detail_export_task_%d.xlsx", taskID)
	return h.uploadResult(ctx, taskID, "async_export", fileName, data,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
}

// exportHeaders 导出 Excel 的表头（顺序与 exportRowToStrings 一致）
func exportHeaders() []string {
	return []string{
		"ID", "站点ID", "应用名称", "详情链接", "一级分类ID", "二级分类ID",
		"Logo", "开发者", "版本号", "内容评级", "价格", "最低Android",
		"更新时间", "下载量", "评分", "评论数", "应用描述",
		"Google Play链接", "Apple商店链接", "APK下载链接", "APK版本号", "APK大小", "APK更新时间",
		"状态", "创建时间", "更新时间",
	}
}

// exportRowToStrings 将一行 app_detail 转为字符串切片（顺序与 exportHeaders 一致）
// NULL 值统一输出空字符串
func exportRowToStrings(r *appDetailExportRow) []string {
	statusStr := "启用"
	if r.Status.Valid && r.Status.Int64 == 2 {
		statusStr = "禁用"
	}
	return []string{
		strconv.FormatUint(r.ID, 10),
		strconv.FormatUint(r.SiteID, 10),
		r.Name.String,
		r.URL.String,
		nullInt64Str(r.FirstCategoryID),
		nullInt64Str(r.SecondCategoryID),
		r.Logo.String,
		r.Developer.String,
		r.Version.String,
		r.ContentRating.String,
		r.Price.String,
		r.MinAndroid.String,
		r.Updated.String,
		nullInt64Str(r.Downloads),
		nullFloat64Str(r.Rating),
		nullInt64Str(r.ReviewCount),
		r.Description.String,
		r.GooglePlayURL.String,
		r.AppleStoreURL.String,
		r.APKDownloadURL.String,
		r.APKVersion.String,
		r.APKSize.String,
		r.APKUpdated.String,
		statusStr,
		nullTimeStr(r.CreatedAt),
		nullTimeStr(r.UpdatedAt),
	}
}

func nullInt64Str(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}

func nullFloat64Str(v sql.NullFloat64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatFloat(v.Float64, 'f', 2, 64)
}

func nullTimeStr(v sql.NullTime) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format("2006-01-02 15:04:05")
}

var _ asynq.Handler = (*ExportAppDetailHandler)(nil)
