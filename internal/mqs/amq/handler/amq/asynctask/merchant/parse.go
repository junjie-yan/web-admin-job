// Package merchant 收敛 coupon_merchant_base（优惠券商家基础表）业务域的
// 行解析、分类名称解析与 SQL 访问，供 asynctask 包的导入/导出 Handler 编排调用。
package merchant

import (
	"fmt"
	"math"
	"strings"

	"github.com/junjie-yan/web-admin-job/internal/helper"
)

// Excel 模板列索引（列布局与 web-admin 生成的模板一致）
const (
	colDomain    = 0  // 商店域名*（唯一，定位键）
	colName      = 1  // 商店名称（新增时必填；更新时留空保持原值）
	colLogo      = 2  // Logo 地址
	colURL       = 3  // 商店主页 URL
	colBestDisc  = 4  // 最佳折扣
	colCodes     = 5  // 可用 Code 数
	colOffers    = 6  // 总 Offers 数
	colAvgSav    = 7  // 平均节省
	colFirstCat  = 8  // 一级分类名称或 ID（coupon_category_base level=1）
	colSecondCat = 9  // 二级分类名称或 ID（coupon_category_base level=2）
	colStatus    = 10 // 状态（启用/禁用）
)

// Row 一行 coupon_merchant_base 数据（与 Excel 字段一一对应）
// 指针字段：nil 表示该列未填写（更新已存在记录时跳过，新增时使用默认值）
type Row struct {
	RowNo            int     // Excel 行号（从 2 开始，便于错误定位）
	Domain           string  // 定位键（必填，库层 UNIQUE(domain)）
	Name             *string // 商店名称（新增时必填，由 Handler 校验）
	Logo             *string
	URL              *string
	BestDiscount     *string
	CouponCodes      *uint32
	TotalOffers      *uint32
	AvgSavings       *string
	FirstCategoryID  *uint64
	SecondCategoryID *uint64
	Status           *uint8 // 1 启用 / 0 禁用
}

// ParseRow 解析单行 Excel 数据为 Row，rowNo 为 Excel 行号（从 2 开始）
//
// 定位键：商店域名（必填，库层 UNIQUE(domain)）；
// 命中已存在记录则更新填写字段，未命中则新增（upsert 语义由调用方 Handler 编排）
// 分类填名称（经 Resolver 查库转 ID，coupon_category_base 按 level 区分），兼容纯数字 ID
// 返回 nil, nil 表示空行
func ParseRow(rowNo int, row []string, nr *Resolver) (*Row, error) {
	domain := strings.TrimSpace(cell(row, colDomain))
	name := strings.TrimSpace(cell(row, colName))

	// 空行判断：定位键与其余关键列全空
	restEmpty := true
	for _, c := range []int{colName, colLogo, colURL, colBestDisc, colCodes, colOffers, colAvgSav, colFirstCat, colSecondCat, colStatus} {
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

	r := &Row{RowNo: rowNo, Domain: domain}
	if name != "" {
		r.Name = &name
	}

	// 一级/二级分类（可选，名称转 ID）
	if v := strings.TrimSpace(cell(row, colFirstCat)); v != "" {
		id, e := nr.ResolveCategory(1, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.FirstCategoryID = &id
	}
	if v := strings.TrimSpace(cell(row, colSecondCat)); v != "" {
		id, e := nr.ResolveCategory(2, v)
		if e != nil {
			return nil, rowErr(rowNo, e.Error())
		}
		r.SecondCategoryID = &id
	}

	// 字符串列（可选）：非空才填写
	strCols := []struct {
		col int
		dst **string
	}{
		{colLogo, &r.Logo},
		{colURL, &r.URL},
		{colBestDisc, &r.BestDiscount},
		{colAvgSav, &r.AvgSavings},
	}
	for _, c := range strCols {
		if v := strings.TrimSpace(cell(row, c.col)); v != "" {
			*c.dst = &v
		}
	}

	// 数值列（可选）：解析失败按行级错误处理
	codes, err := parseUint32(cell(row, colCodes))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.CouponCodes = codes
	offers, err := parseUint32(cell(row, colOffers))
	if err != nil {
		return nil, rowErr(rowNo, err.Error())
	}
	r.TotalOffers = offers

	// 状态（可选）：默认启用
	if v := strings.TrimSpace(cell(row, colStatus)); v != "" {
		st := parseStatus(v)
		r.Status = &st
	}
	return r, nil
}

// parseUint32 解析字符串为 uint32 指针（空串返回 nil，超范围报错）
func parseUint32(s string) (*uint32, error) {
	v, err := helper.ParseUint64(s)
	if err != nil || v == nil {
		return nil, err
	}
	if *v > math.MaxUint32 {
		return nil, fmt.Errorf("数值超出范围: %d", *v)
	}
	u := uint32(*v)
	return &u, nil
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
