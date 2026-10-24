package asynctask

import (
	"context"
	"fmt"
	"sort"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/categorybase"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// importCouponCategoryAdd 批量新增优惠券分类（biz_module=coupon_category_add + type=import）
//
// 判重：同层级下分类名已存在则该行失败；层级=2 必须提供父分类（名称或 ID）。
type importCouponCategoryAdd struct {
	baseHandler
}

func newImportCouponCategoryAdd(svcCtx *svc.ServiceContext) *importCouponCategoryAdd {
	return &importCouponCategoryAdd{baseHandler{svcCtx: svcCtx}}
}

func (h *importCouponCategoryAdd) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runCouponCategory(ctx, h.baseHandler, task, rows, true)
}

// importCouponCategoryUpdate 批量更新优惠券分类（biz_module=coupon_category_update + type=import）
//
// 记录 ID 定位；不存在时该行失败；层级不允许修改；改名校验同层级下未占用。
type importCouponCategoryUpdate struct {
	baseHandler
}

func newImportCouponCategoryUpdate(svcCtx *svc.ServiceContext) *importCouponCategoryUpdate {
	return &importCouponCategoryUpdate{baseHandler{svcCtx: svcCtx}}
}

func (h *importCouponCategoryUpdate) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runCouponCategory(ctx, h.baseHandler, task, rows, false)
}

