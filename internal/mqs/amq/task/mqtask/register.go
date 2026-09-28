package mqtask

import (
	"github.com/hibiken/asynq"

	"github.com/junjie-yan/web-admin-job/internal/mqs/amq/handler/amq/asynctask"
	"github.com/junjie-yan/web-admin-job/pkg/asyncjob"
)

// Register adds task to cron. | 在此处定义任务处理逻辑，注册worker.
func (m *MQTask) Register() {
	mux := asynq.NewServeMux()

	// 异步任务（导入/导出），pattern 常量统一定义在 pkg/asyncjob
	mux.Handle(asyncjob.PatternImport, asynctask.NewImportAppDetailHandler(m.svcCtx))
	mux.Handle(asyncjob.PatternExport, asynctask.NewExportAppDetailHandler(m.svcCtx))

	m.mux = mux
}
