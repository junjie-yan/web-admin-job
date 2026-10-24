// Package asyncjob 提供异步任务（导入/导出）的公共数据模型与持久化管理。
//
// 本包被两个服务共享使用：
//   - web-admin API（生产端）：创建任务、查询列表、取消、删除
//   - web-admin-job（消费端）：标记 processing、更新进度、写入结果文件、标记完成
//
// 跨服务通信：
//   - web-admin API 通过 asynq.Client 把 task_id 推入 Redis 队列
//   - web-admin-job 通过 asynq.Server 消费队列，根据 task_id 执行业务
//   - 两边共享同一张 async_task 表（web_admin 主库）和同一个 Redis
package asyncjob

// 任务类型（物理动作）；具体业务语义（新增/更新/哪张表）由 BizModule 表达
const (
	TypeImport  = "import"  // 上传 Excel 由消费端解析入库（新增/更新/upsert 由 BizModule 决定）
	TypeExport  = "export"  // 查询数据生成 Excel
	TypeSitemap = "sitemap" // 生成站点 sitemap 并上传 R2 公开桶（无输入/结果文件）
)

// 任务状态
const (
	StatusPending    = "pending"    // 已创建，等待处理
	StatusProcessing = "processing" // 处理中
	StatusSuccess    = "success"    // 全部成功
	StatusFailed     = "failed"     // 全部失败（致命错误）
	StatusPartial    = "partial"    // 部分成功
	StatusCanceled   = "canceled"   // 已取消
)

// 业务模块枚举（注册新业务时在此追加，同步更新消费端 dispatcher 白名单）
const (
	BizAppDetail            = "app_detail"             // APP 详情（仅导出；导入请用下方 add / update / upsert 模块）
	BizAppDetailAdd         = "app_detail_add"         // APP 详情批量新增（只新增不存在数据）
	BizAppDetailUpdate      = "app_detail_update"      // APP 详情批量更新（按 站点域名+记录ID 只更新已有数据）
	BizAppDetailUpsert      = "app_detail_upsert"      // APP 详情批量导入（按商店链接定位：存在更新非空字段，不存在新增）
	BizCoupon               = "coupon"                 // 优惠券
	BizMerchantBase         = "coupon_merchant_base"   // 优惠券商家基础（upsert，保留兼容）
	BizCategoryBase         = "coupon_category_base"   // 优惠券分类基础（仅导出）
	BizAppCategoryAdd       = "app_category_add"       // APP 分类批量新增
	BizAppCategoryUpdate    = "app_category_update"    // APP 分类批量更新
	BizCouponMerchantAdd    = "coupon_merchant_add"    // 优惠券商家批量新增
	BizCouponMerchantUpdate = "coupon_merchant_update" // 优惠券商家批量更新
	BizCouponCategoryAdd    = "coupon_category_add"    // 优惠券分类批量新增
	BizCouponCategoryUpdate = "coupon_category_update" // 优惠券分类批量更新
	BizSitemap              = "sitemap"                // 站点 sitemap 生成（appfinder 类型站点）
)

// asynq pattern（与 web-admin-job 消费端约定，payload 为 AsyncTaskPayload）
// 放在公开包中以便 web-admin API 生产端引用，避免引用 internal 包
const (
	PatternImport  = "async_task:import"  // 批量导入
	PatternExport  = "async_task:export"  // 批量导出
	PatternSitemap = "async_task:sitemap" // sitemap 生成
)

// AsyncTaskPayload asynq 队列消息的 payload（两端共享）
// 设计为 JSON 序列化后用 asynq.Task payload 传递
type AsyncTaskPayload struct {
	TaskID uint64 `json:"task_id"`
}

// Task 任务完整记录，与 async_task 表字段一一对应
type Task struct {
	ID            uint64 `json:"id"`
	Name          string `json:"name"`          // 任务名称
	Type          string `json:"type"`          // 任务类型
	BizModule     string `json:"bizModule"`     // 业务模块
	SiteID        uint64 `json:"siteId"`        // 站点内任务：预置站点 ID（0=跨站点通用模板）
	Status        string `json:"status"`        // 状态
	TotalCount    uint64 `json:"totalCount"`    // 总条数
	SuccessCount  uint64 `json:"successCount"`  // 成功条数
	FailCount     uint64 `json:"failCount"`     // 失败条数
	Progress      uint8  `json:"progress"`      // 进度 0-100
	FileURL       string `json:"fileUrl"`       // 输入文件 URL（R2）
	FileName      string `json:"fileName"`      // 原始文件名；导出任务无输入文件，生产端复用该字段存放过滤参数 JSON
	ResultFileURL string `json:"resultFileUrl"` // 结果/错误明细文件 URL
	ErrorMessage  string `json:"errorMessage"`  // 整体错误信息
	ErrorDetail   string `json:"errorDetail"`   // 错误明细 JSON
	OperatorID    string `json:"operatorId"`    // 操作人 ID
	OperatorName  string `json:"operatorName"`  // 操作人名称
	StartedAt     *int64 `json:"startedAt"`     // 开始时间戳（秒）
	FinishedAt    *int64 `json:"finishedAt"`    // 完成时间戳（秒）
	CreatedAt     int64  `json:"createdAt"`     // 创建时间戳（秒）
	UpdatedAt     int64  `json:"updatedAt"`     // 更新时间戳（秒）
}

// CreateInput 创建任务入参
type CreateInput struct {
	Name         string // 任务名称
	Type         string // 任务类型
	BizModule    string // 业务模块
	SiteID       uint64 // 站点内任务：预置站点 ID（0=跨站点通用模板）
	FileURL      string // 输入文件 R2 URL（导出任务可为空）
	FileName     string // 原始文件名（导出任务可为空）
	OperatorID   string // 操作人 ID
	OperatorName string // 操作人名称
}

// ListFilter 列表查询过滤条件
type ListFilter struct {
	Status     string
	BizModule  string
	Type       string
	SiteID     uint64
	OperatorID string
	Keyword    string
	Page       uint64
	PageSize   uint64
}

// ListResult 列表查询结果
type ListResult struct {
	Total uint64  `json:"total"`
	Page  uint64  `json:"page"`
	Size  uint64  `json:"size"`
	Data  []*Task `json:"data"`
}

// FinishInput 完成任务入参（消费端使用）
type FinishInput struct {
	ID            uint64
	Status        string // StatusSuccess / StatusFailed / StatusPartial
	TotalCount    uint64
	SuccessCount  uint64
	FailCount     uint64
	ErrorMessage  string // 整体错误信息（可空）
	ResultFileURL string // 结果文件 URL（可空）
}

// ProgressUpdate 进度更新（消费端使用）
type ProgressUpdate struct {
	ID                uint64
	TotalCount        uint64 // 累计总数（已知总数时填，0 表示不更新）
	SuccessCount      uint64 // 累计成功数
	FailCount         uint64 // 累计失败数
	Progress          uint8  // 0-100
	ErrorDetailAppend string // 追加到 error_detail 的单行错误描述（已序列化前的单条文本，内部会做 JSON 转义与条数限制）
}
