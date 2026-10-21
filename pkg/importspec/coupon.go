package importspec

// coupon 垂直（优惠券站）的模块与页面类型声明。
// 导入列序与 web-admin 旧手写模板函数（coupon/merchant）逐列一致；
// coupon/merchant 的 spec 现阶段仅用于模板生成与白名单推导，
// 消费端解析仍在各自子包（第 3 期迁移到通用引擎）。

var couponVertical = &Vertical{
	Code:   "coupon",
	NameZh: "Coupon站点",
	Specs: []*Spec{
		{
			Module:     "coupon",
			Table:      "coupon",
			Operation:  OpUpsert,
			SiteScoped: true,
			Locators:   []string{"merchant_domain", "source_id"},
			SheetName:  "优惠券导入模板",
			FileBase:   "coupon_import",
			NameZh:     "Coupon·优惠券",
			Columns: []Column{
				{Header: "商店域名*", Key: "merchant_domain", Type: TSiteDomain, Required: true},
				{Header: "来源ID*", Key: "source_id", Type: TString, Required: true},
				{Header: "类型", Key: "type", Type: TString},
				{Header: "优惠Code", Key: "code", Type: TString},
				{Header: "标题", Key: "title", Type: TString},
				{Header: "描述", Key: "description", Type: TString},
				{Header: "折扣文本", Key: "discount_text", Type: TString},
				{Header: "过期时间", Key: "expired_at", Type: TString},
				{Header: "最后验证时间", Key: "verified_at", Type: TString},
				{Header: "人工验证", Key: "manual_verified", Type: TString},
				{Header: "独家", Key: "exclusive", Type: TString},
				{Header: "持续有效", Key: "ongoing", Type: TString},
				{Header: "状态", Key: "status", Type: TStatus},
			},
		},
		{
			Module:     "coupon_merchant_base",
			Table:      "coupon_merchant",
			Operation:  OpUpsert,
			SiteScoped: true,
			Locators:   []string{"merchant_domain"},
			SheetName:  "优惠券商家导入模板",
			FileBase:   "coupon_merchant_base_import",
			NameZh:     "Coupon·商家",
			Columns: []Column{
				{Header: "商店域名*", Key: "merchant_domain", Type: TSiteDomain, Required: true},
				{Header: "商店名称", Key: "name", Type: TString},
				{Header: "Logo", Key: "logo", Type: TString},
				{Header: "主页URL", Key: "url", Type: TString},
				{Header: "最佳折扣", Key: "best_discount", Type: TString},
				{Header: "可用Code数", Key: "code_count", Type: TInt64},
				{Header: "总Offers数", Key: "offer_count", Type: TInt64},
				{Header: "平均节省", Key: "avg_saving", Type: TString},
				{Header: "一级分类名称", Key: "first_category", Type: TCategory, CatLevel: 1},
				{Header: "二级分类名称", Key: "second_category", Type: TCategory, CatLevel: 2},
				{Header: "状态", Key: "status", Type: TStatus},
			},
		},
	},
	ExportModules: []string{"coupon", "coupon_merchant_base", "coupon_category_base"},
	// PageTypes 对齐 web-api-go cache_keys.go 的 coupon 页面类型
	PageTypes: []PageType{
		{Code: "coupons", NameZh: "首页 Top Offers", PerID: false},
		{Code: "store", NameZh: "商店页", PerID: true},
		{Code: "coupon", NameZh: "优惠券详情页", PerID: true},
		{Code: "merchants", NameZh: "商家页", PerID: false},
		{Code: "categories", NameZh: "分类列表", PerID: false},
		{Code: "category", NameZh: "单分类页", PerID: true},
		{Code: "event", NameZh: "活动页", PerID: true},
	},
}
