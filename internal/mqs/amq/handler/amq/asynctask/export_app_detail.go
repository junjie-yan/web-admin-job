package asynctask

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask/appdetail"
)

// appDetailExportProvider APP 详情导出数据源（biz_module=app_detail + type=export）
// 过滤参数 JSON（appdetail.ExportFilter，如 {"siteId":1}）由生产端存放于 task.FileName
type appDetailExportProvider struct{}

// SheetName Excel 工作表名
func (appDetailExportProvider) SheetName() string { return "APP详情" }

// Headers 导出表头
func (appDetailExportProvider) Headers() []string { return appdetail.ExportHeaders() }

// ExportFileName 导出文件名
func (appDetailExportProvider) ExportFileName(taskID uint64) string {
	return fmt.Sprintf("app_detail_export_task_%d.xlsx", taskID)
}

// Count 统计导出总数（filter 解析失败时降级为导出全部）
func (p appDetailExportProvider) Count(ctx context.Context, db *sql.DB, rawFilter string) (uint64, error) {
	filter := p.parseFilter(rawFilter)
	return appdetail.Count(ctx, db, filter.SiteID)
}

// ListPage 分页查询并转为字符串行
func (p appDetailExportProvider) ListPage(ctx context.Context, db *sql.DB, rawFilter string, limit, offset int) ([][]string, error) {
	filter := p.parseFilter(rawFilter)
	rows, err := appdetail.ListForExport(ctx, db, filter.SiteID, limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([][]string, 0, len(rows))
	for _, r := range rows {
		result = append(result, appdetail.ExportRowStrings(r))
	}
	return result, nil
}

// parseFilter 解析过滤参数 JSON（容错：解析失败时返回零值，导出全部）
func (appDetailExportProvider) parseFilter(rawFilter string) appdetail.ExportFilter {
	var filter appdetail.ExportFilter
	if rawFilter != "" {
		_ = json.Unmarshal([]byte(rawFilter), &filter)
	}
	return filter
}

// 编译期断言：保证 provider 实现 exportProvider 接口
var _ exportProvider = appDetailExportProvider{}
