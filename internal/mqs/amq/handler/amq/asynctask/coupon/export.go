package coupon

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	MerchantDomain string `json:"merchantDomain"` // 商店域名精确匹配，空=全部
	OfferType      string `json:"offerType"`      // 类型精确匹配，空=全部
	Title          string `json:"title"`          // 标题模糊匹配，空=全部
	Status         *uint8 `json:"status"`         // nil=全部，1 启用 / 0 禁用
	IsVerified     *bool  `json:"isVerified"`     // 人工验证筛选，nil=全部
	IsExclusive    *bool  `json:"isExclusive"`    // 独家筛选，nil=全部
	IsOngoing      *bool  `json:"isOngoing"`      // 持续有效筛选，nil=全部
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
// 线上数据存在 NULL 值，统一使用 sql.Null* 承接，避免 Scan 报错
type ExportRow struct {
	ID             uint64
	Status         sql.NullInt64
	MerchantDomain sql.NullString
	SourceID       sql.NullString
	OfferType      sql.NullString
	Code           sql.NullString
	Title          sql.NullString
	DiscountText   sql.NullString
	Description    sql.NullString
	ExpiredDate    sql.NullTime
	LastVerifiedAt sql.NullTime
	IsVerified     sql.NullBool
	IsExclusive    sql.NullBool
	IsOngoing      sql.NullBool
	CreatedAt      sql.NullTime
	UpdatedAt      sql.NullTime
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.MerchantDomain != "" {
		conds = append(conds, "a.merchant_domain = ?")
		args = append(args, f.MerchantDomain)
	}
	if f.OfferType != "" {
		conds = append(conds, "a.offer_type = ?")
		args = append(args, f.OfferType)
	}
	if f.Title != "" {
		conds = append(conds, "a.title LIKE ?")
		args = append(args, "%"+f.Title+"%")
	}
	if f.Status != nil {
		conds = append(conds, "a.status = ?")
		args = append(args, *f.Status)
	}
	if f.IsVerified != nil {
		conds = append(conds, "a.is_verified = ?")
		args = append(args, *f.IsVerified)
	}
	if f.IsExclusive != nil {
		conds = append(conds, "a.is_exclusive = ?")
		args = append(args, *f.IsExclusive)
	}
	if f.IsOngoing != nil {
		conds = append(conds, "a.is_ongoing = ?")
		args = append(args, *f.IsOngoing)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Count 统计导出总数
func Count(ctx context.Context, db *sql.DB, f ExportFilter) (uint64, error) {
	where, args := exportWhere(f)
	q := "SELECT COUNT(*) FROM coupon a" + where
	var n uint64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 coupon；返回的切片顺序与 SQL 一致
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT a.id, a.status, a.merchant_domain, a.source_id, a.offer_type,
a.code, a.title, a.discount_text, a.description,
a.expired_date, a.last_verified_at, a.is_verified, a.is_exclusive, a.is_ongoing,
a.created_at, a.updated_at
FROM coupon a` + where +
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
			&r.ID, &r.Status, &r.MerchantDomain, &r.SourceID, &r.OfferType,
			&r.Code, &r.Title, &r.DiscountText, &r.Description,
			&r.ExpiredDate, &r.LastVerifiedAt, &r.IsVerified, &r.IsExclusive, &r.IsOngoing,
			&r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ExportHeaders 导出 Excel 的表头（顺序与 ExportRowStrings 一致）
// 前 13 列与导入模板列序一致（导出文件可直接修改后再次导入），首列为 ID、尾列为时间
func ExportHeaders() []string {
	return []string{
		"ID",
		"商店域名", "来源ID", "类型", "优惠Code", "标题", "描述", "折扣文本",
		"过期时间", "最后验证时间", "人工验证", "独家", "持续有效", "状态",
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
		r.MerchantDomain.String,
		r.SourceID.String,
		r.OfferType.String,
		r.Code.String,
		r.Title.String,
		r.Description.String,
		r.DiscountText.String,
		nullTimeStr(r.ExpiredDate),
		nullTimeStr(r.LastVerifiedAt),
		nullBoolStr(r.IsVerified),
		nullBoolStr(r.IsExclusive),
		nullBoolStr(r.IsOngoing),
		statusStr,
		nullTimeStr(r.CreatedAt),
		nullTimeStr(r.UpdatedAt),
	}
}

func nullTimeStr(v sql.NullTime) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format("2006-01-02 15:04:05")
}

func nullBoolStr(v sql.NullBool) string {
	if !v.Valid {
		return ""
	}
	if v.Bool {
		return "是"
	}
	return "否"
}
