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
// 超过 maxErrorDetails 条时截断；导入/批量更新等含失败条目的任务共用
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
