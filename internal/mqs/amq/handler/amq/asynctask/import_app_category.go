package asynctask

import (
	"context"
	"fmt"
	"sort"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appcategory"
	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// importAppCategoryAdd 批量新增 APP 分类（biz_module=app_category_add + type=import）
//
// 判重：同站点同层级下分类名已存在则该行失败；层级=2 必须提供父分类（名称或 ID）。
// 分类页面 URL 留空按名称自动生成 slug。
type importAppCategoryAdd struct {
	baseHandler
}

func newImportAppCategoryAdd(svcCtx *svc.ServiceContext) *importAppCategoryAdd {
	return &importAppCategoryAdd{baseHandler{svcCtx: svcCtx}}
}

func (h *importAppCategoryAdd) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runAppCategory(ctx, h.baseHandler, task, rows, true)
}

// importAppCategoryUpdate 批量更新 APP 分类（biz_module=app_category_update + type=import）
//
// 站点域名+记录 ID 双定位；记录不存在或不属于该站点时该行失败；
// 层级不允许修改；改名时校验同站点同层级下新名称未占用。
type importAppCategoryUpdate struct {
	baseHandler
}

func newImportAppCategoryUpdate(svcCtx *svc.ServiceContext) *importAppCategoryUpdate {
	return &importAppCategoryUpdate{baseHandler{svcCtx: svcCtx}}
}

func (h *importAppCategoryUpdate) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	runAppCategory(ctx, h.baseHandler, task, rows, false)
}

