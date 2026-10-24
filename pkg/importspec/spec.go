// Package importspec 是站点数据导入的「单一事实源」：以代码声明的方式登记
// 每个站点垂直（app/coupon/...）可用的导入模块（表 × 操作）、模板列规格与
// 缓存刷新页面类型清单。
//
// 消费方（见 docs/SITE-DATA-PLATFORM.md，web-admin 仓库）：
//   - Go 编译期：web-admin 据此生成导入模板表头、推导导入/导出白名单；
//     web-admin-job 据此做站点内变体解析（钉站点）
//   - HTTP 运行期：web-admin GET /async_task/modules 下发元数据，前端动态渲染
//
// 约定：
//   - 本包是唯一事实源，禁止在 web-admin / web-admin-ui 中复制列清单或模块清单
//   - 改动本包 = 契约变更：web-admin-job 发新 tag，web-admin 同 PR 升级依赖
//   - 注册表编译期构建，运行期只读
package importspec

// ColumnType 列类型（决定通用解析/校验行为；站点内变体按类型过滤列）
type ColumnType string

const (
	TSiteDomain ColumnType = "site_domain" // 站点域名（查库转 site_id；站点内变体模板去掉该列）
	TRecordID   ColumnType = "record_id"   // 记录 ID（更新模式定位键）
	TString     ColumnType = "string"
	TInt64      ColumnType = "int64"
	TFloat64    ColumnType = "float64"
	TStatus     ColumnType = "status"   // 1/0/启用/禁用/兼容历史 2 → uint8 归一化
	TCategory   ColumnType = "category" // 分类名称/ID → 按 site+level 查库转 ID
)

// Column 单列规格；Header 含必填标记（*），Required 与标记必须一致（单测锁定）
type Column struct {
	Header   string
	Key      string
	Type     ColumnType
	Required bool
	CatLevel int // TCategory 专用：1 一级 / 2 二级
}

// Operation 导入操作语义
type Operation string

const (
	OpAdd    Operation = "add"    // 只新增（不存在则失败）
	OpUpdate Operation = "update" // 只更新（不存在/不属于站点则失败）
	OpUpsert Operation = "upsert" // 存在则更新，不存在则新增（coupon/merchant 存量语义）
)

// Spec 一个「表 × 操作」导入模块的完整声明
type Spec struct {
	Module     string    // biz_module，全局唯一（白名单/dispatcher/看板共用）
	Table      string    // 目标表名（文档/排查用）
	Operation  Operation // 导入语义；空串表示该模块无导入（仅登记用于看板等）
	Entity     string    // 业务实体标识（工作台按实体分组；同实体存在 add/update 时 upsert 自动隐藏）
	EntityZh   string    // 实体中文名（工作台分组行展示）
	SiteScoped bool      // true=跨站点模板含站点域名列；站点内变体（siteId>0）去掉该列
	Columns    []Column  // 跨站点模板列规格（列序=Excel 列序，单一事实源）
	Locators   []string  // 新增/upsert 判重定位键优先级（字段 Key）
	SheetName  string    // 导入模板 sheet 名
	FileBase   string    // 导入模板文件名主体（自动补 _site 变体后缀与 .xlsx）

	NameZh string // 看板/工作台文案（不设英文名：后台无英文场景）
}

// Headers 返回跨站点模板表头（与 Columns 顺序一致）
func (s *Spec) Headers() []string {
	hs := make([]string, 0, len(s.Columns))
	for _, c := range s.Columns {
		hs = append(hs, c.Header)
	}
	return hs
}

// HeadersScoped 返回站点内变体模板表头：去掉站点域名列（操作上下文已固定 siteId，
// 运营不再手填域名，job 解析时把行钉到该站点，归属校验天然成立）。
// 更新模式的记录 ID 列保留（仍需定位具体记录）。
func (s *Spec) HeadersScoped() []string {
	hs := make([]string, 0, len(s.Columns))
	for _, c := range s.Columns {
		if c.Type == TSiteDomain {
			continue
		}
		hs = append(hs, c.Header)
	}
	return hs
}

// ScopedFileBase 站点内变体模板文件名主体（app_detail_update → app_detail_update_site）
func (s *Spec) ScopedFileBase() string {
	return s.FileBase + "_site"
}

// PageType 缓存刷新清单项；Code 统一引用本包页面类型常量（唯一事实源，
// job flusher / web-admin 刷新逻辑编译期引用同一批常量，杜绝值域漂移）
type PageType struct {
	Code   string
	NameZh string
	PerID  bool // true=支持单页刷新（page+数字记录 ID）
}

