# ---------- 构建阶段 ----------
# 版本必须 >= go.mod 里声明的 go 指令版本，否则构建会直接报
# "go.mod requires go >= 1.25.0"
FROM golang:1.25-alpine AS build

WORKDIR /src

# 先拷贝依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://goproxy.cn,direct && \
    go mod download

COPY . .

# 静态编译，产出单一可执行文件
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/kamihub ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.20

# 时区与根证书：提货页要显示正确时间，调用 HTTPS 上游要验证书
RUN apk add --no-cache ca-certificates tzdata wget && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app

COPY --from=build /out/kamihub /app/kamihub

# 上传目录必须在这里就建好并 chown 给 app。
#
# 原因：docker-compose 会把一个命名卷挂到 /app/data/uploads。
# 如果镜像里没有这个目录，Docker 会以 root:root 新建它，
# 而进程是以非 root 的 app 用户跑的 —— 结果是上传时报
# 「保存文件失败」，而且要到运营真正传图时才会暴露。
# 镜像里预先存在该目录时，Docker 会把目录的所有者一并复制进空卷。
RUN mkdir -p /app/data/uploads && chown -R app:app /app
USER app

ENV APP_ENV=production \
    APP_PORT=8080 \
    TZ=Asia/Shanghai

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${APP_PORT}/health" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/kamihub"]
