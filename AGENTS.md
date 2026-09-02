# AGENTS.md — AI 协作开发指南

本文件是 AI 代理（Claude Code、Cursor、Copilot 等）在本仓库工作的**统一入口指令**。深度知识见 `docs/` 目录。

## 项目一句话

web-admin-job 是 web-admin 微服务的**任务执行中心**（go-zero gRPC + asynq + ent + MySQL×2 + R2）：管理后台通过 gRPC 管理定时任务配置（存 DB，PTM 动态生效），异步批量任务（Excel 导入/导出/批量更新）由 web-admin API 经 Redis 队列投递、本服务消费执行，进度回写共享的 `async_task` 表。

## 常用命令

```bash
go run job.go -f etc/job.yaml   # 启动（需本地 MySQL×2 + Redis，见 etc/job.yaml）
make gen-rpc                    # proto 变更后重新生成（需 goctls）
make gen-ent                    # ent/schema 变更后重新生成
make fmt && make lint && make test   # 提交前必跑
make help                       # 全部命令
```

## 架构速查

```
job.go ── serviceGroup 同时启动 3 个服务：
  gRPC :9105   internal/server → internal/logic → ent     （定时任务 CRUD）
  MQTask       internal/mqs/amq/task/mqtask/register.go    （asynq worker 路由表）
  DPTask       periodicconfig/provider.go 读 sys_tasks 表   （cron 动态派发）

两条任务链路：
  定时任务:  后台 createTask → sys_tasks 表 → PTM 按 cron 派发 → asynq → Handler
  异步任务:  web-admin API 建记录+入队 → Redis → Handler → 进度回写 async_task 表
             （生产端/消费端共享契约包 pkg/asyncjob：pattern/类型/状态/Manager）
```

## 开发规则（必须遵守，完整版见 docs/conventions.md）

1. **pattern/任务类型/业务模块/状态常量统一定义在 `pkg/asyncjob/types.go`**，禁止散落硬编码
2. **新任务 Handler 必须嵌入 `baseHandler`**（`handler/amq/asynctask/common.go`），复用 loadPayload/markProcessing/checkCanceled/reportProgress/finish* 生命周期
3. **业务性失败**：`finishFailed` + `return nil`（不触发重试）；**基础设施瞬时错误**才返回 error 让 asynq 重试
4. Handler 抢占失败（markProcessing 报错）直接 `return nil`
5. 长任务分批（写库 ≤50/批，IN 查询 ≤500/批），每批 `checkCanceled` + `reportProgress`
6. `internal/` 禁止被外部 import；`internal/server` 与 `ent/` 是生成代码禁止手改
7. sitehub 业务 SQL 用 `svcCtx.SitehubDB` 占位符查询，NULL 列用 `sql.Null*` 承接
8. 新配置项同步改 `internal/config/config.go` + `etc/job.yaml` + `etc/job.prod.yaml`
9. 注释/日志用中文，日志用 `logx.WithContext`，错误用 `%w` 包装
10. **涉及 async_task 表结构、pattern、payload 语义的变更必须与 web-admin API 仓库同步发版**

## 新功能入口

| 需求 | 步骤 | 详见 |
|---|---|---|
| 新异步批量任务 | 契约常量 → Handler → register.go 注册 → 生产端入队 | docs/development.md §4.1 |
| 新定时任务 | 后台 gRPC 配置即可（零代码）；新处理逻辑才写 Handler | docs/development.md §4.2 |
| 新 RPC 接口 | job.proto → make gen-rpc → logic 实现 | docs/development.md §4.3 |
| 改表结构 | ent/schema → make gen-ent；async_task 改 pkg/asyncjob | docs/development.md §4.4 |

## 完成定义（DoD）

- [ ] `make fmt && make lint && make test` 通过
- [ ] 遵守上述规则（尤其 #1–#5、#10）
- [ ] 架构/流程/规则有变化时，已同步更新 `docs/` 对应文档
