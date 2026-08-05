# ---- build ----（纯 Go 无 CGO，可静态编译；关系库走 pgx/Postgres）
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd

# ---- runtime ----（不依赖 apk 网络：CA 证书从 build 阶段拷贝；adduser 为 busybox 内建）
FROM alpine:3.20
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/server /app/server
RUN adduser -D -u 10001 app
WORKDIR /app
USER app
EXPOSE 8280
# APP_ENV=prod 必须经环境注入 JWT_SECRET / ALLOWED_ORIGINS（见 docker-compose.prod.yml）。
ENTRYPOINT ["/app/server"]
