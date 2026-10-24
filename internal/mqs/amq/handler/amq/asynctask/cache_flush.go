package asynctask

import (
	"context"
	"fmt"

	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/cacheflush"
	"github.com/junjie-yan/web-admin-job/pkg/importspec"
)

// 站点模块与页面类型常量（别名引用，单一事实源见 pkg/importspec）
const (
	siteApp    = importspec.SiteTypeApp
	siteCoupon = importspec.SiteTypeCoupon

	pageAppDetail  = importspec.PageAppDetail  // APP 详情列表页
	pageCategories = importspec.PageCategories // 分类列表页
	pageCoupons    = importspec.PageCoupons    // 优惠券列表页
	pageCoupon     = importspec.PageCoupon     // 单优惠券页
	pageMerchants  = importspec.PageMerchants  // 商家列表页
	pageStore      = importspec.PageStore      // 单商家商店页
	pageCategory   = importspec.PageCategory   // 单分类页
)

// cacheFlusher 聚合单次导入任务内所有成功写入涉及的缓存范围：
// 写入成功路径调用 add 记录（内部去重），process 结束时 flush 统一调用刷新接口。
// 页面级刷新用「整类前缀」模式（site + site_id + page），不逐条记录 ID 分批，
// 一次导入任务最多产生几条刷新请求，避免刷新风暴（见 SITE-DATA-PLATFORM.md §7）。
type cacheFlusher struct {
	cfg  cacheflush.Config
	reqs map[string]cacheflush.RefreshRequest // "site|siteID|page" → 请求
}

func newCacheFlusher(svcCtx *svc.ServiceContext) *cacheFlusher {
	return &cacheFlusher{
		cfg: cacheflush.Config{
			BaseURL:      svcCtx.Config.WebAPIConf.BaseURL,
			RefreshToken: svcCtx.Config.WebAPIConf.RefreshToken,
		},
		reqs: make(map[string]cacheflush.RefreshRequest),
	}
}

// add 记录一个待刷新范围（site + siteID + pages...），同范围重复记录自动去重
func (f *cacheFlusher) add(site string, siteID uint64, pages ...string) {
	for _, page := range pages {
		key := fmt.Sprintf("%s|%d|%s", site, siteID, page)
		f.reqs[key] = cacheflush.RefreshRequest{Site: site, SiteID: siteID, Page: page}
	}
}

// flush 统一刷新（在 handler 的 process 中 defer 调用，覆盖失败/取消路径已写入的数据）。
// 刷新失败仅记日志不阻断业务，C 端仍有 TTL 兜底。
func (f *cacheFlusher) flush(ctx context.Context) {
	if len(f.reqs) == 0 {
		return
	}
	logger := logx.WithContext(ctx)
	for _, req := range f.reqs {
		deleted, err := cacheflush.Refresh(ctx, f.cfg, req)
		if err != nil {
			logger.Errorf("刷新 C 端缓存失败: site=%s siteID=%d page=%s err=%v", req.Site, req.SiteID, req.Page, err)
			continue
		}
		logger.Infof("已刷新 C 端缓存: site=%s siteID=%d page=%s deleted=%d", req.Site, req.SiteID, req.Page, deleted)
	}
}
