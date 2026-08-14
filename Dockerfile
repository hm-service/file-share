# ==========================================
# 第一阶段：构建
# ==========================================
FROM golang:1.26-alpine AS builder

# TARGETOS / TARGETARCH 由 BuildKit 根据 --platform 自动注入，
# 用 buildx 构建 amd64/arm64 时会分别得到 linux/amd64 与 linux/arm64
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# 设置 Go 环境变量
# CGO_ENABLED=0: 禁用 CGO，确保编译出纯静态链接的二进制文件，这对于在 alpine 或 scratch 中运行至关重要
# GOOS/GOARCH 跟随目标平台，而不是写死 amd64
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH

# 设置工作目录
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -ldflags="-w -s": 移除调试信息和符号表，能显著减小编译后的二进制文件体积
RUN go build -ldflags="-w -s" -o file-share main.go

# ==========================================
# 第二阶段：运行
# ==========================================
FROM alpine:3

ENV TZ=Asia/Shanghai
ENV PORT=8080

RUN addgroup -g 1000 service && \
    adduser -u 1000 -D -G service -s /bin/sh service
RUN mkdir -p /app && chown 1000:1000 /app

COPY --from=builder /build/file-share /app

EXPOSE 8080

VOLUME [ "/data" ]

WORKDIR /app
USER 1000:1000
CMD ["./file-share"]
