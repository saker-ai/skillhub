.PHONY: build deps dev run setup clean help quickstart test ensure-static test-static lint frontend dev-frontend dev-backend

VITE_BASE_PATH ?= ./

# Build frontend (React + Vite)
frontend:
	cd web && pnpm install --prefer-offline && VITE_BASE_PATH="$(VITE_BASE_PATH)" npm run build

# Build the skillhub binary (includes embedded frontend)
build: frontend
	go build -o skillhub ./cmd/skillhub/

# Ensure the embed target exists for Go-only commands in a clean checkout.
ensure-static: test-static

# Build Go only (skip frontend)
build-go: ensure-static
	go build -o skillhub ./cmd/skillhub/

# Download Go dependencies
deps:
	go mod download

# One-click: build + create admin + start server (no Docker needed with SQLite)
# Usage: make quickstart ADMIN_USER=me ADMIN_PASSWORD=pass
quickstart: build
	@echo "Starting SkillHub (SQLite mode, no external dependencies)..."
	SKILLHUB_ADMIN_USER=$(ADMIN_USER) SKILLHUB_ADMIN_PASSWORD=$(ADMIN_PASSWORD) ./skillhub serve

# Start PostgreSQL via Docker (only needed for postgres mode)
pg-up:
	docker run -d --name skillhub-pg \
		-p 5432:5432 \
		-e POSTGRES_USER=skillhub \
		-e POSTGRES_PASSWORD=skillhub \
		-e POSTGRES_DB=skillhub \
		postgres:17-alpine 2>/dev/null || true
	@echo "Waiting for PostgreSQL..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		docker exec skillhub-pg pg_isready -U skillhub >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@echo "PostgreSQL ready."

# Stop PostgreSQL
pg-down:
	docker rm -f skillhub-pg 2>/dev/null || true

# Full setup: build + create admin user
# Usage: make setup ADMIN_USER=myname ADMIN_PASSWORD=mypass
ADMIN_USER ?= admin
ADMIN_PASSWORD ?= admin123
setup: build
	./skillhub admin create-user --handle $(ADMIN_USER) --role admin --password $(ADMIN_PASSWORD)
	@echo ""
	@echo "Setup complete!"
	@echo "  Start server:  make dev"
	@echo "  Web login:     http://localhost:10070/login"
	@echo "  Username:      $(ADMIN_USER)"

# Build frontend + Go binary, then start server
run: build
	./skillhub serve

# Start server in development mode
dev: build
	./skillhub serve

# Start frontend dev server (hot reload, proxies API to Go backend)
dev-frontend:
	cd web && npm run dev

# Start Go backend only (for development with frontend dev server)
dev-backend: build-go
	./skillhub serve

# Start everything via docker-compose
docker-up:
	docker compose -f deployments/docker/docker-compose.yml up --build -d

docker-down:
	docker compose -f deployments/docker/docker-compose.yml down

# Remove all data and rebuild
clean:
	docker rm -f skillhub-pg 2>/dev/null || true
	rm -f skillhub
	rm -rf data/
	rm -rf web/static/
	rm -rf ~/.skillhub/
	@echo "Cleaned."

# Run tests
#
# 排除 web/node_modules —— npm 包 flatted 顺手附带了一个 Go 实现
# (web/node_modules/flatted/golang/pkg/flatted/flatted.go),`go test ./...`
# 会把它当成项目包扫;过滤掉之后测试输出干净、跑得也快。
# 不能用 web/go.mod 隔离,因为父模块 import "github.com/saker-ai/skillhub/web"
# 取 embed.FS,加 nested go.mod 会切断这条 import。
test: test-static
	go test $$(go list ./... | grep -v /web/node_modules/)

# Create the smallest embedded frontend tree needed by Go tests when the
# real Vite build has not been generated yet. The files live under ignored
# web/static/ so production builds can freely replace them.
test-static:
	@mkdir -p web/static/assets web/static/swagger
	@printf '<!doctype html><title>SkillHub test shell</title><div id="root"></div>\n' > web/static/index.html
	@cp -f web/public/swagger-init.js web/static/swagger-init.js
	@printf '/* test placeholder for swagger-ui-dist */\n' > web/static/swagger/swagger-ui.css
	@printf 'window.SwaggerUIBundle=window.SwaggerUIBundle||function(){return {}};window.SwaggerUIBundle.presets={apis:[]};\n' > web/static/swagger/swagger-ui-bundle.js
	@printf '' > web/static/assets/.gitkeep

# Lint
#
# golangci-lint run 期待文件/目录路径(不接受全限定 import path),
# 所以用 `go list -f '{{.Dir}}'` 把包路径转成绝对目录,再过滤掉
# web/node_modules(同 test 目标的理由)。
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; \
	}
	golangci-lint run $$(go list -f '{{.Dir}}' ./... | grep -v /web/node_modules/)

# Show help
help:
	@echo "SkillHub Development"
	@echo ""
	@echo "Quick start (SQLite, no Docker needed):"
	@echo "  make quickstart ADMIN_USER=me ADMIN_PASSWORD=pass"
	@echo ""
	@echo "Or step by step:"
	@echo "  make setup ADMIN_USER=me ADMIN_PASSWORD=pass    # Build + create admin"
	@echo "  make dev                               # Start server on :10070"
	@echo ""
	@echo "Frontend development (hot reload):"
	@echo "  make dev-backend                       # Start Go backend"
	@echo "  make dev-frontend                      # Start Vite dev server (in another terminal)"
	@echo ""
	@echo "PostgreSQL mode:"
	@echo "  make pg-up                             # Start PostgreSQL via Docker"
	@echo "  SKILLHUB_DB_DRIVER=postgres SKILLHUB_DATABASE_URL='postgres://skillhub:skillhub@localhost:5432/skillhub?sslmode=disable' make dev"
	@echo ""
	@echo "Docker:"
	@echo "  make docker-up                         # Start everything via docker-compose"
	@echo "  make docker-down                       # Stop docker-compose services"
	@echo ""
	@echo "Targets:"
	@echo "  quickstart    - One-click: build + create admin + start server (SQLite)"
	@echo "  run           - Build frontend + Go binary, then start server"
	@echo "  build         - Build frontend + Go binary"
	@echo "  build-go      - Build Go binary only (skip frontend)"
	@echo "  frontend      - Build frontend only"
	@echo "  deps          - Download Go dependencies"
	@echo "  pg-up         - Start PostgreSQL via Docker (for postgres mode)"
	@echo "  pg-down       - Stop PostgreSQL"
	@echo "  setup         - Build + create admin user (ADMIN_USER, ADMIN_PASSWORD)"
	@echo "  dev           - Build all + start server"
	@echo "  dev-frontend  - Start Vite dev server (hot reload)"
	@echo "  dev-backend   - Build Go + start server"
	@echo "  test          - Run tests"
	@echo "  docker-up     - Start everything via docker-compose"
	@echo "  docker-down   - Stop docker-compose services"
	@echo "  clean         - Remove all data, binaries, and build artifacts"
	@echo "  help          - Show this help"
