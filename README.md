# Links

Links 是一个面向单用户、自托管场景的私密书签与轻量网页归档应用。

核心能力：

- Passkey-only 初始化、登录与 CLI 恢复
- Chromium Manifest V3 扩展一键保存
- `activeTab` 临时页面权限和 capture-only 扩展 Token
- 受资源限制的后台正文提取与 gzip Blob 归档
- SQLite FTS5、中文二元词和中英文混合检索
- 标签、稍后阅读、收藏与归档阅读模式
- JSON、CSV、浏览器书签 HTML 导出
- SQLite 与归档 Blob 完整备份及校验

完整分阶段记录见 [docs/roadmap.md](docs/roadmap.md)，VPS 部署与恢复说明见 [docs/deployment.md](docs/deployment.md)。

## 本地运行

Passkey 在 `localhost` 可以使用 HTTP；正式部署必须使用 HTTPS 和稳定域名。

```bash
export LINKS_PUBLIC_URL=http://localhost:8080
export LINKS_DATA_DIR=./data

go run -tags sqlite_fts5 ./cmd/links admin setup-link
go run -tags sqlite_fts5 ./cmd/links serve
```

打开 `admin setup-link` 输出的一次性地址，注册第一个 Passkey。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LINKS_ADDR` | `:8080` | HTTP 监听地址 |
| `LINKS_PUBLIC_URL` | `http://localhost:8080` | 用户实际访问应用的完整 Origin |
| `LINKS_RP_ID` | 从公开 URL 推导 | WebAuthn RP ID，部署后应保持稳定 |
| `LINKS_DATA_DIR` | `./data` | SQLite、归档 Blob 数据目录 |
| `LINKS_ALLOW_PRIVATE_FETCH` | `false` | 是否允许后台抓取私网/本机 URL |

不带 `sqlite_fts5` 构建标签时应用仍可运行，但会回退到基础 `LIKE` 检索。正式构建和 Docker 镜像默认启用 FTS5。

## 管理命令

```bash
# 首次初始化
go run -tags sqlite_fts5 ./cmd/links admin setup-link --ttl 10m

# Passkey 丢失后的恢复入口
go run -tags sqlite_fts5 ./cmd/links admin recovery-link --ttl 10m

# 创建完整备份，已存在的目标文件不会被覆盖
go run -tags sqlite_fts5 ./cmd/links admin backup --output links-backup.tar.gz

# 只读校验备份格式和 SQLite 快照
go run -tags sqlite_fts5 ./cmd/links admin verify-backup --input links-backup.tar.gz
```

## Chromium 扩展

开发阶段从 `chrome://extensions` 加载 [extension](extension) 目录。然后：

1. 在 Links 网页打开“设置 → 浏览器扩展”。
2. 生成 10 分钟有效的配对码。
3. 在扩展“连接设置”中填写 Links 地址和配对码。

扩展支持工具栏弹窗、`Command/Ctrl + Shift + S` 和右键菜单。默认只提交 URL、标题、canonical URL 和选中文字；只有明确勾选“归档当前正文”时才读取并上传当前页面正文。

## 测试与构建

```bash
go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 ./cmd/links

node --check extension/service-worker.js
node --check extension/popup.js
node --check extension/options.js
```

## Docker

```bash
export LINKS_PUBLIC_URL=https://links.example.com
docker compose up --build -d
docker compose exec links /app/links admin setup-link
```

反向代理负责 TLS。`LINKS_PUBLIC_URL`、实际访问 Origin 和 Passkey RP ID 必须保持一致。
