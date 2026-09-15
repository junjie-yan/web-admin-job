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

// ImportAppDetailHandler 批量导入 APP 详情（biz_module=app_detail + type=import）
//
// Excel 列布局见 appdetail 包列常量（站点/分类填名称自动查库转 ID，兼容纯数字 ID）
// 去重规则：site_id + url 已存在的记录跳过（url 留空的行按应用名称自动生成后同样查重）
type ImportAppDetailHandler struct {
	baseHandler
}

// NewImportAppDetailHandler 创建导入 handler
func NewImportAppDetailHandler(svcCtx *svc.ServiceContext) *ImportAppDetailHandler {
	return &ImportAppDetailHandler{baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *ImportAppDetailHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	task, retry := h.claimTask(ctx, t, asyncjob.BizAppDetail, asyncjob.TypeImport)
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

	// 逐批解析 + 入库（每 50 行一批）
	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	const batchSize = 50
	var (
		success uint64
		skipped uint64
		fail    uint64
		errs    []string // 错误明细（写入结果文件时最多保留 200 条）
	)

	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据
		var parsed []*appdetail.Row
		var batchErrs []string
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := appdetail.ParseRow(j+2, rows[j], nr, true, false)
			if parseErr != nil {
				batchErrs = append(batchErrs, parseErr.Error())
				continue
			}
			if r != nil {
				parsed = append(parsed, r)
			}
		}

		// 2. 查询本批中已存在的记录（site_id + url）
		var keys []appdetail.Key
		for _, r := range parsed {
			if r.URL != nil {
				keys = append(keys, appdetail.Key{SiteID: r.SiteID, URL: *r.URL})
			}
		}
		var existKeys map[appdetail.Key]uint64
		if len(keys) > 0 {
			existKeys, err = appdetail.FindIDsBySiteAndURL(ctx, h.svcCtx.SitehubDB, keys)
			if err != nil {
				logger.Error(err)
				h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 已存在记录失败: %v", err))
				return nil
			}
		}

		// 3. 过滤已存在记录后整批插入；整批失败降级逐条插入以定位错误行
		pending := make([]*appdetail.Row, 0, len(parsed))
		for _, r := range parsed {
			if r.URL != nil && existKeys != nil {
				if _, ok := existKeys[appdetail.Key{SiteID: r.SiteID, URL: *r.URL}]; ok {
					skipped++
					success++ // 跳过视为处理成功，跳过数在任务消息中单独说明
					continue
				}
			}
			pending = append(pending, r)
		}
		if len(pending) > 0 {
			if e := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, pending); e != nil {
				for _, r := range pending {
					if e := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, []*appdetail.Row{r}); e != nil {
						batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: %v", r.RowNo, e))
						continue
					}
					success++
				}
			} else {
				success += uint64(len(pending))
			}
		}

		fail += uint64(len(batchErrs))
		errs = append(errs, batchErrs...)
		h.reportProgress(ctx, task.ID, total, success, fail, calcProgress(uint64(end), total), "")
	}

	// 4. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeImport, errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 5. 终态
	switch {
	case fail == 0 && skipped > 0:
		h.finishSuccessWithMsg(ctx, task.ID, total, success,
			fmt.Sprintf("导入完成：成功 %d（含跳过已存在 %d）", success, skipped))
	case fail == 0:
		h.finishSuccess(ctx, task.ID, total)
	default:
		h.finishPartial(ctx, task.ID, total, success, fail,
			fmt.Sprintf("导入完成：成功 %d（含跳过已存在 %d），失败 %d", success, skipped, fail), resultURL)
	}
	return nil
}

// 编译期断言：保证 handler 实现 asynq.Handler
var _ asynq.Handler = (*ImportAppDetailHandler)(nil)
