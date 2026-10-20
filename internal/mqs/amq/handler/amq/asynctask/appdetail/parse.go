// Package appdetail 收敛 app_detail 业务域的行解析、名称解析与 SQL 访问，
// 供 asynctask 包的导入/导出 Handler 编排调用。
package appdetail

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/junjie-yan/web-admin-job/internal/helper"
)

// Excel 模板列索引（列布局与 web-admin 模板一致）
// 批量新增模板列 0 为站点域名；批量更新模板前两列为 站点域名+记录ID，其余列单独定义（见 uCol*）
const (
	colSite       = 0  // 站点域名（必填）
	colName       = 1  // 应用名称（必填）
	colURL        = 2  // 详情链接（新增留空按名称生成 slug，更新留空保持原值）
	colFirstCat   = 3  // 一级分类名称或 ID
	colSecondCat  = 4  // 二级分类名称或 ID
	colLogo       = 5  // Logo 地址
	colDeveloper  = 6  // 开发者
	colVersion    = 7  // 版本号
	colPrice      = 8  // 价格
	colMinAndroid = 9  // 最低 Android 版本
	colDownloads  = 10 // 下载量
	colRating     = 11 // 评分
	colReviewCnt  = 12 // 评论数
	colDesc       = 13 // 应用描述
	colGooglePlay = 14 // Google Play 链接
	colAppleStore = 15 // Apple 商店链接
	colAPKURL     = 16 // APK 下载链接
	colAPKVersion = 17 // APK 版本号
	colAPKSize    = 18 // APK 大小
	colAPKUpdated = 19 // APK 更新时间
	colStatus     = 20 // 状态（启用/禁用）
	colShortDesc  = 21 // 短描述（搜索列表页展示，纯文本；追加在末尾以兼容旧模板）
)

// 批量更新模板列索引：前两列为 站点域名 + 记录ID（均必填），列 2..22 与新增模板字段一致（整体后移一位）
const (
	uColSite       = 0  // 站点域名（必填，与记录归属比对，防止跨站点误更新）
	uColID         = 1  // 记录 ID（必填）
	uColName       = 2  // 应用名称
	uColURL        = 3  // 详情链接
	uColFirstCat   = 4  // 一级分类名称或 ID
	uColSecondCat  = 5  // 二级分类名称或 ID
	uColLogo       = 6  // Logo 地址
	uColDeveloper  = 7  // 开发者
	uColVersion    = 8  // 版本号
	uColPrice      = 9  // 价格
	uColMinAndroid = 10 // 最低 Android 版本
	uColDownloads  = 11 // 下载量
	uColRating     = 12 // 评分
	uColReviewCnt  = 13 // 评论数
	uColDesc       = 14 // 应用描述
	uColGooglePlay = 15 // Google Play 链接
	uColAppleStore = 16 // Apple 商店链接
	uColAPKURL     = 17 // APK 下载链接
	uColAPKVersion = 18 // APK 版本号
	uColAPKSize    = 19 // APK 大小
	uColAPKUpdated = 20 // APK 更新时间
	uColStatus     = 21 // 状态（启用/禁用）
	uColShortDesc  = 22 // 短描述
)

// ParseMode 行解析模式（决定模板列布局与必填校验规则）
type ParseMode int

const (
	ParseModeAdd    ParseMode = iota // 批量新增：站点列 + 必填项，只新增不存在的数据
	ParseModeUpdate                  // 批量更新：站点域名 + 记录ID 列 + 任意字段，只更新已有数据
)

// Row 一行 app_detail 数据（与 Excel 字段一一对应）
// 指针字段：nil 表示该列未填写（更新已存在记录时跳过，新增时使用默认值）
type Row struct {
	RowNo int    // Excel 行号（从 2 开始，便于错误定位）
	ID    uint64 // 更新模式：目标记录 ID（必填）；新增模式为 0

	SiteID     uint64  // 站点 ID：由站点域名列解析（两种模式均必填）；update 模式另用于记录归属校验
	SiteDomain string  // update 模式：行内填写的站点域名原文（结果文件展示用）
	Name       string  // 应用名称（add 模式必填）
	URL        *string // 详情链接；新增留空时由 Handler 按名称生成 slug

	FirstCategoryID  *uint64
	SecondCategoryID *uint64

	Logo               *string
	Developer          *string
	Version            *string
	ContentRating      *string
	Price              *string
	MinAndroid         *string
	Updated            *string
	Downloads          *int64
	Rating             *float64
	ReviewCount        *int
	RatingDistribution *string
	Description        *string
	ShortDesc          *string // 短描述（搜索列表页展示，纯文本）
	Screenshots        *string
	GooglePlayURL      *string
	AppleStoreURL      *string
	APKDownloadURL     *string
	APKVersion         *string
	APKSize            *string
	APKUpdated         *string
	Status             *uint8 // 1 启用 / 0 禁用
}

// Key 通用定位键（site_id + 字符串列值），用于 store URL 定位与详情链接占用检查
type Key struct {
	SiteID uint64
	URL    string
}

