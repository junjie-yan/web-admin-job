package merchant

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	Name   string `json:"name"`   // 商店名称模糊匹配，空=全部
	Domain string `json:"domain"` // 商店域名精确匹配，空=全部
	Status *uint8 `json:"status"` // nil=全部，1 启用 / 0 禁用
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
// 线上数据存在 NULL 值，统一使用 sql.Null* 承接，避免 Scan 报错
type ExportRow struct {
	ID                 uint64
	Status             sql.NullInt64
	Name               sql.NullString
	Domain             sql.NullString
	Logo               sql.NullString
	URL                sql.NullString
	BestDiscount       sql.NullString
	CouponCodes        sql.NullInt64
	TotalOffers        sql.NullInt64
	AvgSavings         sql.NullString
	FirstCategoryID    sql.NullInt64
	FirstCategoryName  sql.NullString
	SecondCategoryID   sql.NullInt64
	SecondCategoryName sql.NullString
	CreatedAt          sql.NullTime
	UpdatedAt          sql.NullTime
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Name != "" {
		conds = append(conds, "a.name LIKE ?")
		args = append(args, "%"+f.Name+"%")
	}
	if f.Domain != "" {
		conds = append(conds, "a.domain = ?")
		args = append(args, f.Domain)
	}
	if f.Status != nil {
		conds = append(conds, "a.status = ?")
		args = append(args, *f.Status)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Count 统计导出总数
func Count(ctx context.Context, db *sql.DB, f ExportFilter) (uint64, error) {
	where, args := exportWhere(f)
	q := "SELECT COUNT(*) FROM coupon_merchant_base a" + where
	var n uint64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 coupon_merchant_base；关联 coupon_category_base 取分类名称
// 返回的切片顺序与 SQL 一致
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT a.id, a.status, a.name, a.domain, a.logo, a.url, a.best_discount,
a.coupon_codes, a.total_offers, a.avg_savings,
a.first_category_id, c1.name, a.second_category_id, c2.name, a.created_at, a.updated_at
FROM coupon_merchant_base a
LEFT JOIN coupon_category_base c1 ON c1.id = a.first_category_id
LEFT JOIN coupon_category_base c2 ON c2.id = a.second_category_id` + where +
		" ORDER BY a.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []*ExportRow
	for rows.Next() {
		r := &ExportRow{}
		if err := rows.Scan(
			&r.ID, &r.Status, &r.Name, &r.Domain, &r.Logo, &r.URL, &r.BestDiscount,
			&r.CouponCodes, &r.TotalOffers, &r.AvgSavings,
			&r.FirstCategoryID, &r.FirstCategoryName, &r.SecondCategoryID, &r.SecondCategoryName,
			&r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ExportHeaders 导出 Excel 的表头（顺序与 ExportRowStrings 一致）
func ExportHeaders() []string {
	return []string{
		"ID", "状态", "商店名称", "商店域名", "Logo", "主页URL", "最佳折扣",
		"可用Code数", "总Offers数", "平均节省",
		"一级分类ID", "一级分类名称", "二级分类ID", "二级分类名称",
		"创建时间", "更新时间",
	}
}

// ExportRowStrings 将一行转为字符串切片（顺序与 ExportHeaders 一致），NULL 输出空串
func ExportRowStrings(r *ExportRow) []string {
	statusStr := "启用"
	if r.Status.Valid && r.Status.Int64 != 1 {
		statusStr = "禁用"
	}
	return []string{
		strconv.FormatUint(r.ID, 10),
		statusStr,
		r.Name.String,
		r.Domain.String,
		r.Logo.String,
		r.URL.String,
		r.BestDiscount.String,
		nullInt64Str(r.CouponCodes),
		nullInt64Str(r.TotalOffers),
		r.AvgSavings.String,
		nullInt64Str(r.FirstCategoryID),
		r.FirstCategoryName.String,
		nullInt64Str(r.SecondCategoryID),
		r.SecondCategoryName.String,
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

func nullTimeStr(v sql.NullTime) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format("2006-01-02 15:04:05")
}
