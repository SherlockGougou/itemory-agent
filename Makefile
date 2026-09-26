BINARY := itemory-agent
PKG := ./cmd/itemory-agent
WEB_DIR := internal/api/web
# 版本注入：/api/v1/health 报的版本必须与发布的镜像 tag 一致
VERSION ?= 0.4.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt run docker tidy web web-check

# 构建前必须先产出前端产物：web.go 用 //go:embed all:web/out，而 out/ 不进版本库
# （见 $(WEB_DIR)/.gitignore）。干净 checkout 里直接 `make build` 会报
# "pattern all:web/out: no matching files found"。
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

# 编译控制台前端到 $(WEB_DIR)/out。
# 走 npm ci 而不是 npm install：构建要可复现，不能顺手改 package-lock.json。
web:
	cd $(WEB_DIR) && npm ci && npm run build

# 在 Docker 里跑一次干净构建，用来验证「不带本地 out/ 也能出镜像」。
# 本地直接 make build 会借用开发机上残留的 out/，掩盖构建链路的断裂。
web-check:
	docker build --build-arg VERSION=$(VERSION) -t itemory-agent:check .

run: web
	ITEMORY_CACHE_DIR=./data ITEMORY_HTTP_ADDR=:8787 go run $(PKG)

docker:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t itemory-agent:$(VERSION) .