// runAppCategory 分类新增/更新共用编排：解析 → 批内去重 → 判重/存在校验 → 落库 → 结果文件
func runAppCategory(ctx context.Context, h baseHandler, task *asyncjob.Task, rows [][]string, isAdd bool) {
	logger := logx.WithContext(ctx)
	// 聚合本任务成功写入的站点范围，结束时统一刷新 C 端缓存（失败仅记日志）
	fl := newCacheFlusher(h.svcCtx)
	defer fl.flush(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	repo := appcategory.Repo{DB: h.svcCtx.SitehubDB}
	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	resolveParent := func(siteID uint64, nameOrID string) (uint64, error) {
		return nr.ResolveCategory(siteID, 1, nameOrID) // 父分类恒为一级
	}

	// 站点内变体（工作台导入，task.SiteID>0）：模板无站点域名列，
	// 前插预置站点域名占位后复用跨站解析，行钉到该站点
	pinnedDomain := ""
	if task.SiteID > 0 {
		d, derr := h.pinnedSiteDomain(ctx, task.SiteID)
		if derr != nil {
			h.finishFailed(ctx, task.ID, derr.Error())
			return
		}
		pinnedDomain = d
	}

	var (
		okCnt   uint64
		failCnt uint64
		results []importResultRow
	)
	result := func(rowNo int, ident string, ok bool, msg string) {
		if ok {
			okCnt++
		} else {
			failCnt++
		}
		results = append(results, importResultRow{RowNo: rowNo, Identifier: ident, OK: ok, Message: msg})
	}

	var rowsToApply []*appcategory.Row
	seenKey := make(map[string]int) // 批内去重键 → 行号

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
			var r *appcategory.Row
			var err error
			if task.SiteID > 0 {
				if isAdd {
					r, err = appcategory.ParseAddRowScoped(j+2, rows[j], pinnedDomain, nr.ResolveSite, resolveParent)
				} else {
					r, err = appcategory.ParseUpdateRowScoped(j+2, rows[j], pinnedDomain, nr.ResolveSite, resolveParent)
				}
			} else if isAdd {
				r, err = appcategory.ParseAddRow(j+2, rows[j], nr.ResolveSite, resolveParent)
			} else {
				r, err = appcategory.ParseUpdateRow(j+2, rows[j], nr.ResolveSite, resolveParent)
			}
			if err != nil {
				failCnt++
				results = append(results, importResultRow{RowNo: j + 2, Identifier: categoryIdentFor(rows[j]), Message: err.Error()})
				continue
			}
			if r == nil {
				continue
			}
			// 批内去重：add 按站点+层级+名称；update 按 ID
			key := fmt.Sprintf("%d|%d|%s", r.SiteID, r.Level, r.Name)
			if !isAdd {
				key = fmt.Sprintf("id:%d", r.ID)
			}
			if firstNo, dup := seenKey[key]; dup {
				result(r.RowNo, categoryIdent(r), false, fmt.Sprintf("与第 %d 行重复", firstNo))
				continue
			}
			seenKey[key] = r.RowNo
			rowsToApply = append(rowsToApply, r)
		}
	}

	for _, r := range rowsToApply {
		ident := categoryIdent(r)
		if isAdd {
			if existing, err := repo.FindByName(ctx, r.SiteID, r.Level, r.Name); err != nil {
				result(r.RowNo, ident, false, fmt.Sprintf("查询失败 %v", err))
				continue
			} else if existing != nil {
				result(r.RowNo, ident, false, "同站点同层级下分类名已存在")
				continue
			}
			if err := repo.Insert(ctx, r); err != nil {
				result(r.RowNo, ident, false, fmt.Sprintf("新增失败 %v", err))
				continue
			}
			result(r.RowNo, ident, true, "新增成功")
			fl.add(siteApp, r.SiteID, pageCategories)
			continue
		}

		// update：存在性 + 归属校验
		existing, err := repo.GetByID(ctx, r.ID)
		if err != nil {
			result(r.RowNo, ident, false, fmt.Sprintf("查询失败 %v", err))
			continue
		}
		if existing == nil {
			result(r.RowNo, ident, false, "记录 ID 不存在")
			continue
		}
		if existing.SiteID != r.SiteID {
			result(r.RowNo, ident, false, fmt.Sprintf("记录不属于站点 %s（实际 site_id=%d）", r.SiteDomain, existing.SiteID))
			continue
		}
		// 父分类变更校验：仅二级可设置父分类（与模板表头一致），且父必须存在、
		// 属于同站点且为一级（ParseParent 对纯数字 ID 不查库，这里兜底拦截）
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
			if parent == nil {
				result(r.RowNo, ident, false, "父分类不存在")
				continue
			}
			if parent.SiteID != existing.SiteID {
				result(r.RowNo, ident, false, "父分类不属于当前站点")
				continue
			}
			if parent.Level != 1 {
				result(r.RowNo, ident, false, "父分类必须是一级分类")
				continue
			}
		}
		// 改名时校验同站点同层级下新名称未占用
		if r.Name != "" && r.Name != existing.Name {
			if dup, derr := repo.FindByName(ctx, existing.SiteID, existing.Level, r.Name); derr == nil && dup != nil && dup.ID != r.ID {
				result(r.RowNo, ident, false, fmt.Sprintf("同层级下已存在同名分类（ID=%d）", dup.ID))
				continue
			}
		}
		if err := repo.UpdateByID(ctx, r); err != nil {
			result(r.RowNo, ident, false, fmt.Sprintf("更新失败 %v", err))
			continue
		}
		result(r.RowNo, ident, true, "更新成功")
		fl.add(siteApp, r.SiteID, pageCategories)
	}

	// 结果文件（失败/成功全量）
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

// categoryIdent 结果文件行标识：域名#ID > 域名#名称 > 名称 > ID
func categoryIdent(r *appcategory.Row) string {
	if r.SiteDomain != "" && r.ID > 0 {
		return fmt.Sprintf("%s#%d", r.SiteDomain, r.ID)
	}
	if r.SiteDomain != "" && r.Name != "" {
		return r.SiteDomain + "#" + r.Name
	}
	if r.Name != "" {
		return r.Name
	}
	if r.ID > 0 {
		return fmt.Sprintf("%d", r.ID)
	}
	return "-"
}

// categoryIdentFor 从原始行提取结果文件标识（首个非空单元格）
func categoryIdentFor(row []string) string {
	for _, c := range row {
		if c != "" {
			return c
		}
	}
	return "-"
}
