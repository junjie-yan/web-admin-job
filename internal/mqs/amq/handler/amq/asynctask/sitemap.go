package asynctask

// sitemap 生成任务消费端（pattern=async_task:sitemap，type=sitemap，biz_module=sitemap）。
//
// 迁移自 web-datahub 的 sitemap 生成逻辑：查 sitehub 库（site/app_category/app_detail），
// 内存生成 3 个 XML 后直传 R2 公开桶固定 key（与 SEO 手工上传、nginx 回源约定一致）：
//
//	site/<domain>/sitemap/sitemap-category.xml  二级分类（lastmod=当天, weekly, 0.7）
//	site/<domain>/sitemap/sitemap-app.xml       启用 APP（lastmod=app updated_at, daily, 0.9）
//	site/<domain>/sitemap/sitemap.xml           index（指向上述两个文件）

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// sitemapDispatcher sitemap 生成任务分发器（单站点；site_id 由生产端写入 task.site_id）
type sitemapDispatcher struct {
	baseHandler
}

// NewSitemapDispatcher 创建 sitemap 生成分发器
func NewSitemapDispatcher(svcCtx *svc.ServiceContext) *sitemapDispatcher {
	return &sitemapDispatcher{baseHandler: baseHandler{svcCtx: svcCtx}}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *sitemapDispatcher) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	task, retry := h.claimTypedTask(ctx, t, asyncjob.TypeSitemap)
	if retry != nil {
		return retry
	}
	if task == nil {
		return nil
	}

	if task.SiteID == 0 {
		errMsg := fmt.Sprintf("asynctask: sitemap task %d missing site_id", task.ID)
		logger.Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	if h.checkCanceled(ctx, task.ID) {
		h.finishCanceled(ctx, task.ID)
		return nil
	}

	if err := h.generateSitemap(ctx, task); err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, err.Error())
	}
	return nil
}

// 编译期断言
var _ asynq.Handler = (*sitemapDispatcher)(nil)

// generateSitemap 为单个站点生成 3 个 sitemap 文件并上传 R2 公开桶
func (h *sitemapDispatcher) generateSitemap(ctx context.Context, task *asyncjob.Task) error {
	logger := logx.WithContext(ctx)

	domain, err := h.querySiteDomain(ctx, task.SiteID)
	if err != nil {
		return fmt.Errorf("查询站点失败: %w", err)
	}
	domain, err = sanitizeSitemapDomain(domain)
	if err != nil {
		return fmt.Errorf("站点 %d: %w", task.SiteID, err)
	}
	baseURL := "https://" + domain
	now := time.Now().Format("2006-01-02")

	inst, err := h.svcCtx.R2ByKey("sem-web")
	if err != nil {
		return fmt.Errorf("R2 公开桶未配置（R2Confs 缺少 sem-web 实例）: %w", err)
	}

	// 上传单个文件并回报进度（total=3）
	put := func(filename string, data []byte) error {
		key := fmt.Sprintf("site/%s/sitemap/%s", domain, filename)
		if _, err := helper.PutBytesToKey(ctx, inst.Client, inst.Config.Bucket, key,
			inst.Config.PublicBaseURL, "application/xml", data); err != nil {
			return fmt.Errorf("上传 %s 失败: %w", key, err)
		}
		logger.Infof("asynctask: sitemap task %d uploaded %s", task.ID, key)
		return nil
	}

	// 1. 分类 sitemap（启用二级分类）
	categories, err := h.querySitemapCategories(ctx, task.SiteID)
	if err != nil {
		return fmt.Errorf("查询分类失败: %w", err)
	}
	catURLs := make([]sitemapURL, 0, len(categories))
	for _, c := range categories {
		catURLs = append(catURLs, sitemapURL{
			Loc:      normalizeSitemapURL(c.url, baseURL, fmt.Sprintf("/category/%d", c.id)),
			LastMod:  now,
			Change:   "weekly",
			Priority: 0.7,
		})
	}
	data, err := marshalSitemap(sitemapDoc{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  catURLs,
	})
	if err != nil {
		return err
	}
	categoryName := "sitemap-category.xml"
	if err := put(categoryName, data); err != nil {
		return err
	}
	h.reportProgress(ctx, task.ID, 3, 1, 0, 33, "")

	// 2. APP sitemap（启用 APP，lastmod 取 updated_at）
	apps, err := h.querySitemapApps(ctx, task.SiteID)
	if err != nil {
		return fmt.Errorf("查询 APP 失败: %w", err)
	}
	appURLs := make([]sitemapURL, 0, len(apps))
	for _, a := range apps {
		appURLs = append(appURLs, sitemapURL{
			Loc:      normalizeSitemapURL(a.url, baseURL, fmt.Sprintf("/app/%d", a.id)),
			LastMod:  a.updatedAt.Format("2006-01-02"),
			Change:   "daily",
			Priority: 0.9,
		})
	}
	data, err = marshalSitemap(sitemapDoc{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  appURLs,
	})
	if err != nil {
		return err
	}
	appName := "sitemap-app.xml"
	if err := put(appName, data); err != nil {
		return err
	}
	h.reportProgress(ctx, task.ID, 3, 2, 0, 66, "")

	// 3. sitemap index（对外 /sitemap.xml，由 nginx 反代 R2）
	data, err = marshalSitemapIndex([]sitemapRef{
		{Loc: fmt.Sprintf("%s/%s", baseURL, categoryName), LastMod: now},
		{Loc: fmt.Sprintf("%s/%s", baseURL, appName), LastMod: now},
	})
	if err != nil {
		return err
	}
	if err := put("sitemap.xml", data); err != nil {
		return err
	}

	h.finishSuccessWithMsg(ctx, task.ID, 3, 3,
		fmt.Sprintf("生成完成：sitemap.xml / %s / %s 已上传（%d 个分类页，%d 个 APP 页）",
			categoryName, appName, len(categories), len(apps)))
	return nil
}

