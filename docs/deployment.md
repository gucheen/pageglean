# VPS 部署与恢复

## 建议资源

单用户部署建议从 1 vCPU、512 MB～1 GB 内存和 5 GB 持久化磁盘开始。应用不运行无头浏览器，归档并发固定为 1；实际磁盘需求主要取决于正文数量和长度。

## Docker Compose

```bash
export PAGEGLEAN_PUBLIC_URL=https://pageglean.example.com
export PAGEGLEAN_UID=$(id -u)
export PAGEGLEAN_GID=$(id -g)
mkdir -p data
docker compose up --build -d
docker compose exec pageglean /app/pageglean admin setup-link
```

镜像默认以非 root 用户 `10001:10001` 运行。Compose 使用 `PAGEGLEAN_UID` 和 `PAGEGLEAN_GID` 让进程匹配宿主机部署用户，因此 `data/` 应由该用户创建和管理。

`data/` 是唯一需要持久化和备份的目录，不要把它放在临时文件系统中。如需挂载其他宿主机目录，必须确保上面设置的 UID/GID 可以写入该目录，例如：

```bash
sudo install -d -o "$PAGEGLEAN_UID" -g "$PAGEGLEAN_GID" -m 0750 /srv/pageglean/data
```

然后把 Compose 中的卷挂载改为 `/srv/pageglean/data:/data`。

## HTTPS 反向代理

Caddy 示例：

```caddyfile
pageglean.example.com {
    reverse_proxy 127.0.0.1:8080
    encode zstd gzip
}
```

Passkey 与域名绑定。正式初始化后不要随意修改 `PAGEGLEAN_PUBLIC_URL` 或 `PAGEGLEAN_RP_ID`；必须迁移域名时，先在 VPS 上准备好 `admin recovery-link` 能力。

## 网络抓取安全

默认 `PAGEGLEAN_ALLOW_PRIVATE_FETCH=false`，后台归档会拒绝回环、私网、链路本地和云元数据地址，以降低 SSRF 风险。

只有明确需要归档家庭局域网服务时才设置：

```bash
export PAGEGLEAN_ALLOW_PRIVATE_FETCH=true
```

开启后，任何能创建书签的扩展 Token 都可能促使服务器访问内网 URL，因此应及时撤销丢失设备。

## 备份

在线创建一致性 SQLite 快照，并和压缩正文一起写入 tar.gz：

```bash
docker compose exec pageglean /app/pageglean admin backup --output /data/pageglean-backup.tar.gz
docker compose exec pageglean /app/pageglean admin verify-backup --input /data/pageglean-backup.tar.gz
```

建议将备份文件复制到 VPS 之外。Web UI 的 JSON/CSV/HTML 导出适合数据迁移，但不包含压缩正文；CLI 备份才是完整灾难恢复包。

## 恢复演练

恢复会替换实例数据，因此先在一个全新的空目录中演练：

```bash
mkdir pageglean-restore-test
tar -xzf pageglean-backup.tar.gz -C pageglean-restore-test
```

确认目录内至少包含：

```text
manifest.json
pageglean.db
blobs/
```

然后把 `pageglean.db` 和 `blobs/` 放入新实例的 `PAGEGLEAN_DATA_DIR`，使用与原实例相同的公开域名/RP ID 启动。若使用不同域名，已有 Passkey 不适用，需要在新实例通过 CLI 创建恢复链接并注册新 Passkey。

## 升级

1. 先执行 `admin backup` 和 `admin verify-backup`。
2. 拉取新版本并重新构建镜像。
3. 启动时应用自动增加缺失列、数据表并重建 FTS5 索引。
4. 检查 `/healthz` 和设置页中的书签/归档数量。

应用不会自动删除旧归档 Blob。后续如增加垃圾回收命令，应先保留经过校验的完整备份。
