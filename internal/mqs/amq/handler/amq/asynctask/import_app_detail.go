package asynctask

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
	"github.com/junjie-yan/web-admin-job/pkg/slugify"
)

// ImportAppDetailHandler 批量导入 APP 详情（biz_module=app_detail + type=import，upsert 语义）
//
// 定位键优先级：站点+GooglePlay链接 → 站点+Apple商店链接 → 站点+应用名称（至少填一项）。
// 定位命中：仅更新 Excel 中填写的字段，留空字段保持原值；未命中：新增记录（此时应用名称必填）。
// 新增时详情链接留空按应用名称自动生成（"Sketchbook AA" → "/app/sketchbook-aa"），
// 填写/生成的详情链接在同站点内已被占用时该行报错（应用层查重，库层无唯一约束）。
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
			r, parseErr := appdetail.ParseRow(j+2, rows[j], nr)
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
			return nil
		}
		asIDs, err := appdetail.FindIDsBySiteAndAppleStoreURL(ctx, h.svcCtx.SitehubDB, asKeys)
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
			// 未命中 → 新增：应用名称必填，避免产生无名称记录
			if r.Name == "" {
				batchErrs = append(batchErrs, fmt.Sprintf("第 %d 行: 未命中已有记录，新增时应用名称必填", r.RowNo))
				continue
			}
			// 新增候选：详情链接留空按名称生成 slug（仅在新增路径生成，避免更新时覆盖已有 url）
			if r.URL == nil {
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
			return nil
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
	return nil
}

// 编译期断言：保证 handler 实现 asynq.Handler
var _ asynq.Handler = (*ImportAppDetailHandler)(nil)
