package appdetail

import (
	"database/sql"
	"strconv"
)

// ExportFilter 导出过滤参数
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	SiteID uint64 `json:"siteId"` // 0 表示导出全部
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
// 线上数据存在 NULL 值（如历史数据的 created_at），统一使用 sql.Null* 承接，避免 Scan 报错
type ExportRow struct {
	ID                 uint64
	Status             sql.NullInt64
	SiteID             uint64
	SiteName           sql.NullString
	Name               sql.NullString
	URL                sql.NullString
	FirstCategoryID    sql.NullInt64
	FirstCategoryName  sql.NullString
	SecondCategoryID   sql.NullInt64
	SecondCategoryName sql.NullString
	Logo               sql.NullString
	Developer          sql.NullString
	Version            sql.NullString
	ContentRating      sql.NullString
	Price              sql.NullString
	MinAndroid         sql.NullString
	Updated            sql.NullString
	Downloads          sql.NullInt64
	Rating             sql.NullFloat64
	ReviewCount        sql.NullInt64
	Description        sql.NullString
	GooglePlayURL      sql.NullString
	AppleStoreURL      sql.NullString
	APKDownloadURL     sql.NullString
	APKVersion         sql.NullString
	APKSize            sql.NullString
	APKUpdated         sql.NullString
	CreatedAt          sql.NullTime
	UpdatedAt          sql.NullTime
}

// ExportHeaders 导出 Excel 的表头（顺序与 ExportRowStrings 一致）
func ExportHeaders() []string {
	return []string{
		"ID", "站点ID", "站点名称", "应用名称", "详情链接",
		"一级分类ID", "一级分类名称", "二级分类ID", "二级分类名称",
		"Logo", "开发者", "版本号", "内容评级", "价格", "最低Android",
		"更新时间", "下载量", "评分", "评论数", "应用描述",
		"Google Play链接", "Apple商店链接", "APK下载链接", "APK版本号", "APK大小", "APK更新时间",
		"状态", "创建时间", "更新时间",
	}
}

// ExportRowStrings 将一行 app_detail 转为字符串切片（顺序与 ExportHeaders 一致）
// NULL 值统一输出空字符串
func ExportRowStrings(r *ExportRow) []string {
	statusStr := "启用"
	if r.Status.Valid && r.Status.Int64 == 2 {
		statusStr = "禁用"
	}
	return []string{
		strconv.FormatUint(r.ID, 10),
		strconv.FormatUint(r.SiteID, 10),
		r.SiteName.String,
		r.Name.String,
		r.URL.String,
		nullInt64Str(r.FirstCategoryID),
		r.FirstCategoryName.String,
		nullInt64Str(r.SecondCategoryID),
		r.SecondCategoryName.String,
		r.Logo.String,
		r.Developer.String,
		r.Version.String,
		r.ContentRating.String,
		r.Price.String,
		r.MinAndroid.String,
		r.Updated.String,
		nullInt64Str(r.Downloads),
		nullFloat64Str(r.Rating),
		nullInt64Str(r.ReviewCount),
		r.Description.String,
		r.GooglePlayURL.String,
		r.AppleStoreURL.String,
		r.APKDownloadURL.String,
		r.APKVersion.String,
		r.APKSize.String,
		r.APKUpdated.String,
		statusStr,
		nullTimeStr(r.CreatedAt),
		nullTimeStr(r.UpdatedAt),
	}
}

func nullInt64Str(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}

func nullFloat64Str(v sql.NullFloat64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatFloat(v.Float64, 'f', 2, 64)
}

func nullTimeStr(v sql.NullTime) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format("2006-01-02 15:04:05")
}
