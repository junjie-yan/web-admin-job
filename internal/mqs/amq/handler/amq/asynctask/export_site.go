package asynctask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	siteexp "github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/site"
)

// siteExportProvider 站点列表导出数据源（biz_module=site + type=export）
type siteExportProvider struct{}

// SheetName Excel 工作表名
func (siteExportProvider) SheetName() string { return "站点列表" }

// Headers 导出表头
func (siteExportProvider) Headers() []string { return siteexp.ExportHeaders() }

// ExportFileName 导出文件名
func (siteExportProvider) ExportFileName(taskID uint64) string {
	return fmt.Sprintf("sites_export_task_%d.xlsx", taskID)
}

// Count 统计导出总数（filter 解析失败时降级为导出全部）
func (p siteExportProvider) Count(ctx context.Context, db *sql.DB, rawFilter string) (uint64, error) {
	return siteexp.Count(ctx, db, p.parseFilter(rawFilter))
}

// ListPage 分页查询并转为字符串行
func (p siteExportProvider) ListPage(ctx context.Context, db *sql.DB, rawFilter string, limit, offset int) ([][]string, error) {
	rows, err := siteexp.ListForExport(ctx, db, p.parseFilter(rawFilter), limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([][]string, 0, len(rows))
	for _, r := range rows {
		result = append(result, siteexp.ExportRowStrings(r))
	}
	return result, nil
}

// parseFilter 解析过滤参数 JSON（容错：解析失败时返回零值，导出全部）
func (siteExportProvider) parseFilter(rawFilter string) (f siteexp.ExportFilter) {
	if rawFilter != "" {
		_ = json.Unmarshal([]byte(rawFilter), &f)
	}
	return f
}

// 编译期断言：保证 provider 实现 exportProvider 接口
var _ exportProvider = siteExportProvider{}
