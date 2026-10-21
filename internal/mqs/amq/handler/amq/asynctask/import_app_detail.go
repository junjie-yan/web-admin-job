package asynctask

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
	"github.com/junjie-yan/web-admin-job/pkg/slugify"
)

// importAppDetailAdd 批量新增 APP 详情（biz_module=app_detail_add + type=import）
//
// 只新增不存在的数据：按 站点+GooglePlay链接（优先）或 站点+Apple商店链接 判重，
// 已存在的行报失败；必填项校验在解析层完成（应用名称/一级分类必填，商店链接二选一必填；
// Logo/评分/二级分类允许为空，logo/second_category_id 落 NULL、rating 落 0，后期人工补录）。
// 详情链接留空按应用名称自动生成 slug，同站点占用时该行报失败。
// 执行完毕生成全量结果文件（成功与失败行都写入，不截断）。
type importAppDetailAdd struct {
	baseHandler
}

// newImportAppDetailAdd 创建批量新增处理器（由 importDispatcher 路由调用）
func newImportAppDetailAdd(svcCtx *svc.ServiceContext) *importAppDetailAdd {
	return &importAppDetailAdd{baseHandler{svcCtx: svcCtx}}
}

// process 处理已领取的批量新增任务（rows 为输入 Excel 数据行，已跳过表头）
func (h *importAppDetailAdd) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	logger := logx.WithContext(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	var (
		okCnt   uint64
		failCnt uint64
		results []importResultRow
	)
	addOK := func(r *appdetail.Row, msg string) {
		okCnt++
		results = append(results, importResultRow{RowNo: r.RowNo, Identifier: addIdentifier(r), OK: true, Message: msg})
	}
	addFail := func(r *appdetail.Row, msg string) {
		failCnt++
		results = append(results, importResultRow{RowNo: r.RowNo, Identifier: addIdentifier(r), Message: msg})
	}

	const batchSize = 50
	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据（行级必填/格式错误直接记失败；
		//    站点内任务 task.SiteID>0 走变体解析：无站点域名列，行钉到预置站点）
		var parsed []*appdetail.Row
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			var r *appdetail.Row
			var parseErr error
			if task.SiteID > 0 {
				r, parseErr = appdetail.ParseRowScoped(j+2, rows[j], nr, task.SiteID, appdetail.ParseModeAdd)
			} else {
				r, parseErr = appdetail.ParseRow(j+2, rows[j], nr, appdetail.ParseModeAdd)
			}
			if parseErr != nil {
				failCnt++
				results = append(results, importResultRow{RowNo: j + 2, Identifier: appdetail.RowIdentifier(rows[j]), Message: parseErr.Error()})
				continue
			}
			if r != nil {
				parsed = append(parsed, r)
			}
		}

		// 2. 批内按定位键去重（保留首条，后续行报失败）
		seenLoc := make(map[string]int, len(parsed)) // 定位键 → 首次出现的 Excel 行号
		var candidates []*appdetail.Row
		for _, r := range parsed {
			k := addLocKey(r)
			if firstNo, dup := seenLoc[k]; dup {
				addFail(r, fmt.Sprintf("与第 %d 行定位键重复", firstNo))
				continue
			}
			seenLoc[k] = r.RowNo
			candidates = append(candidates, r)
		}

		// 3. 详情链接留空按应用名称生成 slug
		for _, r := range candidates {
			if r.URL == nil && r.Name != "" {
				if u := slugify.Path("app", r.Name); u != "" {
					r.URL = &u
				}
			}
		}

		// 4. 库内判重：商店链接已存在 → 该记录已存在（新增模式不更新已有数据）
		var gpKeys, asKeys []appdetail.Key
		for _, r := range candidates {
			switch {
			case r.GooglePlayURL != nil && *r.GooglePlayURL != "":
				gpKeys = append(gpKeys, appdetail.Key{SiteID: r.SiteID, URL: *r.GooglePlayURL})
			case r.AppleStoreURL != nil && *r.AppleStoreURL != "":
				asKeys = append(asKeys, appdetail.Key{SiteID: r.SiteID, URL: *r.AppleStoreURL})
			}
		}
		gpIDs, err := appdetail.FindIDsBySiteAndGooglePlayURL(ctx, h.svcCtx.SitehubDB, gpKeys)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return
		}
		asIDs, err := appdetail.FindIDsBySiteAndAppleStoreURL(ctx, h.svcCtx.SitehubDB, asKeys)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return
		}
		var insertables []*appdetail.Row
		for _, r := range candidates {
			if r.GooglePlayURL != nil && *r.GooglePlayURL != "" {
				if _, exists := gpIDs[appdetail.Key{SiteID: r.SiteID, URL: *r.GooglePlayURL}]; exists {
					addFail(r, "该记录已存在")
					continue
				}
			} else if r.AppleStoreURL != nil && *r.AppleStoreURL != "" {
				if _, exists := asIDs[appdetail.Key{SiteID: r.SiteID, URL: *r.AppleStoreURL}]; exists {
					addFail(r, "该记录已存在")
					continue
				}
			}
			insertables = append(insertables, r)
		}

		// 5. 详情链接占用检查（库内 + 批内），撞车行报失败
		urlKeys := make([]appdetail.Key, 0, len(insertables))
		for _, r := range insertables {
			if r.URL != nil {
				urlKeys = append(urlKeys, appdetail.Key{SiteID: r.SiteID, URL: *r.URL})
			}
		}
		existURL, err := appdetail.FindIDsBySiteAndURL(ctx, h.svcCtx.SitehubDB, urlKeys)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return
		}
		seenURL := make(map[appdetail.Key]bool, len(insertables))
		var final []*appdetail.Row
		for _, r := range insertables {
			if r.URL != nil {
				k := appdetail.Key{SiteID: r.SiteID, URL: *r.URL}
				if _, occupied := existURL[k]; occupied {
					addFail(r, fmt.Sprintf("详情链接 %s 已被同站点其他记录占用", *r.URL))
					continue
				}
				if seenURL[k] {
					addFail(r, fmt.Sprintf("批内详情链接重复 %s", *r.URL))
					continue
				}
				seenURL[k] = true
			}
			final = append(final, r)
		}

		// 6. 整批插入（失败降级逐条以定位错误行）
		if len(final) > 0 {
			if e := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, final); e != nil {
				for _, r := range final {
					if ie := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, []*appdetail.Row{r}); ie != nil {
						addFail(r, ie.Error())
						continue
					}
					addOK(r, "新增成功")
				}
			} else {
				for _, r := range final {
					addOK(r, "新增成功")
				}
			}
		}

		h.reportProgress(ctx, task.ID, total, okCnt, failCnt, calcProgress(uint64(len(results)), total), "")
	}

	h.finishWithImportResult(ctx, task.ID, total, okCnt, failCnt, "新增完成：成功 %d", results)
}

