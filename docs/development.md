# 开发指南

> 本文描述如何搭建环境、启动调试，以及各类新功能的开发流程。架构背景见 [architecture.md](architecture.md)，编码规则见 [conventions.md](conventions.md)。

## 1. 环境准备

### 1.1 依赖服务

| 依赖 | 用途 | 默认配置（etc/job.yaml） |
|---|---|---|
| MySQL | `web_admin` 库（任务配置/状态） | 127.0.0.1:3306, root/root123 |
| MySQL | `sitehub` 库（app_detail 业务表） | 127.0.0.1:3306 |
| Redis | asynq 队列 | 127.0.0.1:6379, db0 |
| R2 | 任务文件与 SEO 资源上传/下载（可选，未配置时文件任务降级禁用） | 见 yaml `R2Confs`（实例列表，以 Bucket 名为标识：`sem-web-private` 私有桶存任务输入/结果文件，`sem-web` 公开桶存站点 sitemap 等公开资源） |

本地推荐用 docker-compose 起 MySQL + Redis（web-admin 仓库有现成编排）。数据库表无需手动建：服务启动时自动初始化。

### 1.2 工具链

- Go 1.26+
- [goctls](https://doc.ryansu.tech)（仅 proto/ent 代码生成时需要）
- golangci-lint v2（可选）：`make tools` 安装（配置见根目录 `.golangci.yml`）

## 2. 常用命令

```bash
# 启动服务（开发配置）
go run job.go -f etc/job.yaml
go run job.go -f etc/job.local.yaml   # 个人本地配置（Mode: dev 开 reflection；不入库）

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

# 临时调试脚本（放 tmp/，已 gitignore，用完即删）
go run ./tmp/inspector   # 示例：清理 asynq 队列遗留任务

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

### 4.1 新增异步批量任务（最常见）

以"导入 app_detail"为参照模板：

**Step 1 — 定义契约**（`pkg/asyncjob/types.go`，生产端/消费端共享）：

```go
// 业务模块枚举追加
const BizMyThing = "my_thing"
// pattern 仅在新增物理动作时追加（现有 import/export/sitemap 三条已覆盖绝大多数场景）
const PatternMyThingDo = "async_task:my_thing_do"
// 如需新任务类型，追加 TypeXXX 常量
```

**Step 2 — 声明 spec**（`pkg/importspec/app.go|coupon.go`，导入模块必做）：

```go
// 挂到对应垂直的 Specs：Module（=BizModule）、Entity/EntityZh（工作台分组）、
// Operation（add/update/upsert）、SiteScoped、Columns（列序=模板列序，golden 测试锁定）
```

同实体已有 add/update 时，旧 upsert 模块被元数据接口 hasSplit 规则自动隐藏；
spec 变更须与 web-admin 联合发版（job 打 tag → web-admin 升 go.mod 依赖，两仓红线）。

**Step 3 — 写 Handler**（`internal/mqs/amq/handler/amq/asynctask/my_thing.go`）：

```go
type MyThingHandler struct{ baseHandler }

func NewMyThingHandler(svcCtx *svc.ServiceContext) *MyThingHandler {
    return &MyThingHandler{baseHandler{svcCtx: svcCtx}}
}

func (h *MyThingHandler) process(ctx context.Context, task *asyncjob.Task, rows [][]string) {
    fl := newCacheFlusher(h.svcCtx)   // 写后自动缓存失效（见 Step 5）
    defer fl.flush(ctx)
    // 解析（业务子包）→ 批内去重 → 判重/存在校验 → 落库
    // 成功路径 fl.add(siteXxx, siteID, pageXxx...) 聚合刷新范围
    // 结果文件 + finishSuccess / finishPartial
}
```

业务域逻辑（Excel 行解析/名称解析/SQL）收敛到 `asynctask/<业务模块>/` 子包（参照 `appdetail/`），handler 只做编排。
完整骨架说明见 [common.go](../internal/mqs/amq/handler/amq/asynctask/common.go) 包注释。

**Step 4 — 注册分发器**（`dispatch.go` 的 handlers map，导入模块）或
[mqtask/register.go](../internal/mqs/amq/task/mqtask/register.go)（新 pattern）：

```go
asyncjob.BizMyThing: newImportMyThing(svcCtx),   // dispatch.go
mux.Handle(pattern.PatternMyThingDo, asynctask.NewMyThingHandler(m.svcCtx))  // register.go，仅新 pattern
```

**Step 5 — 生产端对接**（web-admin API 仓库）：

```go
taskID, _ := mgr.Create(ctx, asyncjob.CreateInput{Name: "...", Type: "...", BizModule: asyncjob.BizMyThing, ...})
client.Enqueue(asynq.NewTask(asyncjob.PatternImport, mustJSON(asyncjob.AsyncTaskPayload{TaskID: taskID})))
```

**Step 6 — 管理后台**：现有看板/元数据接口由 spec 驱动零改动；仅需在 web-admin 仓库
`deploy/sql/patch-site-type-config.sql` 补 `site_type_module` 配置行（启用/排序）与 casbin（若新增专用提交端点）。

> **非 Excel 任务变体**（参照 `sitemap.go`）：无输入文件，`claimTypedTask` 领取任务后直接查库执行、
> 分步 `reportProgress`，结果直传 R2 固定 key（`helper.PutBytesToKey`）。

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
