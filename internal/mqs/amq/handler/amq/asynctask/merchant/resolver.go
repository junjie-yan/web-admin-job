package merchant

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// Resolver 分类名称→ID 转换器（coupon_category_base，导入商家时解析一级/二级分类）
// 兼容规则：纯数字字符串按 ID 直接使用；分类按 level+名称查询
// 任务内缓存查询结果，避免重复查询
type Resolver struct {
	ctx    context.Context
	db     *sql.DB
	catIDs map[string]uint64 // key: level|分类名称 -> ID
}

// NewResolver 创建名称解析器（ctx 用于后续查库）
func NewResolver(ctx context.Context, db *sql.DB) *Resolver {
	return &Resolver{
		ctx:    ctx,
		db:     db,
		catIDs: make(map[string]uint64),
	}
}

// ResolveCategory 分类名称（或纯数字 ID）→ coupon_category_base.id
// level: 1 一级分类 / 2 二级分类
func (nr *Resolver) ResolveCategory(level int, nameOrID string) (uint64, error) {
	if id, e := strconv.ParseUint(nameOrID, 10, 64); e == nil {
		return id, nil // 纯数字按 ID 兼容处理
	}
	key := fmt.Sprintf("%d|%s", level, nameOrID)
	if id, ok := nr.catIDs[key]; ok {
		return id, nil
	}
	var id uint64
	err := nr.db.QueryRowContext(nr.ctx,
		"SELECT id FROM coupon_category_base WHERE level = ? AND name = ? ORDER BY id LIMIT 1",
		level, nameOrID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("不存在该%s: %s（可先在「优惠券分类基础」中维护）", categoryLevelName(level), nameOrID)
	}
	if err != nil {
		return 0, fmt.Errorf("查询%s失败: %w", categoryLevelName(level), err)
	}
	nr.catIDs[key] = id
	return id, nil
}

func categoryLevelName(level int) string {
	if level == 2 {
		return "二级分类"
	}
	return "一级分类"
}
