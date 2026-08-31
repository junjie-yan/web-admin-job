// Package pattern defines all the patterns used in tasks which used for Differentiating tasks
package pattern

import "github.com/junjie-yan/web-admin-job/pkg/asyncjob"

const RecordHelloWorld = "hello_world"

// 异步任务 pattern（从 asyncjob 公开包复用，payload 为 asyncjob.AsyncTaskPayload）
const (
	AsyncTaskImport      = asyncjob.PatternImport
	AsyncTaskBatchUpdate = asyncjob.PatternBatchUpdate
	AsyncTaskExport      = asyncjob.PatternExport
)
