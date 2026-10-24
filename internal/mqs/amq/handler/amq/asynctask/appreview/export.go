// Package appreview 收敛 app_review（APP 评论表）的导出 SQL 访问与行转换，
// 供 asynctask 包的导出 Handler 编排调用。
package appreview

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ExportFilter 导出过滤参数（与前端列表搜索条件对应）
// 生产端（web-admin API）约定：JSON 序列化后存于 async_task 表的 file_name 字段
type ExportFilter struct {
	SiteID   uint64 `json:"siteId"`   // 0=全部
	AppID    uint64 `json:"appId"`    // 0=全部
	UserName string `json:"userName"` // 用户名模糊匹配，空=全部
	Rating   uint8  `json:"rating"`   // 0=全部，1-5
	Status   *uint8 `json:"status"`   // nil=全部，1 启用 / 0 禁用
}

// ExportRow 导出用的扁平行结构（用于写入 Excel）
type ExportRow struct {
	ID         uint64
	AppName    sql.NullString
	UserName   sql.NullString
	Rating     sql.NullInt64
	Content    sql.NullString
	ReviewDate sql.NullString
	Status     sql.NullInt64
}

// exportWhere 按 filter 构造 WHERE 子句与绑定参数
func exportWhere(f ExportFilter) (string, []any) {
	var conds []string
	var args []any
	if f.SiteID > 0 {
		conds = append(conds, "r.site_id = ?")
		args = append(args, f.SiteID)
	}
	if f.AppID > 0 {
		conds = append(conds, "r.app_id = ?")
		args = append(args, f.AppID)
	}
	if f.UserName != "" {
		conds = append(conds, "r.user_name LIKE ?")
		args = append(args, "%"+f.UserName+"%")
	}
	if f.Rating > 0 {
		conds = append(conds, "r.rating = ?")
		args = append(args, f.Rating)
	}
	if f.Status != nil {
		conds = append(conds, "r.status = ?")
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
		"SELECT COUNT(*) FROM app_review r"+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListForExport 分页查询 app_review；关联 APP 名称
func ListForExport(ctx context.Context, db *sql.DB, f ExportFilter, limit, offset int) ([]*ExportRow, error) {
	where, args := exportWhere(f)
	q := `SELECT r.id, d.name, r.user_name, r.rating, r.content, r.review_date, r.status
FROM app_review r
LEFT JOIN app_detail d ON d.id = r.app_id` + where +
		" ORDER BY r.site_id, r.id LIMIT ? OFFSET ?"
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
			&r.ID, &r.AppName, &r.UserName, &r.Rating, &r.Content, &r.ReviewDate, &r.Status,
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
		"ID", "应用名称", "用户名", "评分", "评论内容", "评论日期", "状态",
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
		r.AppName.String,
		r.UserName.String,
		nullInt64Str(r.Rating),
		r.Content.String,
		r.ReviewDate.String,
		statusStr,
	}
}

func nullInt64Str(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}
