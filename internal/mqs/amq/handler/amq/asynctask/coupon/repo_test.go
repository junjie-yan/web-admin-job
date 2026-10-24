package coupon

import (
	"strings"
	"testing"
)

// TestBatchInsertSQLBalance 锁定插入语句的列数与值位数一致：
// VALUES 元组除 NOW() 外每个 ? 对应一列，历史事故为列清单漏写
// created_at/updated_at 导致 16 值位 ≠ 14 列、整批恒报 Error 1136
func TestBatchInsertSQLBalance(t *testing.T) {
	cols := strings.Count(couponInsertCols, ",") + 1
	valueItems := strings.Count(couponInsertTuple, ",") + 1
	if cols != valueItems {
		t.Fatalf("插入列数 %d ≠ 值位数 %d（couponInsertCols 与 couponInsertTuple 不同步）", cols, valueItems)
	}
	if now := strings.Count(couponInsertTuple, "NOW()"); now != 2 {
		t.Fatalf("created_at/updated_at 应由 NOW() 生成，实际 NOW() 出现 %d 次", now)
	}
	if qs := strings.Count(couponInsertTuple, "?"); qs != cols-2 {
		t.Fatalf("? 数 %d 应为列数 %d - 2（created_at/updated_at 用 NOW()）", qs, cols)
	}
}