// 页面类型 Code 常量（值域与 web-api-go cache_service.go 的缓存键约定对齐；
// web-api-go 不依赖本仓，由上述注释 + 测试锁定一致性）
const (
	PageAppDetail  = "app_detail" // APP 详情列表页（键按 url，无 id 段，仅整类刷新）
	PageCoupons    = "coupons"    // coupon 首页 Top Offers
	PageCoupon     = "coupon"     // 单优惠券页
	PageMerchants  = "merchants"  // 商家列表页
	PageStore      = "store"      // 单商家商店页
	PageCategories = "categories" // 分类列表页
	PageCategory   = "category"   // 单分类页
	PageEvent      = "event"      // 活动页
	PageFooter     = "footer"     // 站点基础设施：footer 页面
	PageAds        = "ads"        // 站点基础设施：广告位
	PageDomain     = "domain"     // 站点基础设施：域名解析（全局键，siteID=0）
)

// 站点垂直 Code 常量（Registry 键，site.type 值域的管理面子集）
const (
	SiteTypeApp    = "app"
	SiteTypeCoupon = "coupon"
	SiteTypeInfra  = "site" // 基础设施垂直名（非站点类型）
)

// IsInfraPage 判断页面类型是否属于站点基础设施（缓存键挂在 site=site 垂直，
// 与站点自身类型无关）；供刷新逻辑推导垂直归属，替代各处字面 map
func IsInfraPage(code string) bool {
	for _, pt := range SitePageTypes {
		if pt.Code == code {
			return true
		}
	}
	return false
}

// IsValidPageType 判断 page 是否为 vertical 可刷新的页面类型
// （垂直自身 PageTypes ∪ 基础设施 SitePageTypes）；供刷新入口做值域校验，
// 防止拼错的页面类型拼出匹配不到任何键的 pattern、返回假成功
func IsValidPageType(vertical, page string) bool {
	return FindPageType(vertical, page) != nil
}

// FindPageType 按 vertical+page 查找页面类型声明（含 PerID 标记）；
// 基础设施页面（footer/ads/domain）优先匹配（均不支持单页刷新）。
// 未注册的 vertical 或 page 返回 nil
func FindPageType(vertical, page string) *PageType {
	for i := range SitePageTypes {
		if SitePageTypes[i].Code == page {
			return &SitePageTypes[i]
		}
	}
	v := Registry[vertical]
	if v == nil {
		return nil
	}
	for i := range v.PageTypes {
		if v.PageTypes[i].Code == page {
			return &v.PageTypes[i]
		}
	}
	return nil
}

// Vertical 一种站点类型的能力声明（site.type 的值域）
type Vertical struct {
	Code          string // app / coupon；基础设施固定 site
	NameZh        string
	Specs         []*Spec    // 该类型可用的导入模块
	ExportModules []string   // 支持异步导出的 biz_module 清单（部分模块仅有导出）
	PageTypes     []PageType // 缓存刷新页面类型清单（工作台刷新 Tab 数据源）
}

// Registry 站点垂直注册表（编译期构建，唯一事实源）
//
// 管理面值域 = 本表键集 ∪ {site 基础设施}。web-api-go 另有 blog 垂直的 C 端完整
// 实现（internal/site/blog），但管理面未开通（无导入模块/页面类型清单/工作台）；
// 开通时在 Registry 注册 blog 垂直并升级本包 tag 即可，禁止绕过本表硬编码类型。
var Registry = map[string]*Vertical{
	"app":    appVertical,
	"coupon": couponVertical,
	// "site" 为基础设施（footer/ads/域名解析），不对应站点类型，页面类型挂在 app/coupon
	// 之外由 SitePageTypes 提供管理面使用
}

// Order 垂直的稳定输出顺序（Registry 是 map，元数据接口按此顺序返回）
var Order = []string{"app", "coupon"}

// SitePageTypes 站点基础设施的缓存刷新项（按域名路由，不属于单一站点类型）
var SitePageTypes = []PageType{
	{Code: PageFooter, NameZh: "Footer 页面", PerID: false},
	{Code: PageAds, NameZh: "广告位", PerID: false},
	{Code: PageDomain, NameZh: "域名解析", PerID: false},
}

// FindSpec 按 biz_module 查找导入模块声明；找不到返回 nil
func FindSpec(module string) *Spec {
	for _, v := range Registry {
		for _, s := range v.Specs {
			if s.Module == module {
				return s
			}
		}
	}
	return nil
}

// FindVertical 按站点类型 code 查找垂直声明；找不到返回 nil
func FindVertical(code string) *Vertical {
	return Registry[code]
}

// ImportModules 返回全部支持导入的 biz_module（消费端 dispatcher / 生产端白名单同源）
func ImportModules() []string {
	var mods []string
	for _, v := range Registry {
		for _, s := range v.Specs {
			if s.Operation != "" {
				mods = append(mods, s.Module)
			}
		}
	}
	return mods
}

// VerticalModules 返回指定垂直的导入模块清单（顺序=注册顺序）
func VerticalModules(vertical string) []string {
	v := Registry[vertical]
	if v == nil {
		return nil
	}
	var mods []string
	for _, s := range v.Specs {
		if s.Operation != "" {
			mods = append(mods, s.Module)
		}
	}
	return mods
}

// ExportModules 返回全部支持异步导出的 biz_module
func ExportModules() []string {
	var mods []string
	for _, v := range Registry {
		mods = append(mods, v.ExportModules...)
	}
	return mods
}
