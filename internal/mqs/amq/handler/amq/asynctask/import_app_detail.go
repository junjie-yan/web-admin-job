package asynctask

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// ImportAppDetailHandler 批量导入 APP 详情（biz_module=app_detail + type=import）
//
// Excel 表头（与 web-admin 模板一致）：
// 站点ID | 应用名称 | 详情链接 | 一级分类ID | 二级分类ID | 开发者 | 版本号 | 价格 |
// 最低Android | 下载量 | 评分 | 评论数 | Google Play链接 | Apple商店链接 |
// APK下载链接 | APK版本号 | APK大小 | APK更新时间 | 状态
type ImportAppDetailHandler struct {
	baseHandler
}

func NewImportAppDetailHandler(svcCtx *svc.ServiceContext) *ImportAppDetailHandler {
	return &ImportAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *ImportAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	payload, err := h.loadPayload(t)
	if err != nil {
		logger.Error(err)
		return err
	}

	task, err := h.markProcessing(ctx, payload.TaskID)
	if err != nil {
		logger.Error(err)
		// 任务可能已被取消或被其他 worker 抢占，不重试
		return nil
	}

	if task.BizModule != asyncjob.BizAppDetail || task.Type != asyncjob.TypeImport {
		errMsg := fmt.Sprintf("asynctask: task %d biz_module/type mismatch: got %s/%s", task.ID, task.BizModule, task.Type)
		logger.Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	// 1. 下载输入文件
	data, err := h.downloadInput(ctx, task)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, err.Error())
		return nil
	}

	// 2. 解析 Excel
	importer, err := helper.NewExcelImporter(data)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("解析 Excel 失败: %v", err))
		return nil
	}
	defer func() { _ = importer.Close() }()

	rows, err := importer.GetRows()
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("读取 Excel 行失败: %v", err))
		return nil
	}

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return nil
	}

	// 3. 逐批解析 + 入库（每 50 行一批）
	// 去重规则：站点ID + 详情链接 已存在的记录跳过（详情链接留空的行无法判重，直接插入）
	const batchSize = 50
	var (
		success uint64
		skipped uint64
		fail    uint64
		errs    []string // 错误明细，最多保留 200 条
	)

	for i := 0; i < len(rows); i += batchSize {
		// 检查取消
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}

		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}

		// 解析本批数据
		var parsed []*appDetailRow
		var batchErrs []string
		for j := i; j < end; j++ {
			row := rows[j]
			if len(row) == 0 {
				continue // 空行
			}
			r, parseErr := parseImportRow(j+2, row)
			if parseErr != nil {
				batchErrs = append(batchErrs, parseErr.Error())
				continue
			}
			if r == nil {
				continue // 跳过空行
			}
			parsed = append(parsed, r)
		}

		// 查询本批中已存在的记录（site_id + url），跳过不插入
		var keys []appDetailKey
		for _, r := range parsed {
			if r.URL != nil {
				keys = append(keys, appDetailKey{SiteID: r.SiteID, URL: *r.URL})
			}
		}
		var existKeys map[appDetailKey]uint64
		if len(keys) > 0 {
			existKeys, err = findAppDetailIDsBySiteAndURL(ctx, h.svcCtx.SitehubDB, keys)
			if err != nil {
				logger.Error(err)
				h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 已存在记录失败: %v", err))
				return nil
			}
		}

		// 入库（逐条，失败回退到单条记录错误）
		for _, r := range parsed {
			if r.URL != nil && existKeys != nil {
				if _, ok := existKeys[appDetailKey{SiteID: r.SiteID, URL: *r.URL}]; ok {
					skipped++
					success++ // 跳过视为处理成功，跳过数在任务消息中单独说明
					continue
				}
			}
			if e := batchInsertAppDetails(ctx, h.svcCtx.SitehubDB, []*appDetailRow{r}); e != nil {
				msg := fmt.Sprintf("第 %d 行: %v", r.RowNo, e)
				batchErrs = append(batchErrs, msg)
				continue
			}
			success++
		}
		fail += uint64(len(batchErrs))
		if len(batchErrs) > 0 {
			errs = append(errs, batchErrs...)
		}

		// 报进度
		progress := uint8(uint64(end) * 100 / total)
		if progress > 99 {
			progress = 99 // 留 1% 给终态
		}
		h.reportProgress(ctx, task.ID, total, success, fail, progress, "")
	}

	// 4. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 && len(errs) > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, "import", errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 5. 终态
	if fail == 0 {
		if skipped > 0 {
			h.finishSuccessWithMsg(ctx, task.ID, total, success,
				fmt.Sprintf("导入完成：成功 %d（含跳过已存在 %d）", success, skipped))
		} else {
			h.finishSuccess(ctx, task.ID, total)
		}
	} else {
		h.finishPartial(ctx, task.ID, total, success, fail,
			fmt.Sprintf("导入完成：成功 %d（含跳过已存在 %d），失败 %d", success, skipped, fail), resultURL)
	}
	return nil
}

