// Package categorybase 收敛 coupon_category_base（优惠券分类字典，全局无站点维度）
// 的行解析与 SQL 访问，供分类新增/更新 Handler 编排调用。
//
// 列布局（与 web-admin 模板一致，importspec coupon_category_add/update 同源）：
//   - add:    0 分类名称* 1 层级*(1/2) 2 父分类名称或ID(二级必填) 3 图标URL 4 来源URL 5 状态
//   - update: 0 记录ID*   1 分类名称 2 父分类名称或ID(仅二级) 3 图标URL 4 来源URL 5 状态
package categorybase

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	colName   = 0 // 分类名称（add*）/ 记录 ID（update*）
	colLevel  = 1 // 层级（仅 add）
	colParent = 2 // 父分类名称或 ID
	colIcon   = 3 // 图标 URL
	colURL    = 4 // 来源 URL
	colStatus = 5 // 状态
)

// Row 一行分类数据
type Row struct {
	RowNo     int
	ID        uint64 // update 定位
	Name      string
	Level     int    // 1 一级 / 2 二级（update 不允许改层级）
	Pid       uint64 // 父分类 ID（二级；一级恒 0）
	ParentRaw string
	IconURL   *string
	URL       *string
	Status    *uint8
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

func rowErr(rowNo int, msg string) error {
	return fmt.Errorf("第 %d 行: %s", rowNo, msg)
}

// ParseLevel 解析层级列（1/2 或 一级/二级）
func ParseLevel(s string) (int, error) {
	switch strings.TrimSpace(s) {
	case "1", "一级":
		return 1, nil
	case "2", "二级":
		return 2, nil
	}
	return 0, fmt.Errorf("层级无效: %s（填 1/2 或 一级/二级）", s)
}

// ParseStatus 解析状态列（1/0/启用/禁用/兼容历史 2）
func ParseStatus(s string) uint8 {
	switch strings.TrimSpace(s) {
	case "禁用", "0", "2", "disable", "false", "ban":
		return 0
	default:
		return 1
	}
}

// ParseParent 解析父分类列（名称按 level=1 查库，纯数字按 ID；"0"/空串=无父级）
func ParseParent(s string, resolve func(nameOrID string) (uint64, error)) (uint64, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	if id, err := strconv.ParseUint(s, 10, 64); err == nil {
		return id, nil
	}
	return resolve(s)
}

// ParseAddRow 解析批量新增行
// 必填：分类名称、层级；层级=2 时父分类必填；来源 URL 可留空
func ParseAddRow(
	rowNo int, row []string,
	resolveParent func(nameOrID string) (uint64, error),
) (*Row, error) {
	name := strings.TrimSpace(cell(row, colName))
	levelStr := strings.TrimSpace(cell(row, colLevel))
	parentRaw := strings.TrimSpace(cell(row, colParent))
	iconStr := strings.TrimSpace(cell(row, colIcon))
	urlStr := strings.TrimSpace(cell(row, colURL))
	statusStr := strings.TrimSpace(cell(row, colStatus))

	if name == "" && levelStr == "" && parentRaw == "" && iconStr == "" && urlStr == "" && statusStr == "" {
		return nil, nil
	}
	if name == "" {
		return nil, rowErr(rowNo, "分类名称 必填")
	}
	// url 列 NOT NULL + UNIQUE(uk_cc_url)：必填且需唯一，空值/重复均无法落库
	if urlStr == "" {
		return nil, rowErr(rowNo, "来源URL 必填（不可与其它分类重复）")
	}
	level, err := ParseLevel(levelStr)
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}

	r := &Row{RowNo: rowNo, Name: name, Level: level}
	if level == 2 {
		if parentRaw == "" {
			return nil, rowErr(rowNo, "二级分类必须填写父分类")
		}
		pid, perr := ParseParent(parentRaw, resolveParent)
		if perr != nil {
			return nil, rowErr(rowNo, perr.Error())
		}
		r.Pid = pid
	}
	if iconStr != "" {
		r.IconURL = &iconStr
	}
	if urlStr != "" {
		r.URL = &urlStr
	}
	if statusStr != "" {
		st := ParseStatus(statusStr)
		r.Status = &st
	}
	return r, nil
}

// ParseUpdateRow 解析批量更新行（记录 ID 定位；至少填一项）
// 层级不允许修改；父分类仅二级可改；一级分类传父分类会被忽略
func ParseUpdateRow(
	rowNo int, row []string,
	resolveParent func(nameOrID string) (uint64, error),
) (*Row, error) {
	// update 模板列序：0 记录ID* 1 分类名称 2 父分类 3 图标URL 4 来源URL 5 状态
	idStr := strings.TrimSpace(cell(row, 0))
	name := strings.TrimSpace(cell(row, 1))
	parentRaw := strings.TrimSpace(cell(row, 2))
	iconStr := strings.TrimSpace(cell(row, 3))
	urlStr := strings.TrimSpace(cell(row, 4))
	statusStr := strings.TrimSpace(cell(row, 5))

	if idStr == "" && name == "" && parentRaw == "" && iconStr == "" && urlStr == "" && statusStr == "" {
		return nil, nil
	}
	if idStr == "" {
		return nil, rowErr(rowNo, "记录 ID 必填")
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		return nil, rowErr(rowNo, fmt.Sprintf("记录 ID 无效: %s", idStr))
	}

	r := &Row{RowNo: rowNo, ID: id, Name: name, ParentRaw: parentRaw}
	if parentRaw != "" {
		pid, perr := ParseParent(parentRaw, resolveParent)
		if perr != nil {
			return nil, rowErr(rowNo, perr.Error())
		}
		r.Pid = pid
	}
	if iconStr != "" {
		r.IconURL = &iconStr
	}
	if urlStr != "" {
		r.URL = &urlStr
	}
	if statusStr != "" {
		st := ParseStatus(statusStr)
		r.Status = &st
	}
	if r.Name == "" && r.ParentRaw == "" && r.IconURL == nil && r.URL == nil && r.Status == nil {
		return nil, rowErr(rowNo, "记录 ID 之外至少填写一项")
	}
	return r, nil
}
