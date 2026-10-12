package asynctask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/coupon"
)

// couponExportProvider 优惠券导出数据源（biz_module=coupon + type=export）
type couponExportProvider struct{}

// SheetName Excel 工作表名
func (couponExportProvider) SheetName() string { return "优惠券" }

// Headers 导出表头
func (couponExportProvider) Headers() []string { return coupon.ExportHeaders() }

// ExportFileName 导出文件名
func (couponExportProvider) ExportFileName(taskID uint64) string {
	return fmt.Sprintf("coupon_export_task_%d.xlsx", taskID)
}

// Count 统计导出总数（filter 解析失败时降级为导出全部）
func (p couponExportProvider) Count(ctx context.Context, db *sql.DB, rawFilter string) (uint64, error) {
	return coupon.Count(ctx, db, p.parseFilter(rawFilter))
}

// ListPage 分页查询并转为字符串行
func (p couponExportProvider) ListPage(ctx context.Context, db *sql.DB, rawFilter string, limit, offset int) ([][]string, error) {
	rows, err := coupon.ListForExport(ctx, db, p.parseFilter(rawFilter), limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([][]string, 0, len(rows))
	for _, r := range rows {
		result = append(result, coupon.ExportRowStrings(r))
	}
	return result, nil
}

// parseFilter 解析过滤参数 JSON（容错：解析失败时返回零值，导出全部）
func (couponExportProvider) parseFilter(rawFilter string) (f coupon.ExportFilter) {
	if rawFilter != "" {
		_ = json.Unmarshal([]byte(rawFilter), &f)
	}
	return f
}

// 编译期断言：保证 provider 实现 exportProvider 接口
var _ exportProvider = couponExportProvider{}
