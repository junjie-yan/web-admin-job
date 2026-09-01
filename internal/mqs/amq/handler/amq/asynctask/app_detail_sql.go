package asynctask

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// appDetailRow 一行 app_detail 数据（与 Excel 字段一一对应）
// 字段为指针：nil 表示该字段未填写（批量更新时跳过，导入时使用默认值）
type appDetailRow struct {
	RowNo              int    // Excel 行号（从 2 开始，便于错误定位）
	SiteID             uint64 // 必填，导入/批量更新都用
	Name               string // 必填（导入），可选（批量更新）
	URL                *string
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

// batchInsertAppDetails 批量插入 app_detail 记录
// 成功返回 nil；任意行失败则整批回滚，调用方应降级为逐条插入
func batchInsertAppDetails(ctx context.Context, db *sql.DB, rows []*appDetailRow) error {
	if len(rows) == 0 {
		return nil
	}
	const placeholders = `(?, ?, NOW(), NOW(), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	cols := []string{
		"status", "site_id", // 注意 created_at/updated_at 在 NOW()
		"name", "url", "first_category_id", "second_category_id",
		"logo", "developer", "version", "content_rating", "price", "min_android",
		"updated", "downloads", "rating", "review_count", "rating_distribution",
		"description", "screenshots", "google_play_url", "apple_store_url",
		"apk_download_url", "apk_version", "apk_size", "apk_updated",
	}
	valueStrs := make([]string, 0, len(rows))
	args := make([]interface{}, 0, len(rows)*len(cols))
	for _, r := range rows {
		status := uint8(1)
		if r.Status != nil {
			status = *r.Status
		}
		valueStrs = append(valueStrs, placeholders)
		args = append(args,
			status, r.SiteID,
			r.Name, r.URL, r.FirstCategoryID, r.SecondCategoryID,
			r.Logo, r.Developer, r.Version, r.ContentRating, r.Price, r.MinAndroid,
			r.Updated, r.Downloads, r.Rating, r.ReviewCount, r.RatingDistribution,
			r.Description, r.Screenshots, r.GooglePlayURL, r.AppleStoreURL,
			r.APKDownloadURL, r.APKVersion, r.APKSize, r.APKUpdated,
		)
	}
	query := fmt.Sprintf("INSERT INTO app_detail (%s) VALUES %s",
		strings.Join(cols, ","), strings.Join(valueStrs, ","))
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

// appDetailKey 用于批量更新定位记录的唯一键
type appDetailKey struct {
	SiteID uint64
	URL    string
}

// findAppDetailIDsBySiteAndURL 给定一批 (site_id, url) 键，返回已存在记录的键 → id 映射
// url 为空的键会被跳过（批量更新要求 url 必填）
func findAppDetailIDsBySiteAndURL(ctx context.Context, db *sql.DB, keys []appDetailKey) (map[appDetailKey]uint64, error) {
	result := make(map[appDetailKey]uint64, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	// 分批查询，避免 IN 列表过长（每批 500 条）
	const batchSize = 500
	for start := 0; start < len(keys); start += batchSize {
		end := start + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]

		// 按 site_id 分组，构造 (site_id, url IN (...)) 查询
		siteToURLs := make(map[uint64][]string)
		for _, k := range batch {
			if k.URL == "" {
				continue
			}
			siteToURLs[k.SiteID] = append(siteToURLs[k.SiteID], k.URL)
		}
		for siteID, urls := range siteToURLs {
			if len(urls) == 0 {
				continue
			}
			placeholders := make([]string, len(urls))
			args := make([]interface{}, 0, len(urls)+1)
			args = append(args, siteID)
			for i, u := range urls {
				placeholders[i] = "?"
				args = append(args, u)
			}
			q := fmt.Sprintf("SELECT id, url FROM app_detail WHERE site_id = ? AND url IN (%s)",
				strings.Join(placeholders, ","))
			rows, err := db.QueryContext(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("query app_detail: %w", err)
			}
			for rows.Next() {
				var id uint64
				var url string
				if err := rows.Scan(&id, &url); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scan app_detail: %w", err)
				}
				result[appDetailKey{SiteID: siteID, URL: url}] = id
			}
			rows.Close()
		}
	}
	return result, nil
}

// updateAppDetailByID 按 ID 更新 app_detail，仅更新非 nil 字段
func updateAppDetailByID(ctx context.Context, db *sql.DB, id uint64, r *appDetailRow) error {
	sets := []string{}
	args := []interface{}{}
	addStr := func(col string, v *string) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}
	addU64 := func(col string, v *uint64) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}
	addI64 := func(col string, v *int64) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}
	addI := func(col string, v *int) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}
	addF64 := func(col string, v *float64) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}
	addU8 := func(col string, v *uint8) {
		if v != nil {
			sets = append(sets, col+"=?")
			args = append(args, *v)
		}
	}

	addStr("name", strPtr(r.Name))
	addStr("url", r.URL)
	addU64("first_category_id", r.FirstCategoryID)
	addU64("second_category_id", r.SecondCategoryID)
	addStr("logo", r.Logo)
	addStr("developer", r.Developer)
	addStr("version", r.Version)
	addStr("content_rating", r.ContentRating)
	addStr("price", r.Price)
	addStr("min_android", r.MinAndroid)
	addStr("updated", r.Updated)
	addI64("downloads", r.Downloads)
	addF64("rating", r.Rating)
	addI("review_count", r.ReviewCount)
	addStr("rating_distribution", r.RatingDistribution)
	addStr("description", r.Description)
	addStr("screenshots", r.Screenshots)
	addStr("google_play_url", r.GooglePlayURL)
	addStr("apple_store_url", r.AppleStoreURL)
	addStr("apk_download_url", r.APKDownloadURL)
	addStr("apk_version", r.APKVersion)
	addStr("apk_size", r.APKSize)
	addStr("apk_updated", r.APKUpdated)
	addU8("status", r.Status)

	if len(sets) == 0 {
		return nil // 无字段可更新
	}
	sets = append(sets, "updated_at=NOW()")
	args = append(args, id)
	q := fmt.Sprintf("UPDATE app_detail SET %s WHERE id=?", strings.Join(sets, ","))
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

// appDetailExportRow 导出用的扁平行结构（用于写入 Excel）
// 线上数据存在 NULL 值（如历史数据的 created_at），统一使用 sql.Null* 承接，避免 Scan 报错
type appDetailExportRow struct {
	ID                uint64
	Status            sql.NullInt64
	SiteID            uint64
	Name              sql.NullString
	URL               sql.NullString
	FirstCategoryID   sql.NullInt64
	SecondCategoryID  sql.NullInt64
	Logo              sql.NullString
	Developer         sql.NullString
	Version           sql.NullString
	ContentRating     sql.NullString
	Price             sql.NullString
	MinAndroid        sql.NullString
	Updated           sql.NullString
	Downloads         sql.NullInt64
	Rating            sql.NullFloat64
	ReviewCount       sql.NullInt64
	Description       sql.NullString
	GooglePlayURL     sql.NullString
	AppleStoreURL     sql.NullString
	APKDownloadURL    sql.NullString
	APKVersion        sql.NullString
	APKSize           sql.NullString
	APKUpdated        sql.NullString
	CreatedAt         sql.NullTime
	UpdatedAt         sql.NullTime
}

// exportAppDetails 分页查询所有 app_detail（按 filter JSON 过滤，filter 暂只支持 site_id）
// 返回的切片顺序与 SQL 一致
func exportAppDetails(ctx context.Context, db *sql.DB, siteID uint64, limit, offset int) ([]*appDetailExportRow, error) {
	q := `SELECT id, status, site_id, name, url, first_category_id, second_category_id,
logo, developer, version, content_rating, price, min_android, updated, downloads,
rating, review_count, description, google_play_url, apple_store_url,
apk_download_url, apk_version, apk_size, apk_updated, created_at, updated_at
FROM app_detail`
	args := []interface{}{}
	if siteID > 0 {
		q += " WHERE site_id = ?"
		args = append(args, siteID)
	}
	q += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query app_detail for export: %w", err)
	}
	defer rows.Close()

	var result []*appDetailExportRow
	for rows.Next() {
		r := &appDetailExportRow{}
		if err := rows.Scan(
			&r.ID, &r.Status, &r.SiteID, &r.Name, &r.URL, &r.FirstCategoryID, &r.SecondCategoryID,
			&r.Logo, &r.Developer, &r.Version, &r.ContentRating, &r.Price, &r.MinAndroid, &r.Updated, &r.Downloads,
			&r.Rating, &r.ReviewCount, &r.Description, &r.GooglePlayURL, &r.AppleStoreURL,
			&r.APKDownloadURL, &r.APKVersion, &r.APKSize, &r.APKUpdated, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan app_detail export: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// countAppDetails 统计总数（用于导出分批写入时预估）
func countAppDetails(ctx context.Context, db *sql.DB, siteID uint64) (uint64, error) {
	q := "SELECT COUNT(*) FROM app_detail"
	args := []interface{}{}
	if siteID > 0 {
		q += " WHERE site_id = ?"
		args = append(args, siteID)
	}
	var n uint64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
