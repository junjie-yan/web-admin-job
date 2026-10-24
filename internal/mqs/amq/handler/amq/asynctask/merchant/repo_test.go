package merchant

import (
	"strings"
	"testing"
)

// TestBatchInsertSQLBalance 锁定插入语句的列数与值位数一致（历史事故同 coupon 包：
// 列清单漏写 created_at/updated_at 导致 13 值位 ≠ 11 列、整批恒报 Error 1136）
func TestBatchInsertSQLBalance(t *testing.T) {
	cols := strings.Count(merchantInsertCols, ",") + 1
	valueItems := strings.Count(merchantInsertTuple, ",") + 1
	if cols != valueItems {
		t.Fatalf("插入列数 %d ≠ 值位数 %d（merchantInsertCols 与 merchantInsertTuple 不同步）", cols, valueItems)
	}
	if qs := strings.Count(merchantInsertTuple, "?"); qs != cols-2 {
		t.Fatalf("? 数 %d 应为列数 %d - 2（created_at/updated_at 用 NOW()）", qs, cols)
	}
}
