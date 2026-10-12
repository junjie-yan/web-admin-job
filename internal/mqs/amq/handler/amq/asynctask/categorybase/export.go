// Package categorybase 收敛 coupon_category_base（优惠券分类基础表）业务域的
// SQL 访问与导出行转换，供 asynctask 包的导出 Handler 编排调用。
package categorybase

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	Name   string `json:"name"`   // 分类名称模糊匹配，空=全部
	Level  uint8  `json:"level"`  // 0=全部，1 一级 / 2 二级
	Status *uint8 `json:"status"` // nil=全部，1 启用 / 0 禁用
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
type ExportRow struct {
	ID         uint64
	Status     sql.NullInt64
	Pid        sql.NullInt64
	ParentName sql.NullString
	Level      sql.NullInt64
	Name       sql.NullString
	IconURL    sql.NullString
	URL        sql.NullString
	CreatedAt  sql.NullTime
	UpdatedAt  sql.NullTime
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Name != "" {
		conds = append(conds, "a.name LIKE ?")
		args = append(args, "%"+f.Name+"%")
	}
	if f.Level > 0 {
		conds = append(conds, "a.level = ?")
		args = append(args, f.Level)
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
	q := "SELECT COUNT(*) FROM coupon_category_base a" + where
	var n uint64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 coupon_category_base；自关联取父分类名称
// 返回的切片顺序与 SQL 一致
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT a.id, a.status, a.pid, p.name, a.level, a.name, a.icon_url, a.url, a.created_at, a.updated_at
FROM coupon_category_base a
LEFT JOIN coupon_category_base p ON p.id = a.pid` + where +
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
			&r.ID, &r.Status, &r.Pid, &r.ParentName, &r.Level, &r.Name, &r.IconURL, &r.URL,
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
		"ID", "状态", "父分类ID", "父分类名称", "层级", "名称", "图标URL", "来源URL",
		"创建时间", "更新时间",
	}
}

// ExportRowStrings 将一行转为字符串切片（顺序与 ExportHeaders 一致），NULL 输出空串
func ExportRowStrings(r *ExportRow) []string {
	statusStr := "启用"
	if r.Status.Valid && r.Status.Int64 != 1 {
		statusStr = "禁用"
	}
	levelStr := "一级"
	if r.Level.Valid && r.Level.Int64 == 2 {
		levelStr = "二级"
	}
	return []string{
		strconv.FormatUint(r.ID, 10),
		statusStr,
		nullInt64Str(r.Pid),
		r.ParentName.String,
		levelStr,
		r.Name.String,
		r.IconURL.String,
		r.URL.String,
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