// importAppDetailUpdate 批量更新 APP 详情（biz_module=app_detail_update + type=import）
//
// 只更新已有数据：按 站点域名+记录ID 定位（两列均必填），记录不存在或不属于该站点时该行失败，
// 防止填错 ID 时跨站点误更新；站点+ID 之外至少填写一项要更新的字段，
// 仅更新填写的非空字段（留空保持原值）；批内 ID 重复的行报失败。
// 分类列可填名称或 ID，按行内站点解析。执行完毕生成全量结果文件。
type importAppDetailUpdate struct {
	baseHandler
}

// newImportAppDetailUpdate 创建批量更新处理器（由 importDispatcher 路由调用）
func newImportAppDetailUpdate(svcCtx *svc.ServiceContext) *importAppDetailUpdate {
	return &importAppDetailUpdate{baseHandler{svcCtx: svcCtx}}
}

// process 处理已领取的批量更新任务（rows 为输入 Excel 数据行，已跳过表头）
func (h *importAppDetailUpdate) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	logger := logx.WithContext(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	var (
		okCnt   uint64
		failCnt uint64
		results []importResultRow
	)
	updResult := func(r *appdetail.Row, ok bool, msg string) {
		if ok {
			okCnt++
		} else {
			failCnt++
		}
		ident := r.SiteDomain + "#" + strconv.FormatUint(r.ID, 10)
		if r.SiteDomain == "" { // 站点内变体：无域名列，标识退化为纯 ID
			ident = strconv.FormatUint(r.ID, 10)
		}
		results = append(results, importResultRow{RowNo: r.RowNo, Identifier: ident, OK: ok, Message: msg})
	}

	const batchSize = 50
	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据（站点域名+记录ID 必填 + 至少一项更新字段，行级错误直接记失败；
		//    站点内任务 task.SiteID>0 走变体解析：无域名列，行钉到预置站点）
		var parsed []*appdetail.Row
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			var r *appdetail.Row
			var parseErr error
			if task.SiteID > 0 {
				r, parseErr = appdetail.ParseRowScoped(j+2, rows[j], nr, task.SiteID, appdetail.ParseModeUpdate)
			} else {
				r, parseErr = appdetail.ParseRow(j+2, rows[j], nr, appdetail.ParseModeUpdate)
			}
			if parseErr != nil {
				failCnt++
				ident := appdetail.UpdateRowIdentifier(rows[j])
				if task.SiteID > 0 && len(rows[j]) > 0 && strings.TrimSpace(rows[j][0]) != "" {
					ident = strings.TrimSpace(rows[j][0]) // 变体首列即记录 ID
				}
				results = append(results, importResultRow{RowNo: j + 2, Identifier: ident, Message: parseErr.Error()})
				continue
			}
			if r != nil {
				parsed = append(parsed, r)
			}
		}

		// 2. 批内按 ID 去重（保留首条，后续行报失败）
		seenID := make(map[uint64]int, len(parsed)) // ID → 首次出现的 Excel 行号
		var candidates []*appdetail.Row
		for _, r := range parsed {
			if firstNo, dup := seenID[r.ID]; dup {
				updResult(r, false, fmt.Sprintf("与第 %d 行 ID 重复", firstNo))
				continue
			}
			seenID[r.ID] = r.RowNo
			candidates = append(candidates, r)
		}
		if len(candidates) == 0 {
			h.reportProgress(ctx, task.ID, total, okCnt, failCnt, calcProgress(uint64(len(results)), total), "")
			continue
		}

		// 3. 存在性与归属校验：记录必须存在，且 site_id 与行内站点域名解析结果一致
		ids := make([]uint64, 0, len(candidates))
		for _, r := range candidates {
			ids = append(ids, r.ID)
		}
		siteMap, err := appdetail.FindSitesByIDs(ctx, h.svcCtx.SitehubDB, ids)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return
		}
		var updatables []*appdetail.Row
		for _, r := range candidates {
			actualSiteID, exists := siteMap[r.ID]
			if !exists {
				updResult(r, false, "记录 ID 不存在")
				continue
			}
			if actualSiteID != r.SiteID {
				updResult(r, false, fmt.Sprintf("记录不属于站点 %s（实际 site_id=%d），请核对站点域名与记录 ID", r.SiteDomain, actualSiteID))
				continue
			}
			updatables = append(updatables, r)
		}

		// 4. 逐条更新（仅更新填写的非空字段）
		for _, r := range updatables {
			if e := appdetail.UpdateByID(ctx, h.svcCtx.SitehubDB, r.ID, r); e != nil {
				updResult(r, false, fmt.Sprintf("更新失败 %v", e))
				continue
			}
			updResult(r, true, "更新成功")
		}

		h.reportProgress(ctx, task.ID, total, okCnt, failCnt, calcProgress(uint64(len(results)), total), "")
	}

	h.finishWithImportResult(ctx, task.ID, total, okCnt, failCnt, "更新完成：成功 %d", results)
}

