# 系统架构与核心模块

> 本文是 web-admin-job 的架构知识文档，描述系统定位、模块职责、数据流与关键设计决策。

## 1. 系统定位

`web-admin-job` 是 [web-admin](https://github.com/junjie-yan/web-admin) 微服务体系中的**任务执行中心**，基于 [simple-admin-job](https://github.com/suyuan32/simple-admin-job) 二次定制。与 web-admin API 服务**共享数据库与 Redis，通过 asynq 队列协作**（无 RPC 直连依赖）。

承载两类工作负载：

| 负载 | 配置存储 | 触发方式 | 执行方式 |
|---|---|---|---|
| **定时任务**（cron） | `web_admin.sys_tasks` 表 | PeriodicTaskManager 按 cron 动态派发 | asynq worker 消费 |
| **异步批量任务**（Excel 导入/导出/批量更新） | `web_admin.async_task` 表 | web-admin API 生产端入队 | asynq worker 消费 |

## 2. 技术栈

| 组件 | 用途 |
|---|---|
| go-zero (zrpc) | gRPC RPC 框架，服务治理 |
| asynq | 基于 Redis 的异步任务队列与 cron 调度 |
| ent | ORM，管理 `sys_tasks` / `sys_task_logs` |
| MySQL ×2 | `web_admin`（任务配置与状态）+ `sitehub`（业务数据 app_detail） |
| Cloudflare R2 (S3 兼容) | Excel 输入/输出文件存储 |
| excelize | Excel 解析与生成 |

## 3. 全景架构图

```
┌──────────────────┐       gRPC :9105        ┌─────────────────────────────┐
│  web-admin 管理后台│ ──────────────────────► │       web-admin-job          │
│  (API/UI 服务)    │   定时任务 CRUD 管理      │       (本服务 core-job)       │
│                  │                         │                             │
│  asynq.Client ───┼──┐                      │  ◄──┐ asynq.Server (mqtask)  │
└──────────────────┘  │                      │     │                       │
                      ▼                      │     │                       │
                ┌──────────┐  pattern+payload│     │                       │
                │  Redis    │ ═══════════════╪═════╝                       │
                └──────────┘   任务队列        │                             │
                      ▲                      │                             │
                      │ cron 动态派发          │                             │
        ┌─────────────┴───────────┐          ▼                             │
        │ MySQL web_admin 库       │  ┌──────────────────┐                 │
        │ sys_tasks / sys_task_logs│  │ MySQL sitehub 库  │                 │
        │ async_task (两端共享) ◄───┼──┤ app_detail (业务) │                 │
        └─────────────────────────┘  └──────────────────┘                 │
                      ▲                      │                             │
                      │ Excel 上传/下载       │                             │
               ┌──────┴───────┐              │                             │
               │ Cloudflare R2 │◄────────────┘                             │
               └──────────────┘                                            │
```

## 4. 进程内服务拓扑

入口 [job.go](../job.go) 通过 `serviceGroup` 同时托管 3 个服务：

| 服务 | 代码位置 | 职责 | 启用开关 |
|---|---|---|---|
| gRPC Server | `internal/server` | 定时任务/日志的 CRUD RPC | 总是 |
| MQTask | `internal/mqs/amq/task/mqtask` | asynq worker，消费所有任务 | 总是 |
| DPTask | `internal/mqs/amq/task/dynamicperiodictask` | PeriodicTaskManager，从 DB 动态加载 cron | `TaskConf.EnableDPTask` |

**ServiceContext**（`internal/svc/service_context.go`）是全局依赖容器，初始化：

| 字段 | 类型 | 说明 |
|---|---|---|
| `DB` | `*ent.Client` | web_admin 库，定时任务配置表 |
| `SitehubDB` | `*sql.DB` | sitehub 库原生 SQL，app_detail 批量操作 |
| `AsynqServer` | `*asynq.Server` | 任务执行 worker |
| `AsynqPTM` | `*asynq.PeriodicTaskManager` | 动态 cron 调度（配置源：DB） |
| `AsyncTaskMgr` | `*asyncjob.Manager` | async_task 表持久化管理器 |
| `R2Client` | `*s3.Client` | R2 文件客户端（未配置时为 nil，文件功能降级禁用） |

## 5. 核心模块

### 5.1 RPC 层（定时任务管理）

标准 go-zero 分层：`proto → server → logic → ent`。

- 契约：[job.proto](../job.proto)，提供 `task`（定时任务配置）与 `tasklog`（执行日志）两组 CRUD
- 生成产物：`types/job/`（pb 与服务端骨架）、`jobclient/`（供 web-admin 引用的客户端 SDK）
- 生成命令：`make gen-rpc`（需安装 goctls）、`make gen-rpc-ent-logic`（从 ent schema 生成 CRUD logic）

### 5.2 调度层

- **动态定时**（[periodicconfig/provider.go](../internal/mqs/amq/types/periodicconfig/provider.go)）：PTM 定期查询 `sys_tasks` 表中 `status=正常` 的记录，每条转成 `asynq.PeriodicTaskConfig`（cron 表达式 + pattern + payload）。**修改配置即时生效，无需重启**
- **worker 路由**（[mqtask/register.go](../internal/mqs/amq/task/mqtask/register.go)）：pattern → Handler 的映射表，**新增任务处理逻辑必改此文件**

### 5.3 执行层（异步批量任务）

位置：`internal/mqs/amq/handler/amq/asynctask/`

| 文件 | 职责 |
|---|---|
| `common.go` | **baseHandler**：任务生命周期公共方法（claimTask/readInputExcel/finish* 等，所有 Handler 嵌入复用） |
| `errdetail.go` | 错误明细 Excel 生成并上传 R2（导入/批量更新共用） |
| `import_app_detail.go` | Excel 批量导入 app_detail（编排：解析 → 查重 → 批量插入） |
| `batch_update_app_detail.go` | Excel 批量更新 app_detail（编排：解析 → 定位 → 逐条更新） |
| `export_app_detail.go` | 导出 app_detail 为 Excel 上传 R2 |
| `appdetail/` | **app_detail 业务域子包**：`parse.go` 行解析 / `resolver.go` 名称→ID / `repo.go` SQL 读写 / `export.go` 导出行结构与格式化 |

> Handler 只负责任务编排，业务域逻辑（行解析/SQL）收敛在同名子包；新增业务模块时参照 `appdetail/` 建子包。

**baseHandler 生命周期方法**（详见 `common.go` 包注释）：

```
claimTask          前置流程封装：解析 payload → markProcessing（CAS 抢占）→ 校验 biz/type
readInputExcel     下载 R2 输入文件并解析为数据行
checkCanceled      长任务每批次检查是否被取消
reportProgress     每批次回写进度快照（含错误明细追加）
uploadResult       失败明细/导出文件上传 R2
finishSuccess / finishPartial / finishFailed / finishCanceled  写终态
```

**标准执行骨架**：

```
claimTask（payload + CAS 抢占 + biz/type 校验）→ readInputExcel
→ 分批执行业务（每批: 检查取消 → 处理 → reportProgress）→ uploadResult → finish*
```

### 5.4 公共契约层（跨服务共享）— `pkg/asyncjob/`

**这是理解异步任务体系的关键**。`pkg/asyncjob` 是唯一同时被 web-admin API（生产端）和本服务（消费端）引用的公开包（放 `pkg` 而非 `internal` 正为此设计）：

- 任务类型常量：`import / export / batch_update`
- 状态机：`pending → processing → success / failed / partial / canceled`
- 业务模块：`BizAppDetail = "app_detail"`（**新业务在此追加**）
- asynq pattern：`async_task:import / async_task:batch_update / async_task:export`
- `AsyncTaskPayload`：队列消息体，只含 `task_id`（任务详情从 async_task 表读取）
- `Manager`：async_task 表全部持久化操作（Create / MarkProcessing / UpdateProgress / Finish / Cancel / Delete / List / GetByID），启动时幂等建表

两端协作靠三样共享资源：**async_task 表（状态）+ Redis（消息）+ R2（文件）**；代码层面靠本契约包对齐。

### 5.5 支撑模块

| 目录 | 职责 |
|---|---|
| `internal/helper/` | Excel 解析/生成（excelize）、R2 客户端与文件上传下载 |
| `internal/utils/entx/` | ent 事务封装（TxCtx/WithTx） |
| `internal/utils/dberrorhandler/` | DB 错误统一转换 |
| `ent/` + `ent/schema/` | ORM 生成代码与 schema（task → `sys_tasks`，task_log → `sys_task_logs`） |
| `etc/` | 配置文件（job.yaml 默认开发 / job.local.yaml 个人本地·不入库 / job.test.yaml 测试 / job.prod.yaml 生产） |

## 6. 数据模型

### 6.1 `web_admin.sys_tasks`（定时任务配置，ent 管理）

| 字段 | 说明 |
|---|---|
| name / task_group | 名称 / 分组 |
| cron_expression | cron 表达式（PTM 动态加载） |
| pattern | 任务路由键（须在 register.go 已注册） |
| payload | JSON 字符串，消费时传给 Handler |
| status | 启用/停用（停用后 PTM 不派发） |

`pattern` 字段有唯一索引。执行日志写入 `sys_task_logs`。

### 6.2 `web_admin.async_task`（异步任务状态，两端共享，原生 SQL 管理）

关键字段：`type`（import/export/batch_update）、`biz_module`、`status`（状态机）、`total_count / success_count / fail_count / progress`、`file_url / result_file_url`（R2）、`error_message / error_detail`（整体错误 + 前 200 条明细 JSON）、`operator_id / operator_name`、时间戳。

建表由 `asyncjob.Manager.EnsureTable` 幂等执行（两端服务启动时都会调用）。

### 6.3 `sitehub.app_detail`（业务表）

由异步任务通过 `SitehubDB` 原生 SQL 批量写入/更新/导出。**直连业务库是刻意设计**：避免 web-admin-job 反向依赖 web-admin 的 coreclient 造成循环依赖。

## 7. 关键设计决策

1. **配置与执行分离**：gRPC 只管理任务配置，真正执行走 asynq 队列，worker 宕机任务不丢失（Redis 持久化）。
2. **动态调度**：cron 配置存 DB，PTM 轮询加载，后台改配置即时生效。
3. **CAS 防重复消费**：`MarkProcessing` 仅允许 `pending → processing`，多 worker 竞争时只有一个成功；已取消任务直接跳过。
4. **业务失败 ≠ 基础设施失败**：Handler 中业务性失败（解析失败、数据错误）调用 `finishFailed` 后 `return nil`，**不触发 asynq 重试**；只有基础设施级错误才返回 error 让 asynq 重试。
5. **批处理约定**：Excel 导入每批 50 行、`IN` 查询每批 500 条、错误明细最多保留前 200 条。
6. **可观测性**：Prometheus :4005/metrics；部署流水线 TCP 健康检查 9105，失败自动回滚。

## 8. 部署架构

[.github/workflows/deploy.yml](../.github/workflows/deploy.yml)：推送 `main` 分支自动触发——

```
构建 Docker 镜像 (docker/Dockerfile, linux/amd64)
  → 推送 ghcr.io/{owner}/web-admin-job:{sha,latest}
  → SSH 到服务器 /opt/web-admin
  → docker compose 仅重建 core-job（compose 定义在 web-admin 仓库）
  → TCP 健康检查 9105（12 次 × 5s）
  → 失败自动回滚到旧镜像 tag
```


