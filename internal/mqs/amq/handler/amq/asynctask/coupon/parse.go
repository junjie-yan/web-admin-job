// Package coupon 收敛 coupon（优惠券）业务域的行解析、商家域名解析与 SQL 访问，
// 供 asynctask 包的导入/导出 Handler 编排调用。
package coupon

import (
	"fmt"
	"strings"
	"time"
)

// Excel 模板列索引（列布局与 web-admin 生成的模板一致）
const (
	colDomain      = 0  // 商店域名*（定位键1，merchant_domain）
	colSourceID    = 1  // 来源ID*（定位键2，source_id）
	colOfferType   = 2  // 类型（coupon/promotion/deal/cashback/printable）
	colCode        = 3  // 优惠Code
	colTitle       = 4  // 标题
	colDescription = 5  // 描述
	colDiscount    = 6  // 折扣文本（如 50% OFF）
	colExpired     = 7  // 过期时间（2026-12-31 或 2026-12-31 23:59:59）
	colLastVerify  = 8  // 最后验证时间
	colVerified    = 9  // 人工验证（是/否）
	colExclusive   = 10 // 独家（是/否）
	colOngoing     = 11 // 持续有效（是/否）
	colStatus      = 12 // 状态（启用/禁用）
)

// Row 一行 coupon 数据（与 Excel 字段一一对应）
// 指针字段：nil 表示该列未填写（更新已存在记录时跳过，新增时使用默认值）
type Row struct {
	RowNo          int     // Excel 行号（从 2 开始，便于错误定位）
	MerchantDomain string  // 定位键1（必填）
	SourceID       string  // 定位键2（必填）
	MerchantID     *uint64 // 由商店域名解析 coupon_merchant_base.id，解析不到保持 nil
	OfferType      *string
	Code           *string
	Title          *string
	Description    *string
	DiscountText   *string
	ExpiredDate    *string // 日期字符串（parse 时已校验格式，由 MySQL 转为 datetime）
	LastVerifiedAt *string
	IsVerified     *bool
	IsExclusive    *bool
	IsOngoing      *bool
	Status         *uint8 // 1 启用 / 0 禁用
}

// Key 定位键（merchant_domain + source_id，库层 UNIQUE(coupon_merchant_domain_source_id)）
type Key struct {
	MerchantDomain string
	SourceID       string
}

// ParseRow 解析单行 Excel 数据为 Row，rowNo 为 Excel 行号（从 2 开始）
//
// 定位键：商店域名 + 来源ID（两项均必填，库层联合唯一）；
// 命中已存在记录则更新填写字段，未命中则新增（upsert 语义由调用方 Handler 编排）
// 返回 nil, nil 表示空行
func ParseRow(rowNo int, row []string, nr *Resolver) (*Row, error) {
	domain := strings.TrimSpace(cell(row, colDomain))
	sourceID := strings.TrimSpace(cell(row, colSourceID))

	// 空行判断
	restEmpty := true
	for _, c := range []int{colSourceID, colOfferType, colCode, colTitle, colDescription,
		colDiscount, colExpired, colLastVerify, colVerified, colExclusive, colOngoing, colStatus} {
		if strings.TrimSpace(cell(row, c)) != "" {
			restEmpty = false
			break
		}
	}
	if domain == "" && restEmpty {
		return nil, nil // 空行
	}
	if domain == "" {
		return nil, rowErr(rowNo, "商店域名 必填（定位键）")
	}
	if sourceID == "" {
		return nil, rowErr(rowNo, "来源ID 必填（定位键）")
	}

	r := &Row{RowNo: rowNo, MerchantDomain: domain, SourceID: sourceID}

	// 字符串列（可选）：非空才填写
	strCols := []struct {
		col int
		dst **string
	}{
		{colOfferType, &r.OfferType},
		{colCode, &r.Code},
		{colTitle, &r.Title},
		{colDescription, &r.Description},
		{colDiscount, &r.DiscountText},
	}
	for _, c := range strCols {
		if v := strings.TrimSpace(cell(row, c.col)); v != "" {
			*c.dst = &v
		}
	}

	// 日期列（可选）：校验格式，非法按行级错误处理
	if v := strings.TrimSpace(cell(row, colExpired)); v != "" {
		if e := validateDateTime(v); e != nil {
			return nil, rowErr(rowNo, "过期时间格式非法（应为 2026-12-31 或 2026-12-31 23:59:59）: "+v)
		}
		r.ExpiredDate = &v
	}
	if v := strings.TrimSpace(cell(row, colLastVerify)); v != "" {
		if e := validateDateTime(v); e != nil {
			return nil, rowErr(rowNo, "最后验证时间格式非法: "+v)
		}
		r.LastVerifiedAt = &v
	}

	// 布尔列（可选）：是/否
	boolCols := []struct {
		col int
		dst **bool
	}{
		{colVerified, &r.IsVerified},
		{colExclusive, &r.IsExclusive},
		{colOngoing, &r.IsOngoing},
	}
	for _, c := range boolCols {
		v := strings.TrimSpace(cell(row, c.col))
		if v == "" {
			continue
		}
		b, e := parseBool(v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		*c.dst = &b
	}

	// 状态（可选）：默认启用
	if v := strings.TrimSpace(cell(row, colStatus)); v != "" {
		st := parseStatus(v)
		r.Status = &st
	}
	return r, nil
}

// validateDateTime 校验日期时间字符串格式（支持日期或日期+时间两种）
func validateDateTime(s string) error {
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		if _, err := time.Parse(layout, s); err == nil {
			return nil
		}
	}
	return fmt.Errorf("invalid datetime")
}

// parseBool 解析布尔字符串（是/否），无法识别时报错
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "是", "true", "1", "yes", "y":
		return true, nil
	case "否", "false", "0", "no", "n":
		return false, nil
	default:
		return false, fmt.Errorf("布尔值无法识别（应填 是/否）: %s", s)
	}
}

// parseStatus 将状态字符串解析为 uint8（1 启用 / 0 禁用，与项目前端约定一致），无法识别时默认启用
func parseStatus(s string) uint8 {
	switch s {
	case "禁用", "0", "2", "disable", "false", "ban":
		return 0
	default:
		return 1
	}
}

// cell 安全取 row 指定列字符串，越界返回空串
func cell(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return row[col]
}

// rowErr 包装行级错误，附 Excel 行号便于定位
func rowErr(rowNo int, msg string) error {
	return fmt.Errorf("第 %d 行: %s", rowNo, msg)
}
