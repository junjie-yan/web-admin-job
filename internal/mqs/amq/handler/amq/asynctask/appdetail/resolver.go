package appdetail

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// Resolver 站点/分类名称→ID 转换器（导入/批量更新用）
// 兼容规则：纯数字字符串按 ID 直接使用；站点统一按域名查询，分类按名称查询
// 任务内缓存查询结果，避免重复查询
type Resolver struct {
	ctx     context.Context
	db      *sql.DB
	siteIDs map[string]uint64 // 站点域名 -> ID
	catIDs  map[string]uint64 // key: siteID|level|分类名称 -> ID
}

// NewResolver 创建名称解析器（ctx 用于后续查库）
func NewResolver(ctx context.Context, db *sql.DB) *Resolver {
	return &Resolver{
		ctx:     ctx,
		db:      db,
		siteIDs: make(map[string]uint64),
		catIDs:  make(map[string]uint64),
	}
}

// ResolveSite 站点域名（或纯数字 ID）→ site_id
func (nr *Resolver) ResolveSite(nameOrID string) (uint64, error) {
	if id, e := strconv.ParseUint(nameOrID, 10, 64); e == nil {
		return id, nil // 纯数字按 ID 兼容处理
	}
	if id, ok := nr.siteIDs[nameOrID]; ok {
		return id, nil
	}
	var id uint64
	err := nr.db.QueryRowContext(nr.ctx,
		"SELECT id FROM site WHERE domain = ? ORDER BY id LIMIT 1", nameOrID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("站点不存在: %s", nameOrID)
	}
	if err != nil {
		return 0, fmt.Errorf("查询站点失败: %w", err)
	}
	nr.siteIDs[nameOrID] = id
	return id, nil
}

// ResolveCategory 分类名称（或纯数字 ID）→ 分类 ID
// level: 1 一级分类 / 2 二级分类；siteID 为该行已解析出的站点 ID
func (nr *Resolver) ResolveCategory(siteID uint64, level int, nameOrID string) (uint64, error) {
	if id, e := strconv.ParseUint(nameOrID, 10, 64); e == nil {
		return id, nil // 纯数字按 ID 兼容处理
	}
	key := fmt.Sprintf("%d|%d|%s", siteID, level, nameOrID)
	if id, ok := nr.catIDs[key]; ok {
		return id, nil
	}
	var id uint64
	err := nr.db.QueryRowContext(nr.ctx,
		"SELECT id FROM app_category WHERE site_id = ? AND level = ? AND name = ? ORDER BY id LIMIT 1",
		siteID, level, nameOrID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("站点下不存在该%s: %s", categoryLevelName(level), nameOrID)
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
