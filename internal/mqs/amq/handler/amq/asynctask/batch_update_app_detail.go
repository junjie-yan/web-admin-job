package asynctask

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// BatchUpdateAppDetailHandler 批量更新 APP 详情（biz_module=app_detail + type=batch_update）
//
// 定位键优先级：站点+详情链接 精确定位；详情链接留空时用 站点+应用名称 兜底定位
// （同名多条时该行报错，要求改填详情链接）。仅更新 Excel 中填写的字段，空字段保持原值。
// Excel 列布局与导入模板一致（见 appdetail 包列常量），站点必填，详情链接/应用名称至少填一项。
type BatchUpdateAppDetailHandler struct {
	baseHandler
}

// NewBatchUpdateAppDetailHandler 创建批量更新 handler
func NewBatchUpdateAppDetailHandler(svcCtx *svc.ServiceContext) *BatchUpdateAppDetailHandler {
	return &BatchUpdateAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *BatchUpdateAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	task, retry := h.claimTask(ctx, t, asyncjob.BizAppDetail, asyncjob.TypeBatchUpdate)
	if retry != nil {
		return retry
	}
	if task == nil {
		return nil
	}

	rows, cleanup, err := h.readInputExcel(ctx, task)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, err.Error())
		return nil
	}
	defer cleanup()

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return nil
	}

	// 1. 解析全部行（站点必填，url/名称至少一项），构造定位键列表
	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	type parsedRow struct {
		rowNo   int
		r       *appdetail.Row
		urlKey  appdetail.Key      // 非空时按 站点+url 精确定位（优先）
		nameKey appdetail.NameKey  // url 为空时按 站点+名称 兜底定位
	}
	var parsed []parsedRow
	var errs []string // 错误明细（含解析失败，写入结果文件时最多保留 200 条）
	for idx, row := range rows {
		if len(row) == 0 {
			continue
		}
		r, e := appdetail.ParseRow(idx+2, row, nr, false, true)
		if e != nil {
			errs = append(errs, e.Error())
			continue
		}
		if r == nil {
			continue // 空行
		}
		p := parsedRow{rowNo: idx + 2, r: r, nameKey: appdetail.NameKey{SiteID: r.SiteID, Name: r.Name}}
		if r.URL != nil {
			p.urlKey = appdetail.Key{SiteID: r.SiteID, URL: *r.URL}
		}
		parsed = append(parsed, p)
	}
	fail := uint64(len(errs))

	// 2. 一次性查询定位键 → id 映射（url 键与 name 键分别批量查询，内部按 500/批拆分 IN 查询）
	urlKeys := make([]appdetail.Key, 0, len(parsed))
	nameKeys := make([]appdetail.NameKey, 0, len(parsed))
	for _, p := range parsed {
		if p.urlKey.URL != "" {
			urlKeys = append(urlKeys, p.urlKey)
		} else {
			nameKeys = append(nameKeys, p.nameKey)
		}
	}
	keyToID, err := appdetail.FindIDsBySiteAndURL(ctx, h.svcCtx.SitehubDB, urlKeys)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
		return nil
	}
	nameToIDs, err := appdetail.FindIDsBySiteAndName(ctx, h.svcCtx.SitehubDB, nameKeys)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
		return nil
	}

	// resolve 定位一行记录的 id，未命中/歧义时返回错误信息
	resolve := func(p parsedRow) (uint64, string) {
		if p.urlKey.URL != "" {
			if id, ok := keyToID[p.urlKey]; ok {
				return id, ""
			}
			return 0, fmt.Sprintf("未找到 site_id=%d url=%s 对应的记录", p.urlKey.SiteID, p.urlKey.URL)
		}
		ids := nameToIDs[p.nameKey]
		switch len(ids) {
		case 0:
			return 0, fmt.Sprintf("未找到 site_id=%d 应用名称=%s 对应的记录", p.nameKey.SiteID, p.nameKey.Name)
		case 1:
			return ids[0], ""
		default:
			return 0, fmt.Sprintf("该站点下存在 %d 条应用名称=%q 的记录，请填写详情链接精确定位", len(ids), p.nameKey.Name)
		}
	}

	// 3. 逐条更新（各行更新字段不同），每 50 条报一次进度
	const batchSize = 50
	var success uint64
	for i := 0; i < len(parsed); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		end := min(i+batchSize, len(parsed))
		for j := i; j < end; j++ {
			p := parsed[j]
			id, errMsg := resolve(p)
			if errMsg != "" {
				errs = append(errs, fmt.Sprintf("第 %d 行: %s", p.rowNo, errMsg))
				fail++
				continue
			}
			if e := appdetail.UpdateByID(ctx, h.svcCtx.SitehubDB, id, p.r); e != nil {
				errs = append(errs, fmt.Sprintf("第 %d 行: 更新失败 %v", p.rowNo, e))
				fail++
				continue
			}
			success++
		}
		h.reportProgress(ctx, task.ID, total, success, fail, calcProgress(uint64(end), total), "")
	}

	// 4. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeBatchUpdate, errs); e == nil {
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

// 编译期断言：保证 handler 实现 asynq.Handler
var _ asynq.Handler = (*BatchUpdateAppDetailHandler)(nil)
