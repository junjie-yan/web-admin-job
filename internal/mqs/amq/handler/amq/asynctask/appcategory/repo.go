package appcategory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/junjie-yan/web-admin-job/pkg/slugify"
)

// Repo app_category 表 SQL 访问（导入/更新用）
type Repo struct {
	DB *sql.DB
}

// FindByName 同站点同层级下按名称查分类（判重/父分类解析用）
// 返回 nil 表示不存在
func (r *Repo) FindByName(ctx context.Context, siteID uint64, level int, name string) (*struct {
	ID  uint64
	Pid uint64
}, error,
) {
	var id, pid uint64
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, pid FROM app_category WHERE site_id = ? AND level = ? AND name = ? LIMIT 1`,
		siteID, level, name).Scan(&id, &pid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &struct {
		ID  uint64
		Pid uint64
	}{ID: id, Pid: pid}, nil
}

// GetByID 按主键查分类（update 存在性/归属校验）
func (r *Repo) GetByID(ctx context.Context, id uint64) (*struct {
	SiteID uint64
	Level  int
	Name   string
}, error,
) {
	var siteID uint64
	var level uint8
	var name string
	err := r.DB.QueryRowContext(ctx,
		`SELECT site_id, level, name FROM app_category WHERE id = ?`, id).
		Scan(&siteID, &level, &name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &struct {
		SiteID uint64
		Level  int
		Name   string
	}{SiteID: siteID, Level: int(level), Name: name}, nil
}

// Insert 插入分类（新增）
// URL 传 nil 落空串；分类页面 URL 为空时按名称生成 slug（与 C 端路由约定一致）
func (r *Repo) Insert(ctx context.Context, row *Row) error {
	url := ""
	if row.URL != nil {
		url = *row.URL
	} else if slug := slugify.Path("category", row.Name); slug != "" {
		url = slug
	}
	status := uint8(1)
	if row.Status != nil {
		status = *row.Status
	}
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO app_category (site_id, pid, level, name, icon_class, url, status) VALUES (?,?,?,?,?,?,?)`,
		row.SiteID, row.Pid, row.Level, row.Name, "", url, status)
	if err != nil {
		return err
	}
	return nil
}

// UpdateByID 按 ID 更新分类（仅更新非 nil 字段）
func (r *Repo) UpdateByID(ctx context.Context, row *Row) error {
	var sets []string
	var args []any
	if row.Name != "" {
		sets = append(sets, "name = ?")
		args = append(args, row.Name)
	}
	if row.ParentRaw != "" {
		sets = append(sets, "pid = ?")
		args = append(args, row.Pid)
	}
	if row.URL != nil {
		sets = append(sets, "url = ?")
		args = append(args, *row.URL)
	}
	if row.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *row.Status)
	}
	if len(sets) == 0 {
		return nil
	}
	q := fmt.Sprintf(`UPDATE app_category SET %s WHERE id = ?`, strings.Join(sets, ", "))
	args = append(args, row.ID)
	_, err := r.DB.ExecContext(ctx, q, args...)
	return err
}
