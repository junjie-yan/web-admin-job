package asynctask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/categorybase"
)

// categoryBaseExportProvider 优惠券分类基础导出数据源（biz_module=coupon_category_base + type=export）
type categoryBaseExportProvider struct{}

// SheetName Excel 工作表名
func (categoryBaseExportProvider) SheetName() string { return "优惠券分类" }

// Headers 导出表头
func (categoryBaseExportProvider) Headers() []string { return categorybase.ExportHeaders() }

// ExportFileName 导出文件名
func (categoryBaseExportProvider) ExportFileName(taskID uint64) string {
	return fmt.Sprintf("coupon_category_base_export_task_%d.xlsx", taskID)
}

// Count 统计导出总数（filter 解析失败时降级为导出全部）
func (p categoryBaseExportProvider) Count(ctx context.Context, db *sql.DB, rawFilter string) (uint64, error) {
	return categorybase.Count(ctx, db, p.parseFilter(rawFilter))
}

// ListPage 分页查询并转为字符串行
func (p categoryBaseExportProvider) ListPage(ctx context.Context, db *sql.DB, rawFilter string, limit, offset int) ([][]string, error) {
	rows, err := categorybase.ListForExport(ctx, db, p.parseFilter(rawFilter), limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([][]string, 0, len(rows))
	for _, r := range rows {
		result = append(result, categorybase.ExportRowStrings(r))
	}
	return result, nil
}

// parseFilter 解析过滤参数 JSON（容错：解析失败时返回零值，导出全部）
func (categoryBaseExportProvider) parseFilter(rawFilter string) (f categorybase.ExportFilter) {
	if rawFilter != "" {
		_ = json.Unmarshal([]byte(rawFilter), &f)
	}
	return f
}

// 编译期断言：保证 provider 实现 exportProvider 接口
var _ exportProvider = categoryBaseExportProvider{}
