package asynctask

import (
	"context"
	"fmt"
	"strconv"

	"github.com/junjie-yan/web-admin-job/internal/helper"
)

// excelContentType xlsx 文件的 MIME 类型
const excelContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// maxErrorDetails 错误明细文件最多保留的条数
const maxErrorDetails = 200

// uploadErrorDetails 将错误明细写入 Excel 并上传 R2，返回结果 URL
// 超过 maxErrorDetails 条时截断；导入等含失败条目的任务共用
func (h *baseHandler) uploadErrorDetails(ctx context.Context, taskID uint64, taskType string, errs []string) (string, error) {
	if len(errs) == 0 {
		return "", nil
	}
	if len(errs) > maxErrorDetails {
		errs = errs[:maxErrorDetails]
	}
	w := helper.NewExcelWriter("错误明细", []string{"序号", "错误描述"})
	defer func() { _ = w.Close() }()
	for i, e := range errs {
		_ = w.WriteRow([]string{strconv.Itoa(i + 1), e})
	}
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("task_%d_%s_errors.xlsx", taskID, taskType)
	return h.uploadResult(ctx, taskID, "async_"+taskType+"_error", fileName, data, excelContentType)
}

// importResultRow 导入结果文件的行（成功与失败行都记录）
type importResultRow struct {
	RowNo      int    // Excel 行号
	Identifier string // 行标识（应用名称/商店链接/ID 等）
	OK         bool   // 是否处理成功
	Message    string // 处理结果说明
}

// uploadImportResult 将全量导入结果写入 Excel 并上传 R2（成功与失败行都写入，不截断）
func (h *baseHandler) uploadImportResult(ctx context.Context, taskID uint64, rows []importResultRow) (string, error) {
	w := helper.NewExcelWriter("导入结果", []string{"Excel行号", "标识", "处理结果", "说明"})
	defer func() { _ = w.Close() }()
	for _, r := range rows {
		result := "失败"
		if r.OK {
			result = "成功"
		}
		_ = w.WriteRow([]string{strconv.Itoa(r.RowNo), r.Identifier, result, r.Message})
	}
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("task_%d_import_result.xlsx", taskID)
	return h.uploadResult(ctx, taskID, "async_import_result", fileName, data, excelContentType)
}
