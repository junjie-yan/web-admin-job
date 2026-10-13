package asynctask

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
	"github.com/junjie-yan/web-admin-job/pkg/slugify"
)

// importAppDetail 批量导入 APP 详情（biz_module=app_detail + type=import，upsert 语义）
//
// 保留兼容在途任务；新任务请使用 app_detail_add（批量新增）/ app_detail_update（批量更新）。
//
// 定位键优先级：站点+GooglePlay链接 → 站点+Apple商店链接 → 站点+应用名称（至少填一项）。
// 定位命中：仅更新 Excel 中填写的字段，留空字段保持原值；未命中：新增记录。
// 新增时详情链接留空按应用名称自动生成（"Sketchbook AA" → "/app/sketchbook-aa"），
// 填写/生成的详情链接在同站点内已被占用时该行报错（应用层查重，库层无唯一约束）。
type importAppDetail struct {
	baseHandler
}

// newImportAppDetail 创建导入处理器（由 importDispatcher 路由调用）
func newImportAppDetail(svcCtx *svc.ServiceContext) *importAppDetail {
	return &importAppDetail{baseHandler{svcCtx: svcCtx}}
}

// process 处理已领取的导入任务（rows 为输入 Excel 数据行，已跳过表头）
func (h *importAppDetail) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
	logger := logx.WithContext(ctx)

	total := uint64(len(rows))
	if total == 0 {
		h.finishSuccess(ctx, task.ID, 0)
		return
	}

	// 逐批解析 + upsert（每 50 行一批）
	nr := appdetail.NewResolver(ctx, h.svcCtx.SitehubDB)
	const batchSize = 50
	var (
		inserted uint64
		updated  uint64
		fail     uint64
		errs     []string // 错误明细（写入结果文件时最多保留 200 条）
	)

	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据
		var parsed []*appdetail.Row
		var batchErrs []string
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := appdetail.ParseRow(j+2, rows[j], nr, appdetail.ParseModeUpsert)
			if parseErr != nil {
				batchErrs = append(batchErrs, parseErr.Error())
				continue
			}
			if r != nil {
				parsed = append(parsed, r)
			}
		}

		// 2. 按定位键优先级分桶并批量预查（store URL 键与 name 键分别批量查询）
		var gpKeys, asKeys []appdetail.Key
		var nameKeys []appdetail.NameKey
		for _, r := range parsed {
			switch {
			case r.GooglePlayURL != nil && *r.GooglePlayURL != "":
				gpKeys = append(gpKeys, appdetail.Key{SiteID: r.SiteID, URL: *r.GooglePlayURL})
			case r.AppleStoreURL != nil && *r.AppleStoreURL != "":
				asKeys = append(asKeys, appdetail.Key{SiteID: r.SiteID, URL: *r.AppleStoreURL})
			default:
				nameKeys = append(nameKeys, appdetail.NameKey{SiteID: r.SiteID, Name: r.Name})
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
		nameToIDs, err := appdetail.FindIDsBySiteAndName(ctx, h.svcCtx.SitehubDB, nameKeys)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询 app_detail 失败: %v", err))
			return
		}

		// resolve 按定位键优先级定位已有记录的 id；未命中返回 (0, "")，歧义返回错误信息
		resolve := func(r *appdetail.Row) (uint64, string) {
			if r.GooglePlayURL != nil && *r.GooglePlayURL != "" {
				if id, ok := gpIDs[appdetail.Key{SiteID: r.SiteID, URL: *r.GooglePlayURL}]; ok {
					return id, ""
				}
				return 0, ""
			}
			if r.AppleStoreURL != nil && *r.AppleStoreURL != "" {
				if id, ok := asIDs[appdetail.Key{SiteID: r.SiteID, URL: *r.AppleStoreURL}]; ok {
					return id, ""
				}
				return 0, ""
			}
			ids := nameToIDs[appdetail.NameKey{SiteID: r.SiteID, Name: r.Name}]
			switch len(ids) {
			case 0:
				return 0, ""
			case 1:
				return ids[0], ""
			default:
				return 0, fmt.Sprintf("该站点下存在 %d 条应用名称=%q 的记录，请填写 GooglePlay链接 或 Apple商店链接 精确定位", len(ids), r.Name)
			}
		}

		// 3. 分流：命中 → 逐条更新；未命中 → 批内按定位键去重后整批新增
		type updateItem struct {
			id  uint64
			row *appdetail.Row
		}
		var updates []updateItem
		pending := make(map[string]*appdetail.Row) // 定位键 → 行（批内同定位键保留最后一条）
		var order []string
		locKey := func(r *appdetail.Row) string {
			if r.GooglePlayURL != nil && *r.GooglePlayURL != "" {
				return fmt.Sprintf("gp:%d:%s", r.SiteID, *r.GooglePlayURL)
			}
			if r.AppleStoreURL != nil && *r.AppleStoreURL != "" {
				return fmt.Sprintf("as:%d:%s", r.SiteID, *r.AppleStoreURL)
			}
			return fmt.Sprintf("nm:%d:%s", r.SiteID, r.Name)
		}
		for _, r := range parsed {
			id, errMsg := resolve(r)
			if errMsg != "" {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: %s", r.RowNo, errMsg))
				continue
			}
			if id > 0 {
				updates = append(updates, updateItem{id: id, row: r})
				continue
			}
			// 新增候选：详情链接留空按名称生成 slug（仅在新增路径生成，避免更新时覆盖已有 url）
			if r.URL == nil && r.Name != "" {
				if u := slugify.Path("app", r.Name); u != "" {
					r.URL = &u
				}
			}
			k := locKey(r)
			if _, dup := pending[k]; !dup {
				order = append(order, k)
			}
			pending[k] = r
		}

		for _, u := range updates {
			if e := appdetail.UpdateByID(ctx, h.svcCtx.SitehubDB, u.id, u.row); e != nil {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 更新失败 %v", u.row.RowNo, e))
				continue
			}
			updated++
		}

		// 4. 新增前做同站点详情链接占用检查（库内 + 批内），撞车行报错
		pendingRows := make([]*appdetail.Row, 0, len(pending))
		for _, k := range order {
			pendingRows = append(pendingRows, pending[k])
		}
		urlKeys := make([]appdetail.Key, 0, len(pendingRows))
		for _, r := range pendingRows {
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
		seen := make(map[appdetail.Key]bool, len(pendingRows))
		final := make([]*appdetail.Row, 0, len(pendingRows))
		for _, r := range pendingRows {
			if r.URL == nil {
				final = append(final, r)
				continue
			}
			k := appdetail.Key{SiteID: r.SiteID, URL: *r.URL}
			if _, ok := existURL[k]; ok {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 详情链接 %s 已被同站点其他记录占用", r.RowNo, *r.URL))
				continue
			}
			if seen[k] {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 批内详情链接重复 %s", r.RowNo, *r.URL))
				continue
			}
			seen[k] = true
			final = append(final, r)
		}
		if len(final) > 0 {
			if e := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, final); e != nil {
				// 整批失败降级逐条插入以定位错误行
				for _, r := range final {
					if e := appdetail.BatchInsert(ctx, h.svcCtx.SitehubDB, []*appdetail.Row{r}); e != nil {
						batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: %v", r.RowNo, e))
						continue
					}
					inserted++
				}
			} else {
				inserted += uint64(len(final))
			}
		}

		fail += uint64(len(batchErrs))
		errs = append(errs, batchErrs...)
		h.reportProgress(ctx, task.ID, total, inserted+updated, fail, calcProgress(uint64(end), total), "")
	}

	// 5. 写结果文件（失败明细）
	resultURL := ""
	if fail > 0 {
		if url, e := h.uploadErrorDetails(ctx, task.ID, asyncjob.TypeImport, errs); e == nil {
			resultURL = url
		} else {
			logger.Error(e)
		}
	}

	// 6. 终态
	summary := fmt.Sprintf("导入完成：成功 %d（新增 %d，更新 %d）", inserted+updated, inserted, updated)
	if fail == 0 {
		h.finishSuccessWithMsg(ctx, task.ID, total, inserted+updated, summary)
	} else {
		h.finishPartial(ctx, task.ID, total, inserted+updated, fail,
			fmt.Sprintf("%s，失败 %d", summary, fail), resultURL)
	}
}

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

		// 1. 解析本批数据（行级必填/格式错误直接记失败）
		var parsed []*appdetail.Row
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := appdetail.ParseRow(j+2, rows[j], nr, appdetail.ParseModeAdd)
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
// 只更新已有数据：按 ID 定位（必填），ID 之外至少填写一项要更新的字段，
// 仅更新填写的非空字段（留空保持原值）；ID 不存在或批内重复的行报失败。
// 分类列可填名称或 ID，按记录所属站点延迟解析。执行完毕生成全量结果文件。
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
		results = append(results, importResultRow{RowNo: r.RowNo, Identifier: strconv.FormatUint(r.ID, 10), OK: ok, Message: msg})
	}

	const batchSize = 50
	for i := 0; i < len(rows); i += batchSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return
		}
		end := min(i+batchSize, len(rows))

		// 1. 解析本批数据（ID 必填 + 至少一项更新字段，行级错误直接记失败）
		var parsed []*appdetail.Row
		for j := i; j < end; j++ {
			if len(rows[j]) == 0 {
				continue // 空行
			}
			r, parseErr := appdetail.ParseRow(j+2, rows[j], nr, appdetail.ParseModeUpdate)
			if parseErr != nil {
				failCnt++
				results = append(results, importResultRow{RowNo: j + 2, Identifier: appdetail.RowIdentifier(rows[j]), Message: parseErr.Error()})
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

		// 3. 存在性校验并回填站点 ID（分类按记录所属站点延迟解析）
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
			siteID, exists := siteMap[r.ID]
			if !exists {
				updResult(r, false, "ID 不存在")
				continue
			}
			r.SiteID = siteID
			if e := r.ResolveCategories(nr); e != nil {
				updResult(r, false, e.Error())
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
