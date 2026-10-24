package importspec

import (
	"slices"
	"testing"
)

// 基准表头：与线上（2026-10-20 发布）web-admin 手写模板函数逐列一致的快照。
// importspec 接管模板生成后，此测试保证生成的模板与历史格式完全兼容；
// 刻意改列序时同步修改此处即可（review 时一目了然）。
var goldenAppDetailAdd = []string{
	"站点域名*", "应用名称*", "详情链接", "一级分类名称*", "二级分类名称",
	"Logo", "开发者", "版本号", "价格", "最低Android", "下载量",
	"评分", "评论数", "应用描述", "GooglePlay链接(二选一)", "Apple商店链接(二选一)", "APK下载链接",
	"APK版本号", "APK大小", "APK更新时间", "状态", "短描述",
}

var goldenAppDetailUpdate = []string{
	"站点域名*", "记录ID*", "应用名称", "详情链接", "一级分类名称", "二级分类名称",
	"Logo", "开发者", "版本号", "价格", "最低Android", "下载量",
	"评分", "评论数", "应用描述", "GooglePlay链接", "Apple商店链接", "APK下载链接",
	"APK版本号", "APK大小", "APK更新时间", "状态", "短描述",
}

var goldenCoupon = []string{
	"商店域名*", "来源ID*", "类型", "优惠Code", "标题", "描述", "折扣文本",
	"过期时间", "最后验证时间", "人工验证", "独家", "持续有效", "状态",
}

var goldenMerchant = []string{
	"商店域名*", "商店名称", "Logo", "主页URL", "最佳折扣", "可用Code数",
	"总Offers数", "平均节省", "一级分类名称", "二级分类名称", "状态",
}

// TestHeadersMatchGolden spec 生成的表头必须与线上历史模板逐列一致
func TestHeadersMatchGolden(t *testing.T) {
	cases := []struct {
		module string
		golden []string
	}{
		{"app_detail_add", goldenAppDetailAdd},
		{"app_detail_update", goldenAppDetailUpdate},
		// upsert 与新增共用列布局（存在更新非空字段、不存在新增）
		{"app_detail_upsert", goldenAppDetailAdd},
		{"coupon", goldenCoupon},
		{"coupon_merchant_base", goldenMerchant},
	}
	for _, c := range cases {
		s := FindSpec(c.module)
		if s == nil {
			t.Fatalf("模块 %s 未在注册表登记", c.module)
		}
		if !slices.Equal(s.Headers(), c.golden) {
			t.Fatalf("模块 %s 表头与基准不一致:\n got: %v\nwant: %v", c.module, s.Headers(), c.golden)
		}
	}
}

// TestRequiredMarkConsistency Header 的 * 必填标记必须与 Required 字段一致
func TestRequiredMarkConsistency(t *testing.T) {
	for _, v := range Registry {
		for _, s := range v.Specs {
			for _, c := range s.Columns {
				marked := len(c.Header) > 0 && c.Header[len(c.Header)-1] == '*'
				if marked != c.Required {
					t.Fatalf("%s 列 %q: Header 标记(%v)与 Required(%v)不一致", s.Module, c.Header, marked, c.Required)
				}
			}
		}
	}
}

// TestHeadersScoped 站点内变体：去掉站点域名列，其余列序不变
func TestHeadersScoped(t *testing.T) {
	s := FindSpec("app_detail_update")
	scoped := s.HeadersScoped()
	if len(scoped) != len(s.Headers())-1 {
		t.Fatalf("变体列数应少 1（站点域名），got %d want %d", len(scoped), len(s.Headers())-1)
	}
	if scoped[0] != "记录ID*" {
		t.Fatalf("变体第 0 列应为 记录ID*，got %q", scoped[0])
	}
	add := FindSpec("app_detail_add")
	scopedAdd := add.HeadersScoped()
	if scopedAdd[0] != "应用名称*" {
		t.Fatalf("新增变体第 0 列应为 应用名称*，got %q", scopedAdd[0])
	}
}

// TestRegistryModules 白名单推导结果与当前线上白名单一致
func TestRegistryModules(t *testing.T) {
	wantImport := []string{
		"app_category_add", "app_category_update",
		"app_detail_add", "app_detail_update", "app_detail_upsert",
		"coupon", "coupon_category_add", "coupon_category_update",
		"coupon_merchant_add", "coupon_merchant_base", "coupon_merchant_update",
	}
	gotImport := ImportModules()
	slices.Sort(gotImport)
	if !slices.Equal(gotImport, wantImport) {
		t.Fatalf("导入白名单不一致: got %v want %v", gotImport, wantImport)
	}
	wantExport := []string{"app_detail", "coupon", "coupon_category_base", "coupon_merchant_base"}
	gotExport := ExportModules()
	slices.Sort(gotExport)
	if !slices.Equal(gotExport, wantExport) {
		t.Fatalf("导出白名单不一致: got %v want %v", gotExport, wantExport)
	}
}

// TestFindPageTypePerID 页面类型 PerID 标记与缓存键格式对齐：
// 只有键含数字 id 段的页面（store/coupon/category/event）才允许单页刷新；
// footer/ads/domain/app_detail/categories/merchants/coupons 键无 id 段，
// 携带 recordId 会拼出匹配不到任何键的 pattern（假成功），刷新入口据此置 0
func TestFindPageTypePerID(t *testing.T) {
	perIDPages := map[string]bool{
		"store": true, "coupon": true, "category": true, "event": true,
	}
	for vertical, pages := range map[string][]string{
		"app":    {"app_detail", "categories", "footer", "ads", "domain"},
		"coupon": {"coupons", "merchants", "store", "coupon", "categories", "category", "event", "footer", "ads", "domain"},
	} {
		for _, page := range pages {
			pt := FindPageType(vertical, page)
			if pt == nil {
				t.Fatalf("%s/%s 应为可刷新页面类型", vertical, page)
			}
			if pt.PerID != perIDPages[page] {
				t.Errorf("%s/%s PerID=%v, 期望 %v", vertical, page, pt.PerID, perIDPages[page])
			}
		}
	}
	if pt := FindPageType("app", "nonexistent"); pt != nil {
		t.Errorf("未注册页面应返回 nil, got %+v", pt)
	}
}
