# 间壁式换热器核算服务
# 一条命令构建: make build ; 容器一键启动: make up

APP      := heatx-server
PKG      := ./...
BIN      := bin/$(APP)
PORT     ?= 8080

.PHONY: all build run test testv vet fmt tidy docker-build up down logs clean

all: vet test build

## build: 本地编译静态二进制到 bin/
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/server

## run: 直接运行(默认端口 8080,可用 PORT 覆盖)
run:
	HEATX_PORT=$(PORT) go run ./cmd/server

## test: 运行全部自动化测试
test:
	go test -count=1 $(PKG)

## testv: 详细输出
testv:
	go test -count=1 -v $(PKG)

## race: 竞态检测
race:
	go test -race -count=1 $(PKG)

## cover: 覆盖率
cover:
	go test -cover $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

## docker-build: 构建容器镜像
docker-build:
	docker build -t $(APP):latest .

## up: Docker Compose 一键构建并后台启动
up:
	docker compose up --build -d

## down: 停止并移除容器
down:
	docker compose down

logs:
	docker compose logs -f

clean:
	rm -rf bin
