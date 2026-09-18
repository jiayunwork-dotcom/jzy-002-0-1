.PHONY: all build test vet fmt run docker-build docker-up docker-down tidy

APP      := heateserver
PKG      := ./...

all: vet test build

build:
	go build -o bin/$(APP) ./cmd/$(APP)

test:
	go test $(PKG) -count=1

test-v:
	go test $(PKG) -count=1 -v

vet:
	go vet $(PKG)

fmt:
	gofmt -w .

run:
	go run ./cmd/$(APP) -addr=:8080

# 一条命令构建容器并后台启动服务。
docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

docker-build:
	docker build -t heatexchanger-rating:latest .

tidy:
	go mod tidy
