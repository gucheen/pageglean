# 拾页 · PageGlean

拾页（PageGlean）是一个面向单用户、自托管场景的私密书签与轻量网页归档应用。

核心能力：

- Passkey-only 初始化、登录与 CLI 恢复
- 书签栏快捷方式与独立添加页面，预填网址、标题和选中文字
- 受资源限制的后台正文提取与 gzip Blob 归档
- SQLite FTS5、中文二元词和中英文混合检索
- 标签、稍后阅读、星标与归档阅读模式
- 默认私密、显式公开的书签与独立公开短评
- 最新六条公开 JSON 与可选的通用 Webhook 更新通知
- JSON、浏览器书签 HTML、CSV 预览导入与字段映射
- 多选书签后批量修改标签、阅读状态和收藏状态
- JSON、CSV、浏览器书签 HTML 导出
- SQLite 与归档 Blob 完整备份及校验

产品定位与取舍标准见 [docs/product-principles.md](docs/product-principles.md)，完整分阶段记录见 [docs/roadmap.md](docs/roadmap.md)，VPS 部署与恢复说明见 [docs/deployment.md](docs/deployment.md)。

## 本地运行

Passkey 在 `localhost` 可以使用 HTTP；正式部署必须使用 HTTPS 和稳定域名。

```bash
export CGO_ENABLED=0
export PAGEGLEAN_PUBLIC_URL=http://localhost:8080
export PAGEGLEAN_DATA_DIR=./data

go run ./cmd/pageglean admin setup-link
go run ./cmd/pageglean serve
```

打开 `admin setup-link` 输出的一次性地址，注册第一个 Passkey。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PAGEGLEAN_ADDR` | `:8080` | HTTP 监听地址 |
| `PAGEGLEAN_PUBLIC_URL` | `http://localhost:8080` | 用户实际访问应用的完整 Origin |
| `PAGEGLEAN_RP_ID` | 从公开 URL 推导 | WebAuthn RP ID，部署后应保持稳定 |
| `PAGEGLEAN_DATA_DIR` | `./data` | SQLite、归档 Blob 数据目录 |
| `PAGEGLEAN_ALLOW_PRIVATE_FETCH` | `false` | 是否允许后台抓取私网/本机 URL |

SQLite 由纯 Go 的 `modernc.org/sqlite` 驱动提供，无需启用 CGO；FTS5 全文检索默认可用。

## 管理命令

```bash
# 首次初始化
CGO_ENABLED=0 go run ./cmd/pageglean admin setup-link --ttl 10m

# Passkey 丢失后的恢复入口
CGO_ENABLED=0 go run ./cmd/pageglean admin recovery-link --ttl 10m

# 创建完整备份，已存在的目标文件不会被覆盖
CGO_ENABLED=0 go run ./cmd/pageglean admin backup --output pageglean-backup.tar.gz

# 只读校验备份格式和 SQLite 快照
CGO_ENABLED=0 go run ./cmd/pageglean admin verify-backup --input pageglean-backup.tar.gz
```

## 书签栏快捷方式

在“设置 → 书签栏快捷方式”中，把“保存到拾页”拖到浏览器书签栏。浏览网页时点击它，会打开独立的添加页面，预填网址、标题和选中文字（备注）。保存前可以修改网址、标题、备注、标签，以及稍后阅读、星标和正文归档选项。

未登录时先完成 Passkey 登录，预填内容会保留。归档选项由服务器抓取公开网页，不包含浏览器的登录状态。部分浏览器内置页面或受限制的网站无法运行书签脚本，可直接打开 `/add` 手动填写。

## 公开书签与更新通知

添加或编辑书签时，可勾选“公开展示”并填写独立的公开短评。网页描述来自原页面元数据，私人备注始终保持私密。`/public/bookmarks.json` 提供最新收藏的六条公开书签。

可通过 `PAGEGLEAN_WEBHOOK_URL` 和 `PAGEGLEAN_WEBHOOK_SECRET` 配置通用更新通知。通知合并和有限重试仅保存在内存中，重启不补发；需要时在设置里手动发送。字段规则、签名格式和接收方约定见 [公开书签与更新通知](docs/public-bookmarks.md)。

## 导入与批量整理

在网页“设置 → 数据与存储 → 导入书签”中选择文件。支持拾页导出的 JSON、Chromium/Firefox 等浏览器导出的 Netscape HTML，以及带表头的 CSV；文本编码支持 UTF-8 和中文环境常见的 GB18030。CSV 可在预览阶段映射网址、标题、备注、标签、阅读状态、收藏状态和创建时间。

单次导入限制为 10 MB、20,000 条。系统会规范化 URL、跳过无效记录并合并重复项。为避免低配 VPS 突然产生大量抓取任务，导入默认只保存链接；需要时可在导入窗口明确启用正文归档。

书签列表左侧复选框用于多选。选中后可以批量添加或移除标签、修改稍后阅读/收藏状态，或经二次确认后删除。

## 测试与构建

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build ./cmd/pageglean

node --check internal/webui/assets/app.js
```

## Docker

预构建的 `linux/amd64` 镜像发布在 GHCR：

```bash
docker pull ghcr.io/gucheen/pageglean:latest
```

`main` 分支会发布 `latest`、`main` 和提交 SHA 标签；`v*` Git 标签会额外发布对应的语义化版本标签。

```bash
export PAGEGLEAN_PUBLIC_URL=https://pageglean.example.com
export PAGEGLEAN_UID=$(id -u)
export PAGEGLEAN_GID=$(id -g)
mkdir -p data
docker compose up --build -d
docker compose exec pageglean /app/pageglean admin setup-link
```

镜像默认以非 root 用户 `10001:10001` 运行；Compose 会通过 `PAGEGLEAN_UID` 和 `PAGEGLEAN_GID` 对齐宿主机 `data/` 目录的所有者。反向代理负责 TLS。`PAGEGLEAN_PUBLIC_URL`、实际访问 Origin 和 Passkey RP ID 必须保持一致。