// parseImportRow 解析单行导入数据，返回 appDetailRow 或 error
// rowNo 为 Excel 行号（从 2 开始），便于错误定位
// 返回 nil, nil 表示空行跳过
func parseImportRow(rowNo int, row []string) (*appDetailRow, error) {
	// 必填：site_id, name
	siteIDStr := strings.TrimSpace(cell(row, 0))
	name := strings.TrimSpace(cell(row, 1))
	if siteIDStr == "" && name == "" {
		return nil, nil // 空行
	}
	if siteIDStr == "" {
		return nil, fmt.Errorf("第 %d 行: 站点ID 必填", rowNo)
	}
	if name == "" {
		return nil, fmt.Errorf("第 %d 行: 应用名称 必填", rowNo)
	}
	siteID, err := strconv.ParseUint(siteIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("第 %d 行: 站点ID 不是合法正整数: %s", rowNo, siteIDStr)
	}

	r := &appDetailRow{RowNo: rowNo, SiteID: siteID, Name: name}

	// url
	if v := strings.TrimSpace(cell(row, 2)); v != "" {
		r.URL = &v
	}
	// first_category_id
	if v, e := helper.ParseUint64(cell(row, 3)); e == nil && v != nil {
		r.FirstCategoryID = v
	}
	// second_category_id
	if v, e := helper.ParseUint64(cell(row, 4)); e == nil && v != nil {
		r.SecondCategoryID = v
	}
	// developer
	if v := strings.TrimSpace(cell(row, 5)); v != "" {
		r.Developer = &v
	}
	// version
	if v := strings.TrimSpace(cell(row, 6)); v != "" {
		r.Version = &v
	}
	// price
	if v := strings.TrimSpace(cell(row, 7)); v != "" {
		r.Price = &v
	}
	// min_android
	if v := strings.TrimSpace(cell(row, 8)); v != "" {
		r.MinAndroid = &v
	}
	// downloads
	if v, e := helper.ParseInt64(cell(row, 9)); e == nil && v != nil {
		r.Downloads = v
	}
	// rating
	if v, e := helper.ParseFloat64(cell(row, 10)); e == nil && v != nil {
		r.Rating = v
	}
	// review_count
	if v, e := helper.ParseInt(cell(row, 11)); e == nil && v != nil {
		r.ReviewCount = v
	}
	// google_play_url
	if v := strings.TrimSpace(cell(row, 12)); v != "" {
		r.GooglePlayURL = &v
	}
	// apple_store_url
	if v := strings.TrimSpace(cell(row, 13)); v != "" {
		r.AppleStoreURL = &v
	}
	// apk_download_url
	if v := strings.TrimSpace(cell(row, 14)); v != "" {
		r.APKDownloadURL = &v
	}
	// apk_version
	if v := strings.TrimSpace(cell(row, 15)); v != "" {
		r.APKVersion = &v
	}
	// apk_size
	if v := strings.TrimSpace(cell(row, 16)); v != "" {
		r.APKSize = &v
	}
	// apk_updated
	if v := strings.TrimSpace(cell(row, 17)); v != "" {
		r.APKUpdated = &v
	}
	// status
	if v := strings.TrimSpace(cell(row, 18)); v != "" {
		st := parseStatus(v)
		r.Status = &st
	}

	return r, nil
}

// parseStatus 将状态字符串解析为 uint8（1 启用 / 2 禁用），默认启用
func parseStatus(s string) uint8 {
	switch s {
	case "禁用", "2", "disable", "false", "ban":
		return 2
	default:
		return 1
	}
}

// uploadErrorDetails 将错误明细写入 Excel 并上传 R2，返回结果 URL
func (h *baseHandler) uploadErrorDetails(ctx context.Context, taskID uint64, taskType string, errs []string) (string, error) {
	if len(errs) == 0 {
		return "", nil
	}
	w := helper.NewExcelWriter("错误明细", []string{"序号", "错误描述"})
	defer func() { _ = w.Close() }()
	// 限制 200 条
	if len(errs) > 200 {
		errs = errs[:200]
	}
	for i, e := range errs {
		_ = w.WriteRow([]string{strconv.Itoa(i + 1), e})
	}
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("task_%d_%s_errors.xlsx", taskID, taskType)
	return h.uploadResult(ctx, taskID, "async_"+taskType+"_error", fileName, data,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
}

// 编译期断言：保证 handler 实现 asynq.Handler
var _ asynq.Handler = (*ImportAppDetailHandler)(nil)
