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

// BatchUpdateAppDetailHandler 批量更新 APP 详情（biz_module=app_detail + type=batch_update）
//
// 以 site_id + url 作为唯一键定位记录，仅更新 Excel 中填写的字段，空字段保持原值。
// Excel 表头与导入模板一致，但 site_id 和 url 为必填，其他字段任意填写。
type BatchUpdateAppDetailHandler struct {
	baseHandler
}

func NewBatchUpdateAppDetailHandler(svcCtx *svc.ServiceContext) *BatchUpdateAppDetailHandler {
	return &BatchUpdateAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *BatchUpdateAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
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

	if task.BizModule != asyncjob.BizAppDetail || task.Type != asyncjob.TypeBatchUpdate {
		errMsg := fmt.Sprintf("asynctask: task %d biz_module/type mismatch: got %s/%s", task.ID, task.BizModule, task.Type)
		logger.Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	data, err := h.downloadInput(ctx, task)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, err.Error())
		return nil
	}

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

	// 1. 第一遍解析：得到所有 row + 构造 key 列表
	type parsedRow struct {
		rowNo int
		r     *appDetailRow
		key   appDetailKey
	}
	var parsed []parsedRow
	var parseErrs []string

	for idx, row := range rows {
		if len(row) == 0 {
			continue
		}
		rowNo := idx + 2
		r, e := parseBatchUpdateRow(rowNo, row)
		if e != nil {
			parseErrs = append(parseErrs, e.Error())
			continue
		}
		if r == nil {
			continue
		}
		parsed = append(parsed, parsedRow{rowNo: rowNo, r: r, key: appDetailKey{SiteID: r.SiteID, URL: *r.URL}})
	}

	// 2. 一次性查询所有 key → id 映射
	var keys []appDetailKey
	for _, p := range parsed {
		keys = append(keys, p.key)
	}
	keyToID, err := findAppDetailIDsBySiteAndURL(ctx, h.svcCtx.SitehubDB, keys)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
		return nil
	}

	// 3. 逐条更新，每 50 条报一次进度
	const batchSize = 50
	var (
		success uint64
		fail    uint64
		errs    []string
	)
	errs = append(errs, parseErrs...)
	fail += uint64(len(parseErrs))

	for i := 0; i < len(parsed); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		end := i + batchSize
		if end > len(parsed) {
			end = len(parsed)
		}

		for j := i; j < end; j++ {
			p := parsed[j]
			id, ok := keyToID[p.key]
			if !ok {
				msg := fmt.Sprintf("第 %d 行: 未找到 site_id=%d url=%s 对应的记录", p.rowNo, p.key.SiteID, p.key.URL)
				errs = append(errs, msg)
				fail++
				continue
			}
			if e := updateAppDetailByID(ctx, h.svcCtx.SitehubDB, id, p.r); e != nil {
				msg := fmt.Sprintf("第 %d 行: 更新失败 %v", p.rowNo, e)
				errs = append(errs, msg)
				fail++
				continue
			}
			success++
		}

		progress := uint8(uint64(end) * 100 / total)
		if progress > 99 {
			progress = 99
		}
		h.reportProgress(ctx, task.ID, total, success, fail, progress, "")
	}

	// 4. 写错误明细文件
	resultURL := ""
	if fail > 0 && len(errs) > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, "batch_update", errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 5. 终态
	if fail == 0 {
		h.finishSuccess(ctx, task.ID, total)
	} else {
		h.finishPartial(ctx, task.ID, total, success, fail,
			fmt.Sprintf("批量更新完成：成功 %d，失败 %d", success, fail), resultURL)
	}
	return nil
}

// parseBatchUpdateRow 解析批量更新单行
// 必填：site_id (列0) + url (列2)
// 其余列可选，空串跳过
// 返回 nil, nil 表示空行
func parseBatchUpdateRow(rowNo int, row []string) (*appDetailRow, error) {
	siteIDStr := strings.TrimSpace(cell(row, 0))
	urlStr := strings.TrimSpace(cell(row, 2))

	if siteIDStr == "" && urlStr == "" && strings.TrimSpace(cell(row, 1)) == "" {
		return nil, nil // 空行
	}
	if siteIDStr == "" {
		return nil, fmt.Errorf("第 %d 行: 站点ID 必填（批量更新唯一键）", rowNo)
	}
	if urlStr == "" {
		return nil, fmt.Errorf("第 %d 行: 详情链接 必填（批量更新唯一键）", rowNo)
	}
	siteID, err := strconv.ParseUint(siteIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("第 %d 行: 站点ID 不是合法正整数: %s", rowNo, siteIDStr)
	}

	r := &appDetailRow{RowNo: rowNo, SiteID: siteID, URL: &urlStr}

	// name（可选）
	if v := strings.TrimSpace(cell(row, 1)); v != "" {
		r.Name = v
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

var _ asynq.Handler = (*BatchUpdateAppDetailHandler)(nil)
