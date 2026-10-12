package coupon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BatchInsert 批量插入 coupon 记录（整批一条 INSERT）
// 成功返回 nil；任意行失败则整批回滚，调用方应降级为逐条插入以定位错误行
func BatchInsert(ctx context.Context, db *sql.DB, rows []*Row) error {
	if len(rows) == 0 {
		return nil
	}
	// 16 个值位置：status(?) + NOW() + NOW() + 13 个数据列(?)，与下方 cols 一一对应
	const placeholders = `(?, NOW(), NOW(), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	cols := []string{
		"status", // created_at/updated_at 由 NOW() 生成
		"merchant_id", "merchant_domain", "source_id", "offer_type",
		"code", "title", "description", "discount_text",
		"expired_date", "last_verified_at", "is_verified", "is_exclusive", "is_ongoing",
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
			r.MerchantID, r.MerchantDomain, r.SourceID, r.OfferType,
			r.Code, r.Title, r.Description, r.DiscountText,
			r.ExpiredDate, r.LastVerifiedAt, r.IsVerified, r.IsExclusive, r.IsOngoing,
		)
	}
	query := fmt.Sprintf("INSERT INTO coupon (%s) VALUES %s",
		strings.Join(cols, ","), strings.Join(valueStrs, ","))
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

// FindIDsByKey 给定一批定位键（商店域名+来源ID），返回库中已存在记录的 键 → id 映射
// 库层 UNIQUE(merchant_domain, source_id)；内部按 500/批拆分 IN 查询
func FindIDsByKey(ctx context.Context, db *sql.DB, keys []Key) (map[Key]uint64, error) {
	result := make(map[Key]uint64, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	const batchSize = 500
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		batch := keys[start:end]

		// 按 domain 分组，构造 (merchant_domain = ? AND source_id IN (...)) 查询
		domainToSources := make(map[string][]string)
		for _, k := range batch {
			if k.SourceID == "" {
				continue
			}
			domainToSources[k.MerchantDomain] = append(domainToSources[k.MerchantDomain], k.SourceID)
		}
		for domain, sources := range domainToSources {
			placeholders := make([]string, len(sources))
			args := make([]any, 0, len(sources)+1)
			args = append(args, domain)
			for i, s := range sources {
				placeholders[i] = "?"
				args = append(args, s)
			}
			q := fmt.Sprintf("SELECT id, source_id FROM coupon WHERE merchant_domain = ? AND source_id IN (%s)",
				strings.Join(placeholders, ","))
			rows, err := db.QueryContext(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("query coupon: %w", err)
			}
			for rows.Next() {
				var id uint64
				var sourceID string
				if err := rows.Scan(&id, &sourceID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan coupon: %w", err)
				}
				result[Key{MerchantDomain: domain, SourceID: sourceID}] = id
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("iterate coupon: %w", err)
			}
			_ = rows.Close()
		}
	}
	return result, nil
}

// UpdateByID 按 ID 更新 coupon，仅更新非 nil 字段（定位键 merchant_domain/source_id 不更新）；无字段可更新时直接返回
func UpdateByID(ctx context.Context, db *sql.DB, id uint64, r *Row) error {
	var sets []string
	var args []any
	addIf(&sets, &args, "merchant_id", r.MerchantID)
	addIf(&sets, &args, "offer_type", r.OfferType)
	addIf(&sets, &args, "code", r.Code)
	addIf(&sets, &args, "title", r.Title)
	addIf(&sets, &args, "description", r.Description)
	addIf(&sets, &args, "discount_text", r.DiscountText)
	addIf(&sets, &args, "expired_date", r.ExpiredDate)
	addIf(&sets, &args, "last_verified_at", r.LastVerifiedAt)
	addIf(&sets, &args, "is_verified", r.IsVerified)
	addIf(&sets, &args, "is_exclusive", r.IsExclusive)
	addIf(&sets, &args, "is_ongoing", r.IsOngoing)
	addIf(&sets, &args, "status", r.Status)

	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=NOW()")
	args = append(args, id)
	q := fmt.Sprintf("UPDATE coupon SET %s WHERE id=?", strings.Join(sets, ","))
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
