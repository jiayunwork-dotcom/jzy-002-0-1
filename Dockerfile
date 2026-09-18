# syntax=docker/dockerfile:1

# ---- 构建阶段 ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# 纯标准库实现,无外部依赖;先拷贝 go.mod 以利用层缓存
COPY go.mod ./
RUN go mod download || true

COPY . .
# 静态构建、去除调试符号,CGO 关闭以便放进最小基础镜像
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/heatx-server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.20
# wget(busybox)供 HEALTHCHECK 使用;ca-certificates 以备将来出站调用
RUN apk add --no-cache ca-certificates wget && \
    addgroup -S heatx && adduser -S -G heatx heatx
COPY --from=build /out/heatx-server /usr/local/bin/heatx-server

USER heatx
EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/heatx-server"]