// finishWithImportResult 上传全量导入结果文件并写终态（全成功 success / 有失败 partial）
func (h *baseHandler) finishWithImportResult(ctx context.Context, taskID, total, okCnt, failCnt uint64, summaryFmt string, results []importResultRow) {
	logger := logx.WithContext(ctx)

	sort.Slice(results, func(a, b int) bool { return results[a].RowNo < results[b].RowNo })
	resultURL := ""
	if url, e := h.uploadImportResult(ctx, taskID, results); e == nil {
		resultURL = url
	} else {
		logger.Error(e)
	}

	summary := fmt.Sprintf(summaryFmt, okCnt)
	if failCnt == 0 {
		h.finishSuccessWithResult(ctx, taskID, total, okCnt, summary, resultURL)
		return
	}
	h.finishPartial(ctx, taskID, total, okCnt, failCnt,
		fmt.Sprintf("%s，失败 %d", summary, failCnt), resultURL)
}

// addLocKey 新增模式的批内定位键（商店链接二选一，gp 优先；与库内判重口径一致）
func addLocKey(r *appdetail.Row) string {
	if r.GooglePlayURL != nil && *r.GooglePlayURL != "" {
		return fmt.Sprintf("gp:%d:%s", r.SiteID, *r.GooglePlayURL)
	}
	if r.AppleStoreURL != nil && *r.AppleStoreURL != "" {
		return fmt.Sprintf("as:%d:%s", r.SiteID, *r.AppleStoreURL)
	}
	return fmt.Sprintf("nm:%d:%s", r.SiteID, r.Name)
}

// addIdentifier 新增模式的行标识（结果文件展示用）：GooglePlay链接 → Apple商店链接 → 应用名称
func addIdentifier(r *appdetail.Row) string {
	switch {
	case r.GooglePlayURL != nil && *r.GooglePlayURL != "":
		return *r.GooglePlayURL
	case r.AppleStoreURL != nil && *r.AppleStoreURL != "":
		return *r.AppleStoreURL
	case r.Name != "":
		return r.Name
	default:
		return "-"
	}
}
