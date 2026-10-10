# VPS 部署与恢复

## 建议资源

单用户部署建议从 1 vCPU、512 MB～1 GB 内存和 5 GB 持久化磁盘开始。应用不运行无头浏览器，归档并发固定为 1；实际磁盘需求主要取决于正文数量和长度。

## Docker Compose

```bash
export PAGEGLEAN_PUBLIC_URL=https://pageglean.example.com
export PAGEGLEAN_OIDC_ISSUER=https://id.example.com
export PAGEGLEAN_OIDC_CLIENT_ID=pageglean
export PAGEGLEAN_OIDC_CLIENT_SECRET=your-client-secret
export PAGEGLEAN_OIDC_OWNER_SUBJECT=your-user-sub
export PAGEGLEAN_UID=$(id -u)
export PAGEGLEAN_GID=$(id -g)
mkdir -p data
docker compose up --build -d
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

在身份服务中创建服务端 OIDC 客户端，启用授权码流程和 PKCE S256，注册精确回调地址 `https://pageglean.example.com/api/auth/callback`。应用通过 issuer discovery 获取授权、令牌和 JWKS 地址，仅请求 `openid` scope。回调采用 GET（query response mode）；不支持 `form_post` 回调。

`PAGEGLEAN_OIDC_OWNER_SUBJECT` 是唯一允许访问私有资料的用户 `sub`，须从身份服务中获取该客户端下的实际值。不要使用邮箱或用户名，也不会把第一个登录者自动设为管理员。缺少任何 OIDC 配置或身份服务发现失败时，服务启动失败；CLI 备份和校验不要求 OIDC 配置。

账号恢复和 MFA 由身份服务负责。应用退出登录会撤销本地会话，身份服务的 SSO 会话仍然保留。更改 issuer、客户端 ID 或 owner subject 后，已有本地会话失效。更换域名时同步更新公开 URL 和客户端回调地址。客户端密钥只配置在服务端，不要提交到仓库。

## 网络抓取安全

默认 `PAGEGLEAN_ALLOW_PRIVATE_FETCH=false`，后台归档会拒绝回环、私网、链路本地和云元数据地址，以降低 SSRF 风险。

只有明确需要归档家庭局域网服务时才设置：

```bash
export PAGEGLEAN_ALLOW_PRIVATE_FETCH=true
```

开启后，已登录用户保存的链接可能促使服务器访问内网 URL，请仅在需要归档内网内容时启用。

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

然后把 `pageglean.db` 和 `blobs/` 放入新实例的 `PAGEGLEAN_DATA_DIR`，配置 OIDC 后启动。若使用不同域名，同步修改 `PAGEGLEAN_PUBLIC_URL` 和身份服务中的客户端回调地址。

## 升级

1. 先执行 `admin backup` 和 `admin verify-backup`。
2. 拉取新版本并重新构建镜像。
3. 启动时应用自动增加缺失列、数据表并重建 FTS5 索引。
4. 从 Passkey 版本升级时，配置上述 OIDC 环境变量并注册回调地址。书签、归档和用户数据保留；旧 Passkey 会话不再有效，`admin setup-link`、`admin recovery-link` 和 Passkey API 已移除。旧认证表保留在数据库中以兼容已有备份，但不再参与认证。
5. 检查 `/healthz` 和设置页中的书签/归档数量。

应用不会自动删除旧归档 Blob。后续如增加垃圾回收命令，应先保留经过校验的完整备份。

## 可选更新通知

默认 `PAGEGLEAN_WEBHOOK_MODE=generic`：设置 `PAGEGLEAN_WEBHOOK_URL` 和 `PAGEGLEAN_WEBHOOK_SECRET` 即可启用公开书签更新通知，两项均留空时关闭。密钥至少 32 字节。接收端需要令牌认证时，可额外设置 `PAGEGLEAN_WEBHOOK_TOKEN`，通知会附带 `Authorization: Bearer <token>` 请求头。

迁移到 Rivet 时设置 `PAGEGLEAN_WEBHOOK_MODE=rivet`，将 `PAGEGLEAN_WEBHOOK_URL` 设为完整触发地址（例如 `https://rivet.example/api/v1/repos/blog/triggers/rebuild`），并设置必需的 `PAGEGLEAN_WEBHOOK_TOKEN`；无需配置签名密钥。该模式发送空 JSON 对象和稳定的幂等键，网络错误或 503 使用同一键重试。配置更改后重启服务。通知状态不落库，重启不补发；如需同步，在设置页手动发送。完整协议及重启、token 轮换对幂等性的影响见[公开书签与更新通知](public-bookmarks.md)。
