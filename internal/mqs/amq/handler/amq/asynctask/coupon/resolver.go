package coupon

import (
	"context"
	"database/sql"
)

// Resolver 商店域名→商家ID 转换器（coupon_merchant_base，导入优惠券时自动关联商家）
// 任务内缓存查询结果，避免重复查询
type Resolver struct {
	ctx        context.Context
	db         *sql.DB
	merchantID map[string]uint64 // 商店域名 -> coupon_merchant_base.id
	missed     map[string]bool   // 已确认不存在的域名缓存
}

// NewResolver 创建解析器（ctx 用于后续查库）
func NewResolver(ctx context.Context, db *sql.DB) *Resolver {
	return &Resolver{
		ctx:        ctx,
		db:         db,
		merchantID: make(map[string]uint64),
		missed:     make(map[string]bool),
	}
}

// ResolveMerchantID 商店域名 → coupon_merchant_base.id
// 域名不存在时返回 (0, nil)：coupon.merchant_id 可空，不阻断导入（仅不建立关联）
func (nr *Resolver) ResolveMerchantID(domain string) (uint64, error) {
	if id, ok := nr.merchantID[domain]; ok {
		return id, nil
	}
	if nr.missed[domain] {
		return 0, nil
	}
	var id uint64
	err := nr.db.QueryRowContext(nr.ctx,
		"SELECT id FROM coupon_merchant_base WHERE domain = ? ORDER BY id LIMIT 1", domain).Scan(&id)
	if err == sql.ErrNoRows {
		nr.missed[domain] = true
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	nr.merchantID[domain] = id
	return id, nil
}
