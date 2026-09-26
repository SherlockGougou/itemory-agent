# syntax=docker/dockerfile:1
#
# Itemory NAS Agent —— 多架构镜像（linux/amd64 + linux/arm64）
#
# 两个构建阶段：先编译控制台前端，再交叉编译 Go 二进制。
#
# 为什么前端必须在镜像里构建：控制台是 Next.js 的静态导出（next.config.mjs 里
# output: 'export'），产物落在 internal/api/web/out，而 web.go 用
# //go:embed all:web/out 把它嵌进二进制。out/ 被 internal/api/web/.gitignore 忽略、
# 不进版本库——所以干净 checkout 里 go build 会直接报
# "pattern all:web/out: no matching files found"。本地能编过，只是因为开发机上
# 恰好留着上一次的 out/。

# ── 阶段 1：控制台前端 ───────────────────────────────────────────────
# 产物与架构无关，固定跑在 BUILDPLATFORM 上，避免多架构构建时被 QEMU 拖慢。
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS web
WORKDIR /web
# 先只拷清单再 npm ci：依赖没变时这一层能命中缓存。
COPY internal/api/web/package.json internal/api/web/package-lock.json ./
RUN npm ci
COPY internal/api/web/ ./
RUN npm run build

# ── 阶段 2：Go 二进制 ───────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
# 版本随构建注入，确保 /api/v1/health 报的版本与镜像 tag 一致
ARG VERSION=0.4.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用刚构建出来的前端产物填上 out/。.dockerignore 已把 out/ 排除出构建上下文，
# 所以这里拿到的必然是本次 npm run build 的结果。
COPY --from=web /web/out ./internal/api/web/out
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/itemory-agent ./cmd/itemory-agent

# ── 阶段 3：运行时 ─────────────────────────────────────────────────
# Debian slim（amd64/arm64 官方支持）
FROM debian:12-slim
# GHCR 依据 source 把镜像关联到源码仓库，并在包页面显示描述与许可证
LABEL org.opencontainers.image.source="https://github.com/SherlockGougou/itemory-agent" \
      org.opencontainers.image.description="Itemory NAS Agent: indexes photos and videos on a NAS and serves them to the Itemory app" \
      org.opencontainers.image.licenses="MIT"
RUN apt-get update && apt-get install -y --no-install-recommends \
      libvips-tools libheif1 libheif-examples ffmpeg ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -u 1000 -m -s /usr/sbin/nologin itemory \
    && mkdir -p /data && chown itemory:itemory /data

COPY --from=build /out/itemory-agent /usr/local/bin/itemory-agent

# 默认非 root；媒体目录只读挂载；/data 可写（命名卷开箱即用）
USER itemory
VOLUME ["/data"]
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["itemory-agent", "healthcheck"]

ENV ITEMORY_CACHE_DIR=/data \
    ITEMORY_HTTP_ADDR=:8787

ENTRYPOINT ["itemory-agent"]
