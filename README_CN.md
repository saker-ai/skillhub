# SkillHub

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cinience/skillhub)](https://goreportcard.com/report/github.com/cinience/skillhub)

自托管的 Agent 技能与插件注册中心，用于发布、版本管理与分发 agent 技能和插件 bundle。支持 Web UI、REST API 和 CLI 客户端，兼容 [ClawHub](https://github.com/openclaw/clawhub) 协议。适合企业内部构建自己的 agent 技能和插件注册中心。

[English](README.md) | [中文](README_CN.md)

## 为什么选择 SkillHub？

- **数据自主** — 部署在你自己的基础设施上，无厂商锁定，无外部依赖
- **单二进制** — 一个 Go 二进制文件同时提供注册中心、Web UI 和 CLI，随处部署
- **零依赖模式** — 默认使用 SQLite，无需任何外部服务。生产环境可选 PostgreSQL
- **Git 原生版本管理** — 每个技能版本对应一个 Git 提交，内置完整历史、差异对比和回滚能力
- **即时搜索** — 内嵌 [Bleve](https://blevesearch.com/) 全文搜索引擎，覆盖名称、摘要和标签
- **ClawHub 兼容** — 实现 ClawHub 注册中心协议，发布到 SkillHub 的技能可被任何 ClawHub 兼容客户端使用
- **Webhook 导入** — 推送到 GitHub/GitLab/Gitea 后自动导入并发布技能
- **认证与权限** — bcrypt 密码认证、作用域 API Token、基于角色的访问控制（admin / moderator / user）

## 快速开始

### 单二进制部署（最简方式）

前置条件：Go 1.25+、Node.js 22+

```bash
git clone https://github.com/cinience/skillhub.git
cd skillhub
make quickstart ADMIN_USER=admin ADMIN_PASSWORD=admin123
```

自动编译（前端 + 后端）、创建管理员并启动服务，使用 SQLite — 无需 Docker 或外部数据库。

浏览器打开 http://localhost:10070。

### Docker Compose（生产部署）

```bash
git clone https://github.com/cinience/skillhub.git
cd skillhub
SKILLHUB_ADMIN_PASSWORD=secret make docker-up
```

一条命令启动 PostgreSQL 和 SkillHub。首次启动自动创建管理员账号。

浏览器打开 http://localhost:10070。

### 分步执行

```bash
make setup ADMIN_USER=admin ADMIN_PASSWORD=admin123  # 编译 + 创建管理员
make dev                                             # 启动服务 :10070
```

### PostgreSQL 模式（可选）

默认使用 SQLite。如需使用 PostgreSQL：

```bash
make pg-up   # 通过 Docker 启动 PostgreSQL
SKILLHUB_DB_DRIVER=postgres \
SKILLHUB_DATABASE_URL='postgres://skillhub:skillhub@localhost:5432/skillhub?sslmode=disable' \
make dev
```

服务启动时自动执行数据库迁移并创建管理员。后续重启幂等，不会重复创建。

## CLI 使用

`skillhub` 二进制文件同时是服务端和客户端。

```bash
# 认证
skillhub login                              # 交互式登录（输入 registry URL + API Token）
skillhub whoami                             # 查看当前用户

# 发现技能
skillhub search "浏览器自动化"                # 全文搜索
skillhub list --sort downloads              # 浏览注册中心
skillhub inspect agent-browser              # 技能详情 + 版本历史

# 安装和管理
skillhub install agent-browser              # 安装最新版本
skillhub install agent-browser --version 2.0.0
skillhub installed                          # 查看本地已安装
skillhub update --all                       # 更新所有已安装技能
skillhub uninstall agent-browser            # 卸载

# 发布
skillhub publish ./my-skill \
  --slug my-skill --version 1.0.0 \
  --tags "coding,automation" \
  --summary "一个实用的编码技能"

# 管理员
skillhub admin create-user --handle alice --role admin --password secret
skillhub admin create-token --user alice --label "CI"
skillhub admin set-password --user alice --password newpass
```

技能默认安装到 `~/.skillhub/skills/`，可通过 `~/.skillhub/config.yaml` 中的 `skills_dir` 自定义。

## 插件 (Plugins)

Plugin 将多个 skill、MCP server 和 hook 打包为单一可部署单元。SkillHub 同时作为插件注册中心 — 一次发布，随处加载。

### 插件清单 (`plugin.json`)

```json
{
  "name": "my-plugin",
  "version": "1.0.0",
  "description": "生产力工具插件包",
  "skills": ["skills/"],
  "mcp_servers": {
    "code-tools": {
      "type": "sse",
      "url": "http://localhost:9090/sse",
      "timeout_seconds": 30
    }
  },
  "hooks": {
    "pre_tool_use": [
      {"matcher": "bash", "hooks": [{"command": "echo pre-check"}]}
    ]
  }
}
```

### 发布插件

```bash
skillhub plugin publish ./my-plugin \
  --slug my-plugin --version 1.0.0 \
  --summary "生产力工具包"
```

### 插件 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/plugins` | 发布插件（multipart: plugin.json + 文件） |
| `GET` | `/api/v1/plugins` | 列出所有插件 |
| `GET` | `/api/v1/plugins/:slug` | 获取插件元信息 |
| `GET` | `/api/v1/plugins/:slug/versions` | 列出插件版本 |
| `GET` | `/api/v1/plugins/:slug/file?version=X&path=Y` | 获取插件中的文件 |
| `GET` | `/api/v1/plugins/:slug/download?version=X` | 下载插件包 |

### 在 Saker 中加载插件

**全局加载（运行时启动时）：**
```go
api.Options{
    RemotePluginSources: []plugin.RemotePluginSource{{
        Registry: "http://skillhub:10070",
        Slugs:    []string{"my-plugin"},
    }},
}
```

**Session 级别加载（通过 API 按请求）：**
```json
{
  "extra_body": {
    "plugin_uri": "plugin://skillhub:10070?slugs=my-plugin"
  }
}
```

加载后插件的组件被分解注入：
- **Skills** → 注册到 session skill 注册表
- **MCP Servers** → 建立连接并注册为可用工具
- **Hooks** → 在 run 生命周期内与全局 hooks 并行执行

Session 加载的插件按 thread 缓存（TTL 10 分钟），支持跨 turn 复用。

## 架构

```mermaid
graph LR
    subgraph 客户端
        W["Web UI"]
        C["CLI"]
        A["AI Agent"]
    end

    subgraph SkillHub["SkillHub 服务端"]
        direction TB
        R["路由 & 中间件<br/>认证 · 限流 · 日志"]
        S["服务层<br/>Skill · Auth · Import"]
        R --> S
    end

    subgraph 存储
        G[("Git<br/>仓库")]
        B[("Bleve<br/>搜索")]
        D[("SQLite /<br/>Postgres")]
    end

    W & C & A --> R
    S --> G & B & D
```

## 项目结构

```
skillhub/
├── cmd/skillhub/           # 入口 — 服务端 + CLI 路由
│   └── main.go
├── pkg/
│   ├── auth/                # bcrypt 密码、HMAC API Token、会话管理
│   ├── cli/                 # CLI 客户端（配置、HTTP 客户端、命令、输出格式化）
│   ├── config/              # YAML 配置 + 环境变量覆盖
│   ├── gitstore/            # 裸 Git 仓库存储、镜像推送、Webhook 导入
│   ├── handler/             # HTTP 处理器（技能、认证、搜索、管理、Web UI）
│   ├── middleware/          # 认证、限流、请求 ID、日志
│   ├── model/               # 领域模型（User、Skill、Version、Token、Star）
│   ├── repository/          # 数据库访问层（GORM）
│   ├── search/              # Bleve 全文搜索集成
│   ├── server/              # 服务启动、路由注册、自动初始化
│   └── service/             # 业务逻辑（发布、下载、版本管理）
├── configs/                 # 默认配置文件（skillhub.yaml）
├── web/                     # React 前端（Vite + TypeScript + i18n）
├── web/templates/           # 服务端渲染 HTML 备用方案（Go 模板）
├── deployments/docker/      # Dockerfile + docker-compose.yml
├── Makefile
└── go.mod
```

## 配置

配置从 `configs/skillhub.yaml` 加载，支持环境变量覆盖：

| 变量 | 说明 | 默认值 |
|---|---|---|
| `SKILLHUB_PORT` | 服务端口 | `10070` |
| `SKILLHUB_HOST` | 绑定地址 | `0.0.0.0` |
| `SKILLHUB_BASE_URL` | 公开访问 URL | `http://localhost:10070` |
| `SKILLHUB_DB_DRIVER` | 数据库驱动 | `sqlite` |
| `SKILLHUB_DATABASE_URL` | 数据库连接串 | `./data/skillhub.db` |
| `SKILLHUB_GIT_PATH` | Git 仓库存储路径 | `./data/repos` |
| `SKILLHUB_ADMIN_USER` | 启动时自动创建管理员用户名 | _（空）_ |
| `SKILLHUB_ADMIN_PASSWORD` | 管理员密码 | _（空）_ |
| `SKILLHUB_CONFIG` | 配置文件路径 | `configs/skillhub.yaml` |

### CLI 客户端配置

CLI 客户端配置存储在 `~/.skillhub/config.yaml`：

```yaml
registry: http://localhost:10070  # 注册中心地址
token: clh_xxxxxxxxxxxx           # API Token（通过 skillhub login 设置）
skills_dir: ~/.skillhub/skills   # 技能安装目录（可选）
```

| 字段 | 说明 | 默认值 |
|---|---|---|
| `registry` | 注册中心地址 | `http://localhost:10070` |
| `token` | API Token，用于认证 | _（通过 `skillhub login` 设置）_ |
| `skills_dir` | 本地技能安装目录 | `~/.skillhub/skills` |

## API 接口

### 公开接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/skills` | 技能列表 |
| `GET` | `/api/v1/skills/:slug` | 技能详情 |
| `GET` | `/api/v1/skills/:slug/versions` | 版本列表 |
| `GET` | `/api/v1/skills/:slug/versions/:version` | 获取特定版本 |
| `GET` | `/api/v1/skills/:slug/file` | 获取技能文件内容 |
| `GET` | `/api/v1/search?q=...` | 全文搜索 |
| `GET` | `/api/v1/download?slug=...&version=...` | 下载技能 ZIP |
| `GET` | `/api/v1/resolve` | 解析技能版本 |
| `GET` | `/healthz` | 存活检查 |
| `GET` | `/readyz` | 就绪检查 |
| `GET` | `/.well-known/clawhub.json` | ClawHub 协议发现 |

### 认证接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/whoami` | 当前用户信息 |
| `POST` | `/api/v1/skills` | 发布技能 |
| `DELETE` | `/api/v1/skills/:slug` | 软删除技能 |
| `POST` | `/api/v1/skills/:slug/undelete` | 恢复已删除技能 |
| `POST` | `/api/v1/stars/:slug` | 收藏技能 |
| `DELETE` | `/api/v1/stars/:slug` | 取消收藏 |

### 管理员接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/users` | 用户列表 |
| `POST` | `/api/v1/users` | 创建用户 |
| `POST` | `/api/v1/tokens` | 创建 API Token |
| `POST` | `/api/v1/users/ban` | 封禁/解封用户 |
| `POST` | `/api/v1/users/role` | 设置用户角色 |

### 插件接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/plugins` | 发布插件 |
| `GET` | `/api/v1/plugins` | 列出所有插件 |
| `GET` | `/api/v1/plugins/:slug` | 获取插件元信息 |
| `GET` | `/api/v1/plugins/:slug/versions` | 列出版本 |
| `GET` | `/api/v1/plugins/:slug/file` | 获取插件文件内容 |
| `GET` | `/api/v1/plugins/:slug/download` | 下载插件包 |

### Webhook 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/webhooks/github` | GitHub 推送回调 |
| `POST` | `/api/v1/webhooks/gitlab` | GitLab 推送回调 |
| `POST` | `/api/v1/webhooks/gitea` | Gitea 推送回调 |

## 技术栈

| 组件 | 技术 |
|---|---|
| 语言 | Go 1.25 |
| Web 框架 | [Gin](https://github.com/gin-gonic/gin) |
| 前端 | React 19 + Vite + TypeScript |
| 数据库 | SQLite（默认）/ PostgreSQL 17，via [GORM](https://gorm.io/) |
| 搜索引擎 | [Bleve](https://blevesearch.com/)（内嵌全文搜索）|
| Git 存储 | [go-git](https://github.com/go-git/go-git) |
| 认证 | bcrypt + HMAC Token |

## 贡献

请参阅 [CONTRIBUTING.md](CONTRIBUTING.md) 了解开发环境设置、编码规范和 PR 流程。

## 安全

如需报告安全漏洞，请参阅 [SECURITY.md](SECURITY.md)。

## 许可证

[MIT](LICENSE)
