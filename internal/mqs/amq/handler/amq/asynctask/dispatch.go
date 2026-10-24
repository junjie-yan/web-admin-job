package asynctask

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"

	"github.com/junjie-yan/web-admin-job/internal/helper"
	"github.com/junjie-yan/web-admin-job/internal/svc"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// taskProcessor 单个业务模块的导入处理器：领取任务后由 ImportDispatcher 路由调用
// rows 为输入 Excel 的数据行（已跳过表头），实现方自行解析并执行业务写入与终态写入
type taskProcessor interface {
	process(ctx context.Context, task *asyncjob.Task, rows [][]string)
}

// importDispatcher 导入任务分发器（pattern=async_task:import）
// asynq 同一 pattern 只能注册一个 handler，多业务模块在此按 task.BizModule 分发
type importDispatcher struct {
	baseHandler
	handlers map[string]taskProcessor
}

// NewImportDispatcher 创建导入分发器，注册各业务模块的处理器
func NewImportDispatcher(svcCtx *svc.ServiceContext) *importDispatcher {
	return &importDispatcher{
		baseHandler: baseHandler{svcCtx: svcCtx},
		handlers: map[string]taskProcessor{
			asyncjob.BizAppDetailAdd:         newImportAppDetailAdd(svcCtx),
			asyncjob.BizAppDetailUpdate:      newImportAppDetailUpdate(svcCtx),
			asyncjob.BizAppDetailUpsert:      newImportAppDetailUpsert(svcCtx),
			asyncjob.BizCoupon:               newImportCoupon(svcCtx),
			asyncjob.BizMerchantBase:         newImportMerchantBase(svcCtx),
			asyncjob.BizAppCategoryAdd:       newImportAppCategoryAdd(svcCtx),
			asyncjob.BizAppCategoryUpdate:    newImportAppCategoryUpdate(svcCtx),
			asyncjob.BizCouponMerchantAdd:    newImportCouponMerchantAdd(svcCtx),
			asyncjob.BizCouponMerchantUpdate: newImportCouponMerchantUpdate(svcCtx),
			asyncjob.BizCouponCategoryAdd:    newImportCouponCategoryAdd(svcCtx),
			asyncjob.BizCouponCategoryUpdate: newImportCouponCategoryUpdate(svcCtx),
		},
	}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *importDispatcher) ProcessTask(ctx context.Context, t *asynq.Task) error {
	task, retry := h.claimTypedTask(ctx, t, asyncjob.TypeImport)
	if retry != nil {
		return retry
	}
	if task == nil {
		return nil
	}

	p, ok := h.handlers[task.BizModule]
	if !ok {
		errMsg := fmt.Sprintf("asynctask: task %d unsupported import biz_module: %s", task.ID, task.BizModule)
		logx.WithContext(ctx).Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	rows, cleanup, err := h.readInputExcel(ctx, task)
	if err != nil {
		logx.WithContext(ctx).Error(err)
		h.finishFailed(ctx, task.ID, err.Error())
		return nil
	}
	defer cleanup()

	p.process(ctx, task, rows)
	return nil
}

// 编译期断言
var _ asynq.Handler = (*importDispatcher)(nil)

// exportProvider 单个业务模块的导出数据源：由 exportDispatcher 驱动通用导出流程
type exportProvider interface {
	// SheetName Excel 工作表名
	SheetName() string
	// Headers 导出表头（顺序与 ListPage 返回的行一致）
	Headers() []string
	// ExportFileName 导出文件名（含 taskID）
	ExportFileName(taskID uint64) string
	// Count 按 rawFilter（JSON 字符串，解析失败时实现方应降级为导出全部）统计总数
	Count(ctx context.Context, db *sql.DB, rawFilter string) (uint64, error)
	// ListPage 按 rawFilter 分页查询，返回已转为字符串的行（顺序与 Headers 一致）
	ListPage(ctx context.Context, db *sql.DB, rawFilter string, limit, offset int) ([][]string, error)
}

// exportDispatcher 导出任务分发器（pattern=async_task:export），按 task.BizModule 路由
type exportDispatcher struct {
	baseHandler
	providers map[string]exportProvider
}

// NewExportDispatcher 创建导出分发器，注册各业务模块的数据源
func NewExportDispatcher(svcCtx *svc.ServiceContext) *exportDispatcher {
	return &exportDispatcher{
		baseHandler: baseHandler{svcCtx: svcCtx},
		providers: map[string]exportProvider{
			asyncjob.BizAppDetail:    appDetailExportProvider{},
			asyncjob.BizAppCategory:  appCategoryExportProvider{},
			asyncjob.BizAppReview:    appReviewExportProvider{},
			asyncjob.BizSite:         siteExportProvider{},
			asyncjob.BizCoupon:       couponExportProvider{},
			asyncjob.BizMerchantBase: merchantBaseExportProvider{},
			asyncjob.BizCategoryBase: categoryBaseExportProvider{},
		},
	}
}

// ProcessTask 实现 asynq.Handler 接口
func (h *exportDispatcher) ProcessTask(ctx context.Context, t *asynq.Task) error {
	logger := logx.WithContext(ctx)

	task, retry := h.claimTypedTask(ctx, t, asyncjob.TypeExport)
	if retry != nil {
		return retry
	}
	if task == nil {
		return nil
	}

	p, ok := h.providers[task.BizModule]
	if !ok {
		errMsg := fmt.Sprintf("asynctask: task %d unsupported export biz_module: %s", task.ID, task.BizModule)
		logger.Error(errMsg)
		h.finishFailed(ctx, task.ID, errMsg)
		return nil
	}

	// 生产端约定：过滤参数 JSON 存放于 task.FileName 字段
	rawFilter := task.FileName

	total, err := p.Count(ctx, h.svcCtx.SitehubDB, rawFilter)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("统计数据总数失败: %v", err))
		return nil
	}
	if total == 0 {
		// 仍然上传一个只有表头的 Excel，方便用户确认
		url, e := h.writeEmptyExport(ctx, task.ID, p)
		if e != nil {
			logger.Error(e)
		}
		h.finishPartial(ctx, task.ID, 0, 0, 0, "无符合条件的数据", url)
		return nil
	}

	// 分页查询并写入 Excel
	w := helper.NewExcelWriter(p.SheetName(), p.Headers())
	defer func() { _ = w.Close() }()

	const pageSize = 500
	var success uint64
	for offset := 0; offset < int(total); offset += pageSize {
		if h.checkCanceled(ctx, task.ID) {
			h.finishCanceled(ctx, task.ID)
			return nil
		}
		rows, err := p.ListPage(ctx, h.svcCtx.SitehubDB, rawFilter, pageSize, offset)
		if err != nil {
			logger.Error(err)
			h.finishFailed(ctx, task.ID, fmt.Sprintf("查询导出数据失败: %v", err))
			return nil
		}
		for _, row := range rows {
			if e := w.WriteRow(row); e != nil {
				logger.Errorf("write export row: %v", e)
				continue
			}
			success++
		}
		h.reportProgress(ctx, task.ID, total, success, 0, calcProgress(success, total), "")
	}

	// 序列化并上传
	data, err := w.Bytes()
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("序列化 Excel 失败: %v", err))
		return nil
	}
	url, err := h.uploadResult(ctx, task.ID, "async_export", p.ExportFileName(task.ID), data, excelContentType)
	if err != nil {
		logger.Error(err)
		h.finishFailed(ctx, task.ID, fmt.Sprintf("上传导出文件失败: %v", err))
		return nil
	}

	if success == total {
		// 全部导出成功（结果文件 URL 已由 uploadResult 写入）
		h.finishSuccessWithMsg(ctx, task.ID, total, success,
			fmt.Sprintf("导出完成：共 %d 条", success))
	} else {
		fail := total - success
		h.finishPartial(ctx, task.ID, total, success, fail,
			fmt.Sprintf("导出完成：成功 %d 条，失败 %d 条", success, fail), url)
	}
	return nil
}

// writeEmptyExport 写一个只有表头的空 Excel 并上传
func (h *exportDispatcher) writeEmptyExport(ctx context.Context, taskID uint64, p exportProvider) (string, error) {
	w := helper.NewExcelWriter(p.SheetName(), p.Headers())
	defer func() { _ = w.Close() }()
	data, err := w.Bytes()
	if err != nil {
		return "", err
	}
	return h.uploadResult(ctx, taskID, "async_export", p.ExportFileName(taskID), data, excelContentType)
}

// 编译期断言
var _ asynq.Handler = (*exportDispatcher)(nil)
