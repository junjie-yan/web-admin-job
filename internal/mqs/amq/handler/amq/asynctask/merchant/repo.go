package merchant

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BatchInsert 批量插入 coupon_merchant_base 记录（整批一条 INSERT）
// 成功返回 nil；任意行失败则整批回滚，调用方应降级为逐条插入以定位错误行
// 调用方需保证：每行 Name 非 nil（新增时名称必填）
func BatchInsert(ctx context.Context, db *sql.DB, rows []*Row) error {
	if len(rows) == 0 {
		return nil
	}
	// 13 个值位置：status(?) + NOW() + NOW() + 10 个数据列(?)，与下方 cols 一一对应
	const placeholders = `(?, NOW(), NOW(), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	cols := []string{
		"status", // created_at/updated_at 由 NOW() 生成
		"domain", "name", "logo", "url", "best_discount",
		"coupon_codes", "total_offers", "avg_savings", "first_category_id", "second_category_id",
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
			status,
			r.Domain, r.Name, r.Logo, r.URL, r.BestDiscount,
			r.CouponCodes, r.TotalOffers, r.AvgSavings, r.FirstCategoryID, r.SecondCategoryID,
		)
	}
	query := fmt.Sprintf("INSERT INTO coupon_merchant_base (%s) VALUES %s",
		strings.Join(cols, ","), strings.Join(valueStrs, ","))
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

// FindIDsByDomain 给定一批商店域名，返回库中已存在记录的 域名 → id 映射（定位键，库层 UNIQUE(domain)）
// 内部按 500/批拆分 IN 查询
func FindIDsByDomain(ctx context.Context, db *sql.DB, domains []string) (map[string]uint64, error) {
	result := make(map[string]uint64, len(domains))
	if len(domains) == 0 {
		return result, nil
	}

	const batchSize = 500
	for start := 0; start < len(domains); start += batchSize {
		end := min(start+batchSize, len(domains))
		batch := domains[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch))
		for i, d := range batch {
			placeholders[i] = "?"
			args = append(args, d)
		}
		q := fmt.Sprintf("SELECT id, domain FROM coupon_merchant_base WHERE domain IN (%s)",
			strings.Join(placeholders, ","))
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("query coupon_merchant_base: %w", err)
		}
		for rows.Next() {
			var id uint64
			var domain string
			if err := rows.Scan(&id, &domain); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan coupon_merchant_base: %w", err)
			}
			result[domain] = id
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate coupon_merchant_base: %w", err)
		}
		_ = rows.Close()
	}
	return result, nil
}

// UpdateByID 按 ID 更新 coupon_merchant_base，仅更新非 nil 字段；无字段可更新时直接返回
func UpdateByID(ctx context.Context, db *sql.DB, id uint64, r *Row) error {
	var sets []string
	var args []any
	addIf(&sets, &args, "name", r.Name)
	addIf(&sets, &args, "logo", r.Logo)
	addIf(&sets, &args, "url", r.URL)
	addIf(&sets, &args, "best_discount", r.BestDiscount)
	addIf(&sets, &args, "coupon_codes", r.CouponCodes)
	addIf(&sets, &args, "total_offers", r.TotalOffers)
	addIf(&sets, &args, "avg_savings", r.AvgSavings)
	addIf(&sets, &args, "first_category_id", r.FirstCategoryID)
	addIf(&sets, &args, "second_category_id", r.SecondCategoryID)
	addIf(&sets, &args, "status", r.Status)

	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=NOW()")
	args = append(args, id)
	q := fmt.Sprintf("UPDATE coupon_merchant_base SET %s WHERE id=?", strings.Join(sets, ","))
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
