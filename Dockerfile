# 单进程：HTTP 后台、查看页、入站 SMTP。不发信。
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/laijian ./cmd/laijian

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app \
    && mkdir -p /data \
    && chown app:app /data
WORKDIR /app
COPY --from=build /out/laijian /app/laijian
USER app
ENV HTTP_ADDR=:8080 \
    SMTP_ADDR=:2525 \
    MAIL_DATA_DIR=/data \
    DATABASE_PATH=/data/mail.db \
    TZ=Asia/Shanghai
EXPOSE 8080 2525
VOLUME ["/data"]
ENTRYPOINT ["/app/laijian"]
