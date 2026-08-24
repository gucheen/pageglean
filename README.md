# 拾页 · PageGlean

拾页（PageGlean）是一个面向单用户、自托管场景的私密书签与轻量网页归档应用。

核心能力：

- Passkey-only 初始化、登录与 CLI 恢复
- Chromium Manifest V3 扩展一键保存
- `activeTab` 临时页面权限和 capture-only 扩展 Token
- 受资源限制的后台正文提取与 gzip Blob 归档
- SQLite FTS5、中文二元词和中英文混合检索
- 标签、稍后阅读、收藏与归档阅读模式
- JSON、浏览器书签 HTML、CSV 预览导入与字段映射
- 多选书签后批量修改标签、阅读状态和收藏状态
- JSON、CSV、浏览器书签 HTML 导出
- SQLite 与归档 Blob 完整备份及校验

产品定位与取舍标准见 [docs/product-principles.md](docs/product-principles.md)，完整分阶段记录见 [docs/roadmap.md](docs/roadmap.md)，VPS 部署与恢复说明见 [docs/deployment.md](docs/deployment.md)。

## 本地运行

Passkey 在 `localhost` 可以使用 HTTP；正式部署必须使用 HTTPS 和稳定域名。

```bash
export PAGEGLEAN_PUBLIC_URL=http://localhost:8080
export PAGEGLEAN_DATA_DIR=./data

go run -tags sqlite_fts5 ./cmd/pageglean admin setup-link
go run -tags sqlite_fts5 ./cmd/pageglean serve
```

打开 `admin setup-link` 输出的一次性地址，注册第一个 Passkey。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PAGEGLEAN_ADDR` | `:8080` | HTTP 监听地址 |
| `PAGEGLEAN_PUBLIC_URL` | `http://localhost:8080` | 用户实际访问应用的完整 Origin |
| `PAGEGLEAN_RP_ID` | 从公开 URL 推导 | WebAuthn RP ID，部署后应保持稳定 |
| `PAGEGLEAN_DATA_DIR` | `./data` | SQLite、归档 Blob 数据目录 |
| `PAGEGLEAN_ALLOW_PRIVATE_FETCH` | `false` | 是否允许后台抓取私网/本机 URL |

不带 `sqlite_fts5` 构建标签时应用仍可运行，但会回退到基础 `LIKE` 检索。正式构建和 Docker 镜像默认启用 FTS5。

## 管理命令

```bash
# 首次初始化
go run -tags sqlite_fts5 ./cmd/pageglean admin setup-link --ttl 10m

# Passkey 丢失后的恢复入口
go run -tags sqlite_fts5 ./cmd/pageglean admin recovery-link --ttl 10m

# 创建完整备份，已存在的目标文件不会被覆盖
go run -tags sqlite_fts5 ./cmd/pageglean admin backup --output pageglean-backup.tar.gz

# 只读校验备份格式和 SQLite 快照
go run -tags sqlite_fts5 ./cmd/pageglean admin verify-backup --input pageglean-backup.tar.gz
```

## Chromium 扩展

开发阶段从 `chrome://extensions` 加载 [extension](extension) 目录。然后：

1. 在拾页网页打开“设置 → 浏览器扩展”。
2. 生成 10 分钟有效的配对码。
3. 在扩展“连接设置”中填写拾页地址和配对码。

扩展支持工具栏弹窗、`Command/Ctrl + Shift + S` 和右键菜单。默认只提交 URL、标题、canonical URL 和选中文字；只有明确勾选“归档当前正文”时才读取并上传当前页面正文。

## 导入与批量整理

在网页“设置 → 数据与存储 → 导入书签”中选择文件。支持拾页导出的 JSON、Chromium/Firefox 等浏览器导出的 Netscape HTML，以及带表头的 CSV；文本编码支持 UTF-8 和中文环境常见的 GB18030。CSV 可在预览阶段映射网址、标题、备注、标签、阅读状态、收藏状态和创建时间。

单次导入限制为 10 MB、20,000 条。系统会规范化 URL、跳过无效记录并合并重复项。为避免低配 VPS 突然产生大量抓取任务，导入默认只保存链接；需要时可在导入窗口明确启用正文归档。

书签列表左侧复选框用于多选。选中后可以批量添加或移除标签、修改稍后阅读/收藏状态，或经二次确认后删除。

## 测试与构建

```bash
go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
go build -tags sqlite_fts5 ./cmd/pageglean

node --check extension/service-worker.js
node --check extension/popup.js
node --check extension/options.js
```

## Docker

```bash
export PAGEGLEAN_PUBLIC_URL=https://pageglean.example.com
export PAGEGLEAN_UID=$(id -u)
export PAGEGLEAN_GID=$(id -g)
mkdir -p data
docker compose up --build -d
docker compose exec pageglean /app/pageglean admin setup-link
```

镜像默认以非 root 用户 `10001:10001` 运行；Compose 会通过 `PAGEGLEAN_UID` 和 `PAGEGLEAN_GID` 对齐宿主机 `data/` 目录的所有者。反向代理负责 TLS。`PAGEGLEAN_PUBLIC_URL`、实际访问 Origin 和 Passkey RP ID 必须保持一致。
