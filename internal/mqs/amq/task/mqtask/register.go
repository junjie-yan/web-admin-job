package mqtask

import (
	"github.com/hibiken/asynq"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// Register adds task to cron. | 在此处定义任务处理逻辑，注册worker.
func (m *MQTask) Register() {
	mux := asynq.NewServeMux()

	// 异步任务（导入/导出/sitemap），pattern 常量统一定义在 pkg/asyncjob
	// asynq 同一 pattern 仅允许注册一个 handler，多业务模块（app_detail/coupon 等）
	// 由 dispatcher 领取任务后按 biz_module 路由，详见 asynctask/dispatch.go
	mux.Handle(asyncjob.PatternImport, asynctask.NewImportDispatcher(m.svcCtx))
	mux.Handle(asyncjob.PatternExport, asynctask.NewExportDispatcher(m.svcCtx))
	mux.Handle(asyncjob.PatternSitemap, asynctask.NewSitemapDispatcher(m.svcCtx))

	m.mux = mux
}
