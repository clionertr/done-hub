FROM node:22.20 as builder

WORKDIR /build

COPY web/package.json .
COPY web/yarn.lock .

# arm64 QEMU 模拟构建时网络较慢，npmmirror 镜像易超时；
# 加长网络超时并保留 GHA 构建缓存（第二次构建命中缓存不再下载）
ENV YARN_NETWORK_TIMEOUT=600000
RUN yarn --frozen-lockfile

COPY ./web .
COPY ./VERSION .
RUN DISABLE_ESLINT_PLUGIN='true' VITE_APP_VERSION=$(cat VERSION) npm run build

FROM golang:1.25.5 AS builder2

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOPROXY=https://proxy.golang.org,direct

WORKDIR /build
ADD go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=builder /build/build ./web/build
RUN go build -ldflags "-s -w -X 'done-hub/common.Version=$(cat VERSION)'" -o done-hub

FROM alpine:latest

RUN apk update && \
    apk upgrade && \
    apk add --no-cache ca-certificates tzdata && \
    update-ca-certificates 2>/dev/null || true

COPY --from=builder2 /build/done-hub /
EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/done-hub"]