// ParseRow 解析单行 Excel 数据为 Row，rowNo 为 Excel 行号（从 2 开始）
// 站点统一填域名（经 Resolver 查库转 ID），分类填名称，均兼容纯数字 ID 直接填写
// 返回 nil, nil 表示空行
func ParseRow(rowNo int, row []string, nr *Resolver, mode ParseMode) (*Row, error) {
	if mode == ParseModeUpdate {
		return parseUpdateRow(rowNo, row, nr)
	}
	return parseAddRow(rowNo, row, nr)
}

// parseAddRow 解析批量新增模板的行（列 0 = 站点域名，列 1.. 为字段）
// 必填项：站点域名、应用名称、一级分类、GooglePlay/Apple商店链接（二选一）；
// Logo/评分/二级分类 允许为空（logo/second_category_id 落 NULL，rating 落 0），后期人工补录
func parseAddRow(rowNo int, row []string, nr *Resolver) (*Row, error) {
	siteStr := strings.TrimSpace(cell(row, colSite))
	name := strings.TrimSpace(cell(row, colName))
	urlStr := strings.TrimSpace(cell(row, colURL))
	gpStr := strings.TrimSpace(cell(row, colGooglePlay))
	asStr := strings.TrimSpace(cell(row, colAppleStore))

	if siteStr == "" && name == "" && urlStr == "" && gpStr == "" && asStr == "" {
		return nil, nil // 空行
	}
	switch {
	case siteStr == "":
		return nil, rowErr(rowNo, "站点域名 必填")
	case name == "":
		return nil, rowErr(rowNo, "应用名称 必填")
	case strings.TrimSpace(cell(row, colFirstCat)) == "":
		return nil, rowErr(rowNo, "一级分类 必填")
	case gpStr == "" && asStr == "":
		return nil, rowErr(rowNo, "GooglePlay链接 与 Apple商店链接 至少填写一项")
	}

	siteID, err := nr.ResolveSite(siteStr)
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}

	r := &Row{RowNo: rowNo, SiteID: siteID, Name: name}
	if urlStr != "" {
		r.URL = &urlStr
	}

	// 一级/二级分类（名称转 ID，兼容纯数字 ID）
	if v := strings.TrimSpace(cell(row, colFirstCat)); v != "" {
		id, e := nr.ResolveCategory(siteID, 1, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.FirstCategoryID = &id
	}
	if v := strings.TrimSpace(cell(row, colSecondCat)); v != "" {
		id, e := nr.ResolveCategory(siteID, 2, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.SecondCategoryID = &id
	}

	// 字符串列（可选）：非空才填写
	strCols := []struct {
		col int
		dst **string
	}{
		{colDeveloper, &r.Developer},
		{colVersion, &r.Version},
		{colPrice, &r.Price},
		{colMinAndroid, &r.MinAndroid},
		{colGooglePlay, &r.GooglePlayURL},
		{colAppleStore, &r.AppleStoreURL},
		{colAPKURL, &r.APKDownloadURL},
		{colAPKVersion, &r.APKVersion},
		{colAPKSize, &r.APKSize},
		{colAPKUpdated, &r.APKUpdated},
		{colLogo, &r.Logo},
		{colDesc, &r.Description},
		{colShortDesc, &r.ShortDesc},
	}
	for _, c := range strCols {
		if v := strings.TrimSpace(cell(row, c.col)); v != "" {
			*c.dst = &v
		}
	}

	// 数值列（可选）：解析失败按行级错误处理
	downloads, err := helper.ParseInt64(cell(row, colDownloads))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.Downloads = downloads
	rating, err := helper.ParseFloat64(cell(row, colRating))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.Rating = rating
	reviewCount, err := helper.ParseInt(cell(row, colReviewCnt))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.ReviewCount = reviewCount

	// 状态（可选）：默认启用
	if v := strings.TrimSpace(cell(row, colStatus)); v != "" {
		st := parseStatus(v)
		r.Status = &st
	}
	return r, nil
}

// parseUpdateRow 解析 站点域名+记录ID 开头的行（update 模式）
// 列 0 = 站点域名（必填），列 1 = 记录 ID（必填），列 2..22 与新增模板字段一致（整体后移一位）
// 站点域名即时解析为 SiteID：用于分类解析，并由 Handler 与记录实际归属站点比对（不一致则该行失败）
func parseUpdateRow(rowNo int, row []string, nr *Resolver) (*Row, error) {
	// 空行判断：所有列均为空
	allEmpty := true
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		return nil, nil
	}

	domain := strings.TrimSpace(cell(row, uColSite))
	if domain == "" {
		return nil, rowErr(rowNo, "站点域名 必填")
	}
	idStr := strings.TrimSpace(cell(row, uColID))
	if idStr == "" {
		return nil, rowErr(rowNo, "记录 ID 必填")
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		return nil, rowErr(rowNo, fmt.Sprintf("记录 ID 无效: %s", idStr))
	}

	siteID, err := nr.ResolveSite(domain)
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}

	r := &Row{RowNo: rowNo, ID: id, SiteID: siteID, SiteDomain: domain, Name: strings.TrimSpace(cell(row, uColName))}
	if v := strings.TrimSpace(cell(row, uColURL)); v != "" {
		r.URL = &v
	}

	// 一级/二级分类：按行内站点即时解析（名称转 ID，兼容纯数字 ID）
	if v := strings.TrimSpace(cell(row, uColFirstCat)); v != "" {
		catID, e := nr.ResolveCategory(siteID, 1, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.FirstCategoryID = &catID
	}
	if v := strings.TrimSpace(cell(row, uColSecondCat)); v != "" {
		catID, e := nr.ResolveCategory(siteID, 2, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.SecondCategoryID = &catID
	}

	// 字符串列（可选）：非空才填写
	strCols := []struct {
		col int
		dst **string
	}{
		{uColDeveloper, &r.Developer},
		{uColVersion, &r.Version},
		{uColPrice, &r.Price},
		{uColMinAndroid, &r.MinAndroid},
		{uColGooglePlay, &r.GooglePlayURL},
		{uColAppleStore, &r.AppleStoreURL},
		{uColAPKURL, &r.APKDownloadURL},
		{uColAPKVersion, &r.APKVersion},
		{uColAPKSize, &r.APKSize},
		{uColAPKUpdated, &r.APKUpdated},
		{uColLogo, &r.Logo},
		{uColDesc, &r.Description},
		{uColShortDesc, &r.ShortDesc},
	}
	for _, c := range strCols {
		if v := strings.TrimSpace(cell(row, c.col)); v != "" {
			*c.dst = &v
		}
	}

	// 数值列（可选）：解析失败按行级错误处理
	downloads, err := helper.ParseInt64(cell(row, uColDownloads))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.Downloads = downloads
	rating, err := helper.ParseFloat64(cell(row, uColRating))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.Rating = rating
	reviewCount, err := helper.ParseInt(cell(row, uColReviewCnt))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.ReviewCount = reviewCount

	// 状态（可选）：1 启用 / 0 禁用
	if v := strings.TrimSpace(cell(row, uColStatus)); v != "" {
		st := parseStatus(v)
		r.Status = &st
	}

	// 更新模式要求 站点域名+记录ID 之外至少填写一项要更新的字段
	if !r.hasUpdatableFields() {
		return nil, rowErr(rowNo, "站点域名与记录 ID 之外至少填写一项要更新的字段")
	}
	return r, nil
}

// hasUpdatableFields 是否填写了至少一项待更新字段（update 模式校验用）
func (r *Row) hasUpdatableFields() bool {
	return r.Name != "" || r.URL != nil || r.FirstCategoryID != nil || r.SecondCategoryID != nil ||
		r.Logo != nil || r.Developer != nil || r.Version != nil || r.Price != nil ||
		r.MinAndroid != nil || r.Downloads != nil || r.Rating != nil || r.ReviewCount != nil ||
		r.Description != nil || r.ShortDesc != nil ||
		r.GooglePlayURL != nil || r.AppleStoreURL != nil ||
		r.APKDownloadURL != nil || r.APKVersion != nil || r.APKSize != nil || r.APKUpdated != nil ||
		r.Status != nil
}

// RowIdentifier 提取批量新增行的标识用于结果文件展示：优先应用名称，其次 GooglePlay/Apple 商店链接，最后站点列
func RowIdentifier(row []string) string {
	for _, col := range []int{colName, colGooglePlay, colAppleStore, colSite} {
		if v := strings.TrimSpace(cell(row, col)); v != "" {
			return v
		}
	}
	return "-"
}

// UpdateRowIdentifier 提取批量更新行的标识用于结果文件展示（更新模板列序）：
// 站点域名#记录ID 优先；两者缺一时取已填的一项；都缺时依次回退应用名称、商店链接
func UpdateRowIdentifier(row []string) string {
	domain := strings.TrimSpace(cell(row, uColSite))
	id := strings.TrimSpace(cell(row, uColID))
	switch {
	case domain != "" && id != "":
		return domain + "#" + id
	case domain != "":
		return domain
	case id != "":
		return id
	}
	for _, col := range []int{uColName, uColGooglePlay, uColAppleStore} {
		if v := strings.TrimSpace(cell(row, col)); v != "" {
			return v
		}
	}
	return "-"
}

// parseStatus 将状态字符串解析为 uint8（1 启用 / 0 禁用，与项目前端约定一致；兼容历史填法 2），无法识别时默认启用
func parseStatus(s string) uint8 {
	switch s {
	case "禁用", "0", "2", "disable", "false", "ban":
		return 0
	default:
		return 1
	}
}

// cell 安全取 row 指定列字符串，越界返回空串
func cell(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return row[col]
}

// rowErr 包装行级错误，附 Excel 行号便于定位
func rowErr(rowNo int, msg string) error {
	return fmt.Errorf("第 %d 行: %s", rowNo, msg)
}
