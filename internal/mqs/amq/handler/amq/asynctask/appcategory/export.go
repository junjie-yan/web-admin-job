// 收敛 app_category（APP 分类表）的导出 SQL 访问与行转换，
// 供 asynctask 包的导出 Handler 编排调用。
package appcategory

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	SiteID uint64 `json:"siteId"` // 0=全部
	Name   string `json:"name"`   // 分类名称模糊匹配，空=全部
	Level  uint8  `json:"level"`  // 0=全部，1 一级 / 2 二级
	Status *uint8 `json:"status"` // nil=全部，1 启用 / 0 禁用
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
type ExportRow struct {
	ID        uint64
	SiteName  sql.NullString
	Name      sql.NullString
	ParentName sql.NullString
	Level     sql.NullInt64
	IconClass sql.NullString
	URL       sql.NullString
	AppCount  sql.NullInt64
	Status    sql.NullInt64
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.SiteID > 0 {
		conds = append(conds, "a.site_id = ?")
		args = append(args, f.SiteID)
	}
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
	var n uint64
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM app_category a"+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 app_category；关联站点名称与父分类名称
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT a.id, s.name, a.name, p.name, a.level, a.icon_class, a.url, a.app_count, a.status
FROM app_category a
LEFT JOIN site s ON s.id = a.site_id
LEFT JOIN app_category p ON p.id = a.pid` + where +
		" ORDER BY a.site_id, a.level, a.id LIMIT ? OFFSET ?"
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
			&r.ID, &r.SiteName, &r.Name, &r.ParentName, &r.Level, &r.IconClass, &r.URL,
			&r.AppCount, &r.Status,
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
		"ID", "站点名称", "分类名称", "父分类", "层级", "图标类名", "URL", "应用数量", "状态",
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
		r.SiteName.String,
		r.Name.String,
		r.ParentName.String,
		levelStr,
		r.IconClass.String,
		r.URL.String,
		nullInt64Str(r.AppCount),
		statusStr,
	}
}

func nullInt64Str(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}
