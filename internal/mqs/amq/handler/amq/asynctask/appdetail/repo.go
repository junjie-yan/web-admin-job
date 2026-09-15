package appdetail

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BatchInsert 批量插入 app_detail 记录（整批一条 INSERT）
// 成功返回 nil；任意行失败则整批回滚，调用方应降级为逐条插入以定位错误行
func BatchInsert(ctx context.Context, db *sql.DB, rows []*Row) error {
	if len(rows) == 0 {
		return nil
	}
	const placeholders = `(?, ?, NOW(), NOW(), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	cols := []string{
		"status", "site_id", // created_at/updated_at 由 NOW() 生成
		"name", "url", "first_category_id", "second_category_id",
		"logo", "developer", "version", "content_rating", "price", "min_android",
		"updated", "downloads", "rating", "review_count", "rating_distribution",
		"description", "screenshots", "google_play_url", "apple_store_url",
		"apk_download_url", "apk_version", "apk_size", "apk_updated",
	}
	valueStrs := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*len(cols))
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

// FindIDsBySiteAndURL 给定一批 Key，返回已存在记录的键 → id 映射
// url 为空的键会被跳过；内部按 500/批拆分 IN 查询，避免 IN 列表过长
func FindIDsBySiteAndURL(ctx context.Context, db *sql.DB, keys []Key) (map[Key]uint64, error) {
	result := make(map[Key]uint64, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	const batchSize = 500
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
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
			args := make([]any, 0, len(urls)+1)
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
					_ = rows.Close()
					return nil, fmt.Errorf("scan app_detail: %w", err)
				}
				result[Key{SiteID: siteID, URL: url}] = id
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("iterate app_detail: %w", err)
			}
			_ = rows.Close()
		}
	}
	return result, nil
}

// FindIDsBySiteAndName 给定一批 NameKey，返回键 → id 列表映射（同站点同名可能对应多条记录，
// 由调用方决定如何处理：通常唯一时更新，多条时要求改用站点+url 精确定位）
// 内部按 500/批拆分 IN 查询
func FindIDsBySiteAndName(ctx context.Context, db *sql.DB, keys []NameKey) (map[NameKey][]uint64, error) {
	result := make(map[NameKey][]uint64, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	const batchSize = 500
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		batch := keys[start:end]

		siteToNames := make(map[uint64][]string)
		for _, k := range batch {
			if k.Name == "" {
				continue
			}
			siteToNames[k.SiteID] = append(siteToNames[k.SiteID], k.Name)
		}
		for siteID, names := range siteToNames {
			if len(names) == 0 {
				continue
			}
			placeholders := make([]string, len(names))
			args := make([]any, 0, len(names)+1)
			args = append(args, siteID)
			for i, n := range names {
				placeholders[i] = "?"
				args = append(args, n)
			}
			q := fmt.Sprintf("SELECT id, name FROM app_detail WHERE site_id = ? AND name IN (%s)",
				strings.Join(placeholders, ","))
			rows, err := db.QueryContext(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("query app_detail by name: %w", err)
			}
			for rows.Next() {
				var id uint64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan app_detail by name: %w", err)
				}
				key := NameKey{SiteID: siteID, Name: name}
				result[key] = append(result[key], id)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("iterate app_detail by name: %w", err)
			}
			_ = rows.Close()
		}
	}
	return result, nil
}

// UpdateByID 按 ID 更新 app_detail，仅更新非 nil 字段；无字段可更新时直接返回
func UpdateByID(ctx context.Context, db *sql.DB, id uint64, r *Row) error {
	var sets []string
	var args []any
	addIf(&sets, &args, "name", strPtr(r.Name))
	addIf(&sets, &args, "url", r.URL)
	addIf(&sets, &args, "first_category_id", r.FirstCategoryID)
	addIf(&sets, &args, "second_category_id", r.SecondCategoryID)
	addIf(&sets, &args, "logo", r.Logo)
	addIf(&sets, &args, "developer", r.Developer)
	addIf(&sets, &args, "version", r.Version)
	addIf(&sets, &args, "content_rating", r.ContentRating)
	addIf(&sets, &args, "price", r.Price)
	addIf(&sets, &args, "min_android", r.MinAndroid)
	addIf(&sets, &args, "updated", r.Updated)
	addIf(&sets, &args, "downloads", r.Downloads)
	addIf(&sets, &args, "rating", r.Rating)
	addIf(&sets, &args, "review_count", r.ReviewCount)
	addIf(&sets, &args, "rating_distribution", r.RatingDistribution)
	addIf(&sets, &args, "description", r.Description)
	addIf(&sets, &args, "screenshots", r.Screenshots)
	addIf(&sets, &args, "google_play_url", r.GooglePlayURL)
	addIf(&sets, &args, "apple_store_url", r.AppleStoreURL)
	addIf(&sets, &args, "apk_download_url", r.APKDownloadURL)
	addIf(&sets, &args, "apk_version", r.APKVersion)
	addIf(&sets, &args, "apk_size", r.APKSize)
	addIf(&sets, &args, "apk_updated", r.APKUpdated)
	addIf(&sets, &args, "status", r.Status)

	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=NOW()")
	args = append(args, id)
	q := fmt.Sprintf("UPDATE app_detail SET %s WHERE id=?", strings.Join(sets, ","))
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

// addIf v 非 nil 时追加 SET 子句与绑定参数（泛型实现，覆盖各列类型）
func addIf[T any](sets *[]string, args *[]any, col string, v *T) {
	if v != nil {
		*sets = append(*sets, col+"=?")
		*args = append(*args, *v)
	}
}

// strPtr 非空字符串返回指针，空串返回 nil
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListForExport 分页查询 app_detail（按 siteID 过滤，0 表示全部）
// 关联 site / app_category 取站点名称与分类名称；返回的切片顺序与 SQL 一致
func ListForExport(ctx context.Context, db *sql.DB, siteID uint64, limit, offset int) ([]*ExportRow, error) {
	q := `SELECT a.id, a.status, a.site_id, s.name, a.name, a.url,
a.first_category_id, c1.name, a.second_category_id, c2.name,
a.logo, a.developer, a.version, a.content_rating, a.price, a.min_android, a.updated, a.downloads,
a.rating, a.review_count, a.description, a.google_play_url, a.apple_store_url,
a.apk_download_url, a.apk_version, a.apk_size, a.apk_updated, a.created_at, a.updated_at
FROM app_detail a
LEFT JOIN site s ON s.id = a.site_id
LEFT JOIN app_category c1 ON c1.id = a.first_category_id
LEFT JOIN app_category c2 ON c2.id = a.second_category_id`
	args := []any{}
	if siteID > 0 {
		q += " WHERE a.site_id = ?"
		args = append(args, siteID)
	}
	q += " ORDER BY a.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query app_detail for export: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []*ExportRow
	for rows.Next() {
		r := &ExportRow{}
		if err := rows.Scan(
			&r.ID, &r.Status, &r.SiteID, &r.SiteName, &r.Name, &r.URL,
			&r.FirstCategoryID, &r.FirstCategoryName, &r.SecondCategoryID, &r.SecondCategoryName,
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

// Count 统计总数（siteID 为 0 表示全部，用于导出分批写入时预估）
func Count(ctx context.Context, db *sql.DB, siteID uint64) (uint64, error) {
	q := "SELECT COUNT(*) FROM app_detail"
	args := []any{}
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
