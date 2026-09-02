# web-admin-job

[文档地址|Document](https://doc.ryansu.tech/zh/guide/official-comp/cron.html)

`web-admin-job` 是 [web-admin](https://github.com/junjie-yan/web-admin) 项目的在线定时任务 RPC 服务,基于 [simple-admin-job](https://github.com/suyuan32/simple-admin-job) 二次定制。

支持:
- 基于 asynq 的动态定时任务（cron 配置存 DB，后台修改即时生效）
- 异步批量任务（Excel 导入/导出/批量更新，进度持久化到 async_task 表）

---

`web-admin-job` is the online scheduled-task RPC service for the
[web-admin](https://github.com/junjie-yan/web-admin) project, customized from
[simple-admin-job](https://github.com/suyuan32/simple-admin-job).

Support: asynq schedule task & async batch jobs (import/export/batch update).

## 快速启动

```bash
# 前置：本地 MySQL（web_admin + sitehub 库）与 Redis 已启动，配置见 etc/job.yaml
go run job.go -f etc/job.yaml   # gRPC 监听 0.0.0.0:9105
```

## 文档

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 系统架构、核心模块、数据模型、设计决策 |
| [docs/development.md](docs/development.md) | 环境搭建、启动调试、新功能开发流程 |
| [docs/conventions.md](docs/conventions.md) | 开发规则与代码规范 |

AI 协作入口：[AGENTS.md](AGENTS.md) / [CLAUDE.md](CLAUDE.md)
