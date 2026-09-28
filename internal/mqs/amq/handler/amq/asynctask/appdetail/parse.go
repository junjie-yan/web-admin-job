// Package appdetail 收敛 app_detail 业务域的行解析、名称解析与 SQL 访问，
// 供 asynctask 包的导入/导出 Handler 编排调用。
package appdetail

import (
	"fmt"
	"strings"

	"github.com/junjie-yan/web-admin-job/internal/helper"
)

// Excel 模板列索引（列布局与 web-admin 模板一致）
// 列序与导出 Excel 对应字段顺序保持一致（Logo 在分类后、应用描述在评论数后）
const (
	colSite       = 0  // 站点域名或 ID（必填）
	colName       = 1  // 应用名称（第三优先级定位键）
	colURL        = 2  // 详情链接（数据字段；新增时留空按名称自动生成，更新时留空保持原值）
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
)

// Row 一行 app_detail 数据（与 Excel 字段一一对应）
// 指针字段：nil 表示该列未填写（更新已存在记录时跳过，新增时使用默认值）
type Row struct {
	RowNo              int     // Excel 行号（从 2 开始，便于错误定位）
	SiteID             uint64  // 必填
	Name               string  // 第三优先级定位键（可空，但三个定位键至少一项非空）
	URL                *string // 详情链接；新增留空时由 Handler 按名称生成 slug
	FirstCategoryID    *uint64
	SecondCategoryID   *uint64
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
	Screenshots        *string
	GooglePlayURL      *string
	AppleStoreURL      *string
	APKDownloadURL     *string
	APKVersion         *string
	APKSize            *string
	APKUpdated         *string
	Status             *uint8 // 1 启用 / 2 禁用
}

// Key 通用定位键（site_id + 字符串列值），用于 store URL 定位与详情链接占用检查
type Key struct {
	SiteID uint64
	URL    string
}

// NameKey 按名称定位记录的键（site_id + name），第三优先级定位方式（同名可能多条）
type NameKey struct {
	SiteID uint64
	Name   string
}

// ParseRow 解析单行 Excel 数据为 Row，rowNo 为 Excel 行号（从 2 开始）
// 定位键优先级：站点+GooglePlay链接 → 站点+Apple商店链接 → 站点+应用名称，至少填写一项；
// 命中已存在记录则更新填写字段，未命中则新增（upsert 语义由调用方 Handler 编排）
//
// 站点统一填域名（经 Resolver 查库转 ID），分类填名称，均兼容纯数字 ID 直接填写
// 返回 nil, nil 表示空行
func ParseRow(rowNo int, row []string, nr *Resolver) (*Row, error) {
	siteStr := strings.TrimSpace(cell(row, colSite))
	name := strings.TrimSpace(cell(row, colName))
	urlStr := strings.TrimSpace(cell(row, colURL))
	gpStr := strings.TrimSpace(cell(row, colGooglePlay))
	asStr := strings.TrimSpace(cell(row, colAppleStore))

	if siteStr == "" && name == "" && urlStr == "" && gpStr == "" && asStr == "" {
		return nil, nil // 空行
	}
	if siteStr == "" {
		return nil, rowErr(rowNo, "站点域名 必填")
	}
	if name == "" && gpStr == "" && asStr == "" {
		return nil, rowErr(rowNo, "应用名称 / GooglePlay链接 / Apple商店链接 至少填写一项（定位键）")
	}

	siteID, err := nr.ResolveSite(siteStr)
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}

	r := &Row{RowNo: rowNo, SiteID: siteID, Name: name}
	if urlStr != "" {
		r.URL = &urlStr
	}

	// 一级/二级分类（可选，名称转 ID）
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

// parseStatus 将状态字符串解析为 uint8（1 启用 / 2 禁用），无法识别时默认启用
func parseStatus(s string) uint8 {
	switch s {
	case "禁用", "2", "disable", "false", "ban":
		return 2
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