// sitemapCatRow 分类 sitemap 查询行
type sitemapCatRow struct {
	id  uint64
	url string
}

// sitemapAppRow APP sitemap 查询行
type sitemapAppRow struct {
	id        uint64
	url       string
	updatedAt time.Time
}

// querySiteDomain 查询站点域名（不过滤 status，显式生成不受站点禁用影响）
func (h *sitemapDispatcher) querySiteDomain(ctx context.Context, siteID uint64) (string, error) {
	var domain string
	err := h.svcCtx.SitehubDB.QueryRowContext(ctx,
		`SELECT domain FROM site WHERE id = ?`, siteID).Scan(&domain)
	if err != nil {
		return "", fmt.Errorf("站点 %d 不存在或查询失败: %w", siteID, err)
	}
	return domain, nil
}

// querySitemapCategories 查询站点启用的二级分类（与 datahub FindLevel2ForSitemap 一致）
func (h *sitemapDispatcher) querySitemapCategories(ctx context.Context, siteID uint64) ([]sitemapCatRow, error) {
	rows, err := h.svcCtx.SitehubDB.QueryContext(ctx,
		`SELECT id, url FROM app_category WHERE site_id = ? AND status = 1 AND level = 2`, siteID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]sitemapCatRow, 0, 64)
	for rows.Next() {
		var r sitemapCatRow
		if err := rows.Scan(&r.id, &r.url); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// querySitemapApps 查询站点启用的 APP（与 datahub FindForSitemap 一致）
func (h *sitemapDispatcher) querySitemapApps(ctx context.Context, siteID uint64) ([]sitemapAppRow, error) {
	rows, err := h.svcCtx.SitehubDB.QueryContext(ctx,
		`SELECT id, url, updated_at FROM app_detail WHERE site_id = ? AND status = 1`, siteID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]sitemapAppRow, 0, 1024)
	for rows.Next() {
		var r sitemapAppRow
		if err := rows.Scan(&r.id, &r.url, &r.updatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
