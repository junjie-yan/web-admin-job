# Custom configuration | 独立配置
# Service name | 项目名称
SERVICE=Job
# Service name in specific style | 项目经过style格式化的名称
SERVICE_STYLE=job
# Service name in lowercase | 项目名称全小写格式
SERVICE_LOWER=job
# Service name in snake format | 项目名称下划线格式
SERVICE_SNAKE=job
# Service name in snake format | 项目名称短杠格式
SERVICE_DASH=job

# The project version, if you don't use git, you should set it manually | 项目版本，如果不使用git请手动设置
VERSION=$(shell git describe --tags --always)

# The project file name style | 项目文件命名风格
PROJECT_STYLE=go_zero

# Whether to use i18n | 是否启用 i18n
PROJECT_I18N=true

# The suffix after build or compile | 构建后缀
PROJECT_BUILD_SUFFIX=rpc


# Ent enabled features | Ent 启用的官方特性
ENT_FEATURE=sql/execquery


# The arch of the build | 构建的架构
GOARCH=amd64

# ---- You may not need to modify the codes below | 下面的代码大概率不需要更改 ----

GO ?= go
GOFMT ?= gofmt "-s"
# 排除隐藏目录（.git/.gomodcache 等），避免 gofmt 扫到非项目源码
GOFILES := $(shell find . \( -path './.*' \) -prune -o -name '*.go' -print)
LDFLAGS := -s -w

.PHONY: test
test: # Run test for the project | 运行项目测试（当前无 _test.go，等效全包编译门禁）
	go test -v --cover ./...

.PHONY: fmt
fmt: # Format the codes | 格式化代码
	$(GOFMT) -w $(GOFILES)

.PHONY: lint
lint: # Run go linter | 运行代码错误分析（linter 配置见 .golangci.yml）
	golangci-lint run

.PHONY: tools
tools: # Install the necessary tools | 安装必要的工具
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: gen-rpc
gen-rpc: # Generate RPC files from proto | 生成 RPC 的代码
	goctls rpc protoc ./$(SERVICE_STYLE).proto --go_out=./types --go-grpc_out=./types --zrpc_out=. --style=$(PROJECT_STYLE)
ifeq ($(shell uname -s), Darwin)
	sed -i "" 's/,omitempty//g' ./types/$(SERVICE_LOWER)/*.pb.go
else
	sed -i 's/,omitempty//g' ./types/$(SERVICE_LOWER)/*.pb.go
endif
	@echo "Generate RPC codes successfully"

.PHONY: gen-ent
gen-ent: # Generate Ent codes | 生成 Ent 的代码
	go run -mod=mod entgo.io/ent/cmd/ent generate --template glob="./ent/template/*.tmpl" ./ent/schema --feature $(ENT_FEATURE)
	@echo "Generate Ent codes successfully"

.PHONY: gen-rpc-ent-logic
gen-rpc-ent-logic: # Generate logic code from Ent, need model and group params | 根据 Ent 生成逻辑代码, 需要设置 model 和 group
	@mkdir -p ./desc && goctls rpc ent --schema=./ent/schema  --style=$(PROJECT_STYLE) --multiple=false --service_name=$(SERVICE) --search_key_num=3 --output=./ --model=$(model) --group=$(group) --proto_out=./desc/$(shell echo $(model) | tr A-Z a-z).proto --i18n=$(PROJECT_I18N) --overwrite=true
	@echo "Generate logic codes from Ent successfully (desc/ 下为生成暂存文件，已 gitignore，请手动合并 RPC 定义到 job.proto)"

.PHONY: build-win
build-win: # Build project for Windows | 构建Windows下的可执行文件
	env CGO_ENABLED=0 GOOS=windows GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -trimpath -o $(SERVICE_STYLE)_$(PROJECT_BUILD_SUFFIX).exe $(SERVICE_STYLE).go
	@echo "Build project for Windows successfully"

.PHONY: build-mac
build-mac: # Build project for MacOS | 构建MacOS下的可执行文件
	env CGO_ENABLED=0 GOOS=darwin GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -trimpath -o $(SERVICE_STYLE)_$(PROJECT_BUILD_SUFFIX) $(SERVICE_STYLE).go
	@echo "Build project for MacOS successfully"

.PHONY: build-linux
build-linux: # Build project for Linux | 构建Linux下的可执行文件
	env CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -trimpath -o $(SERVICE_STYLE)_$(PROJECT_BUILD_SUFFIX) $(SERVICE_STYLE).go
	@echo "Build project for Linux successfully"

.PHONY: help
help: # Show help | 显示帮助
	@grep -E '^[a-zA-Z0-9 -]+:.*#'  Makefile | sort | while read -r l; do printf "\033[1;32m$$(echo $$l | cut -f 1 -d':')\033[00m:$$(echo $$l | cut -f 2- -d'#')\n"; done
