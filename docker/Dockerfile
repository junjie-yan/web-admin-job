# syntax=docker/dockerfile:1.7
# 多阶段构建 - web-admin-job（异步任务消费者 + job RPC）
# 与 web-admin 的 docker/Dockerfile.api 同模式：builder 编译，runtime 仅含二进制

FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

# 利用 Docker 层缓存，先复制依赖描述
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download && go mod verify

# 复制源码并构建（入口 job.go）
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -buildvcs=false -o job_rpc .

# ────────────────────────────────────────────────────────
# 运行阶段 - 极简镜像（无 Go 环境）
# ────────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata busybox-extras \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone \
    && addgroup -S app && adduser -S app -G app

WORKDIR /app

# 复制二进制；用 job.prod.yaml 覆盖为 job.yaml（生产配置已写死容器网络值）
COPY --from=builder /build/job_rpc ./
COPY --from=builder /build/etc/job.prod.yaml /app/etc/job.yaml

RUN chown -R app:app /app
USER app

# job RPC 默认端口 9105; Prometheus 在 4005（均不对外暴露，仅健康检查用）
EXPOSE 9105 4005

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD nc -z 127.0.0.1 9105 || exit 1

ENTRYPOINT ["./job_rpc", "-f", "etc/job.yaml"]
