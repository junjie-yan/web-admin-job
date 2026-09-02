# 项目文档

> web-admin-job 知识库索引。**修改代码时请同步维护对应文档**，文档与代码不一致视为缺陷。

## 文档导航

| 文档 | 内容 | 何时阅读/更新 |
|---|---|---|
| [architecture.md](architecture.md) | 系统架构、核心模块职责、数据模型、设计决策 | 理解系统时；新增/调整模块后 |
| [development.md](development.md) | 环境搭建、启动调试、各类新功能的开发流程 | 开发新功能前；流程变化时 |
| [conventions.md](conventions.md) | 分层依赖规则、Handler/DB/配置规范、评审清单 | 编码与评审时；规则变更时 |

## 快速上手（30 秒版）

```bash
go run job.go -f etc/job.yaml   # 启动（需本地 MySQL×2 + Redis）
make help                        # 查看全部命令
```

- 新增**异步批量任务**：`pkg/asyncjob` 定义常量 → 写 Handler（嵌 `baseHandler`）→ `mqtask/register.go` 注册 → 生产端入队 → 详见 development.md §4.1
- 新增**定时任务**：后台 gRPC 配置即可，无需改代码；新处理逻辑才需要写 Handler
- 架构疑问 → architecture.md；编码规范 → conventions.md

## 文档维护约定

1. **单一事实源**：架构知识只在 architecture.md，开发流程只在 development.md，规则只在 conventions.md，其他文件（含 AGENTS.md / CLAUDE.md / README.md）只做引用摘要，避免多处漂移
2. **变更联动**：PR 涉及架构调整、新开发模式、新规则时，必须同步更新对应文档，并在 PR 描述中注明
3. **入口约定**：AI 协作工具（Claude Code 等）入口为根目录 `AGENTS.md` / `CLAUDE.md`，新协作者入口为本目录
