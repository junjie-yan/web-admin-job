# 开发规则与代码规范

> 本文是项目开发规则（coding conventions & rules），供人工与 AI 协作者共同遵守。违反规则的 PR 应在评审时打回。

## 1. 分层与依赖规则

```
job.go (入口)
  └── internal/server      ← 只做参数转发，禁止写业务
        └── internal/logic ← 业务逻辑层
              └── svc.ServiceContext / ent / pkg/asyncjob
internal/mqs/amq/handler  ← asynq 消费端 Handler
pkg/asyncjob              ← 跨服务公共契约（与 web-admin API 共享）
```

**强制规则：**

| # | 规则 |
|---|---|
| R1 | `pkg/` 下只放需要被 web-admin API 引用的公共契约；`internal/` 禁止被外部服务 import |
| R2 | pattern、任务类型、业务模块、状态等常量**必须**定义在 `pkg/asyncjob`，禁止在 handler 内散落硬编码字符串 |
| R3 | server 层（`internal/server`）是生成代码，禁止手改；业务一律写在 logic |
| R4 | 异步任务 pattern 常量直接使用 `pkg/asyncjob`（`asyncjob.PatternXxx`），禁止新建中间 re-export 包装包，保持单一事实源 |
| R5 | 跨服务共享的表（async_task）只通过 `asyncjob.Manager` 读写，禁止在 handler 里裸写 SQL |

## 2. Handler 编写规则（asynq 消费端）

| # | 规则 |
|---|---|
| H1 | Handler 必须嵌入 `baseHandler` 复用生命周期方法，禁止自行实现 payload 解析/状态流转 |
| H2 | 执行前必须 `markProcessing`（CAS 抢占）；抢占失败返回 `nil`（任务已被他人接管或取消） |
| H3 | **业务性失败**（数据错误、解析失败、校验不过）：`finishFailed`/`finishPartial` 后 `return nil`，不触发 asynq 重试 |
| H4 | **基础设施性错误**（DB 连不上、R2 不可用等瞬时故障）：可返回 error 交由 asynq 重试 |
| H5 | 长任务必须分批处理，每批执行 `checkCanceled`（取消检查）与 `reportProgress`（进度回写） |
| H6 | 批量写库单批 ≤ 50 行；`IN` 查询单批 ≤ 500 条；错误明细最多保留 200 条（由 Manager 保证） |
| H7 | Handler 必须幂等友好：重试场景下不能产生重复副作用（写库依赖 CAS/唯一键兜底） |

## 3. 数据库规则

| # | 规则 |
|---|---|
| D1 | ent 管理的表（定时任务配置）放 `web_admin` 库，schema 在 `ent/schema/`，表名 `sys_` 前缀 |
| D2 | sitehub 业务表操作走 `svcCtx.SitehubDB` 原生 SQL，统一收敛在 `handler/amq/asynctask/app_detail_sql.go` 同级文件 |
| D3 | SQL 参数一律使用占位符 `?`，禁止字符串拼接值（防注入）；表名/列名拼接仅允许来自代码内白名单常量 |
| D4 | 查询可能为 NULL 的列必须用 `sql.Null*` 承接（线上历史数据存在 NULL） |
| D5 | schema 变更后必须 `make gen-ent`，禁止手改 `ent/` 生成代码 |
| D6 | 多表事务使用 `internal/utils/entx` 的 TxCtx 封装 |

## 4. 代码风格

| # | 规则 |
|---|---|
| S1 | 注释与日志使用中文；导出符号必须有注释（golangci-lint 强制） |
| S2 | 命名遵循 goctl style=go_zero（Makefile 已配置），生成文件勿手改 |
| S3 | 日志用 `logx`（带 ctx：`logx.WithContext(ctx)`），禁止 `fmt.Println` 调试残留 |
| S4 | 错误处理用 `%w` 包装并附加上下文前缀（如 `asynctask: xxx: %w`） |
| S5 | 提交前执行 `make fmt && make lint` |
| S6 | 敏感信息（密钥/密码）禁止入库，使用配置文件或 CI secrets |

## 5. 配置规则

| # | 规则 |
|---|---|
| C1 | 新增配置项：`internal/config/config.go` 加字段（`json:",optional"` 标注）+ `etc/job.yaml` 与 `etc/job.prod.yaml` 同步补默认值 |
| C2 | 可选依赖（如 R2）未配置时必须优雅降级（nil 客户端 + 明确报错），禁止 panic |

## 6. Git 与发布规则

| # | 规则 |
|---|---|
| G1 | 提交信息用英文祈使句或中文均可，需体现"为什么"；示例：`fix: avoid duplicate import when worker retries` |
| G2 | 推送 `main` 即自动部署生产（core-job），**合并前必须本地验证启动 + lint** |
| G3 | 破坏性变更（async_task 表结构、pattern 语义、payload 结构）必须与 web-admin API 端同步发版，先合本仓库后合 API 端 |
| G4 | 部署失败回滚由 CI 自动执行，人工不要在服务器上直接改容器 |

## 7. 评审清单（PR 自检）

- [ ] 新任务 pattern/类型/模块常量已加入 `pkg/asyncjob`，且生产端已同步？
- [ ] Handler 已注册到 `mqtask/register.go`？
- [ ] 分批处理 + 取消检查 + 进度回写齐全？
- [ ] 业务失败走 `finishFailed` + `return nil`，无意外重试风险？
- [ ] SQL 无注入风险、NULL 承接正确？
- [ ] `make fmt` / `make lint` / `make test` 通过？
- [ ] 相关文档（architecture.md / development.md）已同步更新？