// runCouponCategory 分类新增/更新共用编排
func runCouponCategory(ctx context.Context, h baseHandler, task *asyncjob.Task, rows [][]string, isAdd bool) {
	logger := logx.WithContext(ctx)
	// 聚合本任务成功写入的缓存范围，结束时统一刷新 C 端缓存（失败仅记日志）
	// coupon_category_base 为全局 base 表，整类跨实例刷新（SiteID=0）
	fl := newCacheFlusher(h.svcCtx)
	defer fl.flush(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	repo := categorybase.Repo{DB: h.svcCtx.SitehubDB}
	// 父分类按一级解析（名称未命中报错，纯数字 ID 由 ParseParent 兜底）
	resolveParent := func(nameOrID string) (uint64, error) {
		if found, e := repo.FindByName(ctx, 1, nameOrID); e == nil && found != nil {
			return found.ID, nil
		} else if e != nil {
			return 0, e
		}
		return 0, fmt.Errorf("一级分类不存在: %s", nameOrID)
	}

	var (
		okCnt   uint64
		failCnt uint64
		results []importResultRow
	)
	// 同批父子依赖：新增二级分类的父分类可能在本批更早行、尚未落库，
	// 解析期查库会报「一级分类不存在」；此类 add 失败行延后到 apply 完成后重试一轮
	var deferredAdds []deferredRow
	result := func(rowNo int, ident string, ok bool, msg string) {
		if ok {
			okCnt++
		} else {
			failCnt++
		}
		results = append(results, importResultRow{RowNo: rowNo, Identifier: ident, OK: ok, Message: msg})
	}

	var rowsToApply []*categorybase.Row
	seenKey := make(map[string]int)

	for i := 0; i < len(rows); i += 50 {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+50, len(rows))
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue
			}
			var r *categorybase.Row
			var err error
			if isAdd {
				r, err = categorybase.ParseAddRow(j+2, rows[j], resolveParent)
			} else {
				r, err = categorybase.ParseUpdateRow(j+2, rows[j], resolveParent)
			}
			if err != nil {
				if isAdd {
					deferredAdds = append(deferredAdds, deferredRow{rowNo: j + 2, row: rows[j]})
					continue
				}
				failCnt++
				results = append(results, importResultRow{RowNo: j + 2, Identifier: firstNonEmpty(rows[j]), Message: err.Error()})
				continue
			}
			if r == nil {
				continue
			}
			key := fmt.Sprintf("%d|%s", r.Level, r.Name)
			if !isAdd {
				key = fmt.Sprintf("id:%d", r.ID)
			}
			if firstNo, dup := seenKey[key]; dup {
				result(r.RowNo, firstNonEmpty(rows[j]), false, fmt.Sprintf("与第 %d 行重复", firstNo))
				continue
			}
			seenKey[key] = r.RowNo
			rowsToApply = append(rowsToApply, r)
		}
	}

	for _, r := range rowsToApply {
		ident := r.Name
		if r.ID > 0 {
			ident = fmt.Sprintf("%d", r.ID)
		}
		if isAdd {
			if existing, err := repo.FindByName(ctx, r.Level, r.Name); err != nil {
				result(r.RowNo, ident, false, fmt.Sprintf("查询失败 %v", err))
				continue
			} else if existing != nil {
				result(r.RowNo, ident, false, "同层级下分类名已存在")
				continue
			}
			if err := repo.Insert(ctx, r); err != nil {
				result(r.RowNo, ident, false, fmt.Sprintf("新增失败 %v", err))
				continue
			}
			result(r.RowNo, ident, true, "新增成功")
			fl.add(siteCoupon, 0, pageCategories, pageCategory)
			continue
		}

		existing, err := repo.GetByID(ctx, r.ID)
		if err != nil {
			result(r.RowNo, ident, false, fmt.Sprintf("查询失败 %v", err))
			continue
		}
		if existing == nil {
			result(r.RowNo, ident, false, "记录 ID 不存在")
			continue
		}
		// 父分类变更校验：仅二级可设置父分类（与模板表头一致），且父必须存在且为一级
		//（ParseParent 对纯数字 ID 不查库，这里兜底拦截不存在的父 ID，防悬空 pid）
		if r.ParentRaw != "" {
			if existing.Level != 2 {
				result(r.RowNo, ident, false, "仅二级分类可设置父分类")
				continue
			}
			if r.Pid == 0 {
				result(r.RowNo, ident, false, "父分类无效（二级分类不可清空父分类）")
				continue
			}
			parent, perr := repo.GetByID(ctx, r.Pid)
			if perr != nil {
				result(r.RowNo, ident, false, fmt.Sprintf("查询父分类失败 %v", perr))
				continue
			}
			if parent == nil || parent.Level != 1 {
				result(r.RowNo, ident, false, "父分类必须是一级分类")
				continue
			}
		}
		// 改名校验
		if r.Name != "" && r.Name != existing.Name {
			if dup, derr := repo.FindByName(ctx, existing.Level, r.Name); derr == nil && dup != nil && dup.ID != r.ID {
				result(r.RowNo, ident, false, fmt.Sprintf("同层级下已存在同名分类（ID=%d）", dup.ID))
				continue
			}
		}
		if err := repo.UpdateByID(ctx, r); err != nil {
			result(r.RowNo, ident, false, fmt.Sprintf("更新失败 %v", err))
			continue
		}
		result(r.RowNo, ident, true, "更新成功")
		fl.add(siteCoupon, 0, pageCategories, pageCategory)
	}

	// 同批父子依赖重试：此时本批一级分类已全部落库，延后的二级分类行重新解析即可命中父分类；
	// 仍失败的（必填缺失等永久错误）此时才计入失败
	for _, d := range deferredAdds {
		r, err := categorybase.ParseAddRow(d.rowNo, d.row, resolveParent)
		if err != nil {
			failCnt++
			results = append(results, importResultRow{RowNo: d.rowNo, Identifier: firstNonEmpty(d.row), Message: err.Error()})
			continue
		}
		if r == nil {
			continue
		}
		existing, err := repo.FindByName(ctx, r.Level, r.Name)
		if err != nil {
			result(d.rowNo, r.Name, false, fmt.Sprintf("查询失败 %v", err))
			continue
		}
		if existing != nil {
			result(d.rowNo, r.Name, false, "同层级下分类名已存在")
			continue
		}
		if err := repo.Insert(ctx, r); err != nil {
			result(d.rowNo, r.Name, false, fmt.Sprintf("新增失败 %v", err))
			continue
		}
		result(d.rowNo, r.Name, true, "新增成功")
		fl.add(siteCoupon, 0, pageCategories, pageCategory)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].RowNo < results[j].RowNo })
	resultURL := ""
	if url, e := h.uploadImportResult(ctx, task.ID, results); e == nil {
		resultURL = url
	} else {
		logger.Error(e)
	}
	summary := fmt.Sprintf("完成：成功 %d，失败 %d", okCnt, failCnt)
	if failCnt == 0 {
		h.finishSuccessWithResult(ctx, task.ID, total, okCnt, summary, resultURL)
		return
	}
	h.finishPartial(ctx, task.ID, total, okCnt, failCnt,
		fmt.Sprintf("%s，失败 %d", summary, failCnt), resultURL)
}

// firstNonEmpty 从原始行提取结果文件标识（首个非空单元格）
func firstNonEmpty(row []string) string {
	for _, c := range row {
		if c != "" {
			return c
		}
	}
	return "-"
}
