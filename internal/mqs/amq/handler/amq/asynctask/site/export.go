// Package site 收敛 site（站点表）的导出 SQL 访问与行转换，
// 供 asynctask 包的导出 Handler 编排调用。
package site

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	Name   string `json:"name"`   // 站点名称模糊匹配，空=全部
	Domain string `json:"domain"` // 域名模糊匹配，空=全部
	Type   string `json:"type"`   // 站点类型 app/coupon，空=全部
	Status *uint8 `json:"status"` // nil=全部，1 启用 / 0 禁用
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
type ExportRow struct {
	ID         uint64
	Name       sql.NullString
	Domain     sql.NullString
	Logo       sql.NullString
	Icon       sql.NullString
	Country    sql.NullString
	Language   sql.NullString
	ThemeName  sql.NullString
	GaAccountID sql.NullString
	GtagID     sql.NullString
	Status     sql.NullInt64
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Name != "" {
		conds = append(conds, "s.name LIKE ?")
		args = append(args, "%"+f.Name+"%")
	}
	if f.Domain != "" {
		conds = append(conds, "s.domain LIKE ?")
		args = append(args, "%"+f.Domain+"%")
	}
	if f.Type != "" {
		conds = append(conds, "s.type = ?")
		args = append(args, f.Type)
	}
	if f.Status != nil {
		conds = append(conds, "s.status = ?")
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
	var n uint64
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM site s"+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 site；关联主题名称
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT s.id, s.name, s.domain, s.logo, s.icon, s.country, s.language, t.name, s.ga_account_id, s.gtag_id, s.status
FROM site s
LEFT JOIN theme t ON t.id = s.theme_id` + where +
		" ORDER BY s.id LIMIT ? OFFSET ?"
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
			&r.ID, &r.Name, &r.Domain, &r.Logo, &r.Icon, &r.Country, &r.Language,
			&r.ThemeName, &r.GaAccountID, &r.GtagID, &r.Status,
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
		"ID", "站点名称", "域名", "Logo", "Icon", "国家", "语言", "主题", "GA Account ID", "Gtag ID", "状态",
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
		r.Name.String,
		r.Domain.String,
		r.Logo.String,
		r.Icon.String,
		r.Country.String,
		r.Language.String,
		r.ThemeName.String,
		r.GaAccountID.String,
		r.GtagID.String,
		statusStr,
	}
}
