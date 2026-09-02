# 开发指南

> 本文描述如何搭建环境、启动调试，以及各类新功能的开发流程。架构背景见 [architecture.md](architecture.md)，编码规则见 [conventions.md](conventions.md)。

## 1. 环境准备

### 1.1 依赖服务

| 依赖 | 用途 | 默认配置（etc/job.yaml） |
|---|---|---|
| MySQL | `web_admin` 库（任务配置/状态） | 127.0.0.1:3306, root/root123 |
| MySQL | `sitehub` 库（app_detail 业务表） | 127.0.0.1:3306 |
| Redis | asynq 队列 | 127.0.0.1:6379, db0 |
| R2 | Excel 文件上传/下载（可选，未配置时文件任务降级禁用） | 见 yaml `R2Conf` |

本地推荐用 docker-compose 起 MySQL + Redis（web-admin 仓库有现成编排）。数据库表无需手动建：服务启动时自动初始化。

### 1.2 工具链

- Go 1.26+
- [goctls](https://doc.ryansu.tech)（仅 proto/ent 代码生成时需要）
- golangci-lint v2（可选）：`make tools` 安装（配置见根目录 `.golangci.yml`）

## 2. 常用命令

```bash
# 启动服务（开发配置）
go run job.go -f etc/job.yaml

# 代码生成
make gen-rpc            # proto → types/job + jobclient + internal/server 骨架
make gen-ent            # ent/schema → ORM 代码（含自定义模板）
make gen-rpc-ent-logic model=Task group=task   # 从 ent schema 生成 CRUD logic + proto

# 质量检查
make fmt                # gofmt
make lint               # golangci-lint
make test               # go test ./internal/..

# 构建
make build-mac / build-linux / build-win
# 生产镜像由 CI（.github/workflows/deploy.yml）基于 docker/Dockerfile 多阶段构建并推送 ghcr.io

# 查看 make help
```

> `gen-rpc` 会执行 sed 去除 pb 文件中的 `,omitempty`，Mac/Linux 已在 Makefile 内处理。

## 3. 启动流程速览

[job.go](../job.go) 启动顺序：

1. 加载配置 `etc/job.yaml`（`-f` 可指定）
2. `svc.NewServiceContext`：初始化 ent/双 MySQL/Redis/asynq/AsyncTaskMgr/R2（失败仅告警不阻塞的部分：async_task 建表、R2 客户端）
3. 注册 gRPC 服务（`Job`，监听 `0.0.0.0:9105`，dev/test 模式开启 grpc reflection）
4. `serviceGroup` 同时启动：gRPC + MQTask worker + DPTask（按开关）

验证启动成功：日志出现 `Starting rpc server at 0.0.0.0:9105...`，且 worker 无 `failed to start mqtask server` 报错。

## 4. 新功能开发流程

### 4.1 新增异步批量任务（最常见，5 步）

以"导入 app_detail"为参照模板：

**Step 1 — 定义契约**（`pkg/asyncjob/types.go`，生产端/消费端共享）：

```go
// 业务模块枚举追加
const BizMyThing = "my_thing"
// pattern 追加
const PatternMyThingDo = "async_task:my_thing_do"
// 如需新任务类型，追加 TypeXXX 常量
```

**Step 2 — 写 Handler**（`internal/mqs/amq/handler/amq/asynctask/my_thing.go`）：

```go
type MyThingHandler struct{ baseHandler }

func NewMyThingHandler(svcCtx *svc.ServiceContext) *MyThingHandler {
    return &MyThingHandler{baseHandler{svcCtx: svcCtx}}
}

func (h *MyThingHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
    payload, err := h.loadPayload(t)          // 1. 解析 task_id
    if err != nil { return err }              // 基础设施错误 → 返回触发重试
    task, err := h.markProcessing(ctx, payload.TaskID)  // 2. CAS 抢占
    if err != nil { return nil }              // 被抢占/取消 → 不重试
    // 3. 业务校验（biz_module/type 不符 → finishFailed + return nil）
    // 4. 分批处理：每批 checkCanceled → 执行 → reportProgress
    // 5. h.finishSuccess / finishPartial / finishFailed 写终态
    return nil
}
```

完整骨架说明见 [common.go](../internal/mqs/amq/handler/amq/asynctask/common.go) 包注释。

**Step 3 — 注册路由**（[mqtask/register.go](../internal/mqs/amq/task/mqtask/register.go)）：

```go
mux.Handle(pattern.PatternMyThingDo, asynctask.NewMyThingHandler(m.svcCtx))
```

**Step 4 — 生产端对接**（web-admin API 仓库）：

```go
taskID, _ := mgr.Create(ctx, asyncjob.CreateInput{Name: "...", Type: "...", BizModule: asyncjob.BizMyThing, ...})
client.Enqueue(asynq.NewTask(asyncjob.PatternMyThingDo, mustJSON(asyncjob.AsyncTaskPayload{TaskID: taskID})))
```

**Step 5 — 管理后台**：新增任务列表/进度页（读写 async_task 表，字段说明见 architecture.md §6.2）。

### 4.2 新增定时任务

- **仅配置（零代码）**：通过管理后台 gRPC `createTask` 创建记录，填 `pattern`（须已在 register.go 注册）、`payload`（JSON）、`cron_expression`，PTM 自动加载生效
- **新增处理逻辑**：在 `internal/mqs/amq/handler/amq/` 下新建包，实现 `asynq.Handler`（`ProcessTask` 方法），到 register.go 注册，然后通过后台创建配置

### 4.3 新增 RPC 接口

1. [job.proto](../job.proto) 增加 message 与 rpc 定义（注意 `// group: xxx` 注释规范）
2. `make gen-rpc`
3. 在 `internal/logic/<group>/` 实现业务逻辑；纯 CRUD 可用 `make gen-rpc-ent-logic model=Xxx group=xxx` 直接生成
4. 调用方通过 `jobclient.NewJob(zrpc client)` 访问

### 4.4 修改数据库表结构

1. 修改 [ent/schema/](../ent/schema/) 下 schema（表名用 `entsql.Annotation{Table: ...}` 指定 `sys_` 前缀）
2. `make gen-ent` 重新生成
3. 涉及 RPC 的变更走 §4.3 流程
4. async_task 表结构变更：改 `pkg/asyncjob/manager.go` 的 `EnsureTable` DDL 与相应 SQL（注意 web-admin 端也依赖此包，需同步升级）

## 5. 调试技巧

- **观察队列**：asynq 自带 CLI `asynq`（`asynq stats` / `asynq task ls`）或 [asynqmon](https://github.com/hibiken/asynqmon) Web UI，连同一个 Redis 即可看到队列与任务
- **SQL 日志**：`DatabaseConf.Debug: true` 开启 ent 调试日志
- **手动触发定时任务**：将 cron 表达式临时改为 `*/1 * * * *`
- **手动投递异步任务**：往 async_task 表插一条 pending 记录 + 用 asynq CLI/代码入队对应 pattern
- **gRPC 调试**：dev 模式开启了 reflection，可用 grpcurl 直接调用

## 6. 部署

推送 `main` 分支自动触发 [.github/workflows/deploy.yml](../.github/workflows/deploy.yml)：构建镜像 → 推 ghcr.io → SSH 滚动更新 `core-job` → 健康检查（9105 TCP，12×5s）→ 失败自动回滚。生产配置为 `etc/job.prod.yaml`（由部署环境注入）。
