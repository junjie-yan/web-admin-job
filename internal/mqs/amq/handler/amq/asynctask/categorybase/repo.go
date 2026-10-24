package categorybase

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Repo coupon_category_base 表 SQL 访问（分类新增/更新用）
type Repo struct {
	DB *sql.DB
}

// GetByID 按主键查分类（update 存在性/层级校验）
// 返回 nil 表示不存在
func (r *Repo) GetByID(ctx context.Context, id uint64) (*struct {
	Level int
	Pid   uint64
	Name  string
}, error,
) {
	var level uint8
	var pid uint64
	var name string
	err := r.DB.QueryRowContext(ctx,
		`SELECT level, pid, name FROM coupon_category_base WHERE id = ?`, id).
		Scan(&level, &pid, &name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &struct {
		Level int
		Pid   uint64
		Name  string
	}{Level: int(level), Pid: pid, Name: name}, nil
}

// FindByName 同层级下按名称查分类（判重/父分类解析用）
// 返回 nil 表示不存在
func (r *Repo) FindByName(ctx context.Context, level int, name string) (*struct {
	ID uint64
}, error,
) {
	var id uint64
	err := r.DB.QueryRowContext(ctx,
		`SELECT id FROM coupon_category_base WHERE level = ? AND name = ? LIMIT 1`,
		level, name).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &struct{ ID uint64 }{ID: id}, nil
}

// Insert 插入分类（新增；url 列 NOT NULL + UNIQUE(uk_cc_url)，调用方须保证非空）
func (r *Repo) Insert(ctx context.Context, row *Row) error {
	icon, url := "", ""
	if row.IconURL != nil {
		icon = *row.IconURL
	}
	if row.URL != nil {
		url = *row.URL
	}
	status := uint8(1)
	if row.Status != nil {
		status = *row.Status
	}
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO coupon_category_base (pid, level, name, icon_url, url, status) VALUES (?,?,?,?,?,?)`,
		row.Pid, row.Level, row.Name, icon, url, status)
	return err
}

// UpdateByID 按 ID 更新分类（仅更新非 nil/非空字段；层级不可改）
func (r *Repo) UpdateByID(ctx context.Context, row *Row) error {
	var sets []string
	var args []any
	if row.Name != "" {
		sets = append(sets, "name = ?")
		args = append(args, row.Name)
	}
	if row.ParentRaw != "" {
		pid := row.Pid
		sets = append(sets, "pid = ?")
		args = append(args, pid)
	}
	if row.IconURL != nil {
		sets = append(sets, "icon_url = ?")
		args = append(args, *row.IconURL)
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
	q := fmt.Sprintf(`UPDATE coupon_category_base SET %s WHERE id = ?`, strings.Join(sets, ", "))
	args = append(args, row.ID)
	_, err := r.DB.ExecContext(ctx, q, args...)
	return err
}
