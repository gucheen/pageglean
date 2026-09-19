# 公开书签与更新通知

## 字段与可见性

- `description`：原网页自己的描述，来源于 `<meta name="description">`，缺失时读取 `og:description`。书签栏快捷方式可以带入它，服务端归档也会读取它；没有元数据时保持为空，不用正文摘要或私人备注替代。
- `note`：私人备注，只有登录后的管理接口、私人导出和完整备份包含它。
- `publicComment`：自己写的公开短评，最多 1000 个字符；只有书签设为公开时才对外展示。
- `public`：是否允许公开展示，与星标和稍后阅读独立。新书签和升级前已有书签默认私密。

添加和编辑表单可以设置公开状态；列表中“公开”用于筛选，条目也会标明公开状态。公开短评可以保留在私密书签中，取消公开不会删除短评。

重复保存遵循已有的合并规则，不改变原书签的公开状态和短评；更改这些字段请编辑原书签。JSON 和 CSV 导出保留各字段，导入的新书签始终私密，但保留网页描述、私人备注和公开短评。完整备份恢复保留原来的公开状态。浏览器书签 HTML 只承载其支持的链接、标题、标签和备注。

## 公开页面

`GET /public/` 无需登录，可浏览全部公开书签，每页 30 条，通过 `?page=2` 翻页。按收藏时间倒序排列，同一时间按内部 ID 倒序。页面由服务端直接生成，不依赖 JavaScript；展示标题、网址域名、收藏日期、网页描述和公开短评，不包含私人备注、标签或正文归档。登录页和设置页均提供入口，可直接分享此页面地址。

页面不缓存，取消公开或删除后，下次访问或刷新即不再展示。尚未公开任何书签时显示空状态。

## 公开接口

`GET /public/bookmarks.json` 无需登录，固定返回最多六条公开书签，按收藏时间倒序排列，同一时间按内部 ID 倒序。编辑标题、短评或公开状态不会改变收藏时间。

```json
{
  "version": 1,
  "revision": "sha256-content-hash",
  "bookmarks": [
    {
      "url": "https://example.com/article",
      "title": "文章标题",
      "description": "原网页描述",
      "publicComment": "值得分享的一点看法",
      "savedAt": "2026-09-16T08:00:00Z"
    }
  ]
}
```

公开接口采用独立字段清单，不包含私人备注、标签、星标、阅读状态、正文归档、错误信息或内部 ID。字段中的文字是普通文本，接收方在生成 HTML 时应正常转义，不能当作可信 HTML 插入。

没有公开书签时 `bookmarks` 为 `[]`。接口提供 ETag，支持 `If-None-Match` 和 `304 Not Modified`；`Cache-Control: no-cache` 要求缓存重新验证。`revision` 只由实际返回的内容计算，私人字段或前六条之外的变化不会改变它。

取消公开或删除后接口立即更新，并用下一条公开书签补齐。外部服务保存过的副本需要由接收方自行更新。

## Rivet 构建触发 Webhook

迁移到 Rivet 时配置：

```sh
PAGEGLEAN_WEBHOOK_MODE=rivet
PAGEGLEAN_WEBHOOK_URL=https://rivet.example/api/v1/repos/blog/triggers/rebuild
PAGEGLEAN_WEBHOOK_TOKEN=your-rivet-token
```

URL 替换为 TLS 反向代理上的完整触发地址，token 仅放在认证请求头中，URL 不允许包含查询参数或凭据。代理需要保留 `Authorization` 和 `Idempotency-Key`，避免记录其值。此模式不需要 `PAGEGLEAN_WEBHOOK_SECRET`，已有的签名密钥会被忽略。URL 留空时关闭通知。Compose 已透传模式、URL 和 token。

每次发送 `POST`，请求体固定为两个字节 `{}`，`Content-Type: application/json`，附带 `Authorization: Bearer <token>` 和 `Idempotency-Key: <事件 ID>`。不发送旧协议的事件字段或签名头，也不发送 command、ref、revision、环境变量或 secret。仓库默认分支与 pipeline 由接收端配置；构建流程需要自行从 PageGlean 的 `/public/bookmarks.json` 拉取数据。

沿用下文的启动不补发、30 秒合并、10 秒请求超时和最多五次尝试，但响应与重试规则如下：

- 仅 HTTP 200 / 201 表示触发送达，包括接收端返回原 Run 的幂等响应；不表示构建成功或完成。
- 网络错误或 HTTP 503 自动重试，始终复用同一事件 ID。其他状态停止自动重试，设置页显示 HTTP 错误码，不显示响应内部详情。
- 在途、待发送或失败的同一内容版本，点击“发送更新通知”仍使用原 ID；修复 422 对应的默认分支或配置后，可以这样重试。收到成功响应后再次手动发送，或公开内容版本变化，才生成新 ID。
- ID 和通知状态仍仅保存在内存中。重启不补发，重启后的手动发送会生成新 ID；若上次响应丢失但接收端已创建任务，可能创建另一任务。轮换 token 后，接收端也会将投递视为新请求。

`PAGEGLEAN_WEBHOOK_MODE` 默认是 `generic`，以下为保留的原协议。切换模式和部署配置后需要重启服务，可在设置页手动发送一次验证接收端。

## 通用 Webhook（generic 模式）

Webhook 只通知“公开接口内容已变化”，不调用 Git、构建平台或部署工具。接收方可以据此刷新页面、同步数据、发送消息或执行自己的流程。

在部署环境配置一个接收目标：

```sh
PAGEGLEAN_WEBHOOK_URL=https://receiver.example/events/pageglean
PAGEGLEAN_WEBHOOK_SECRET=replace-with-a-random-secret-of-at-least-32-bytes
# 可选：接收端要求的认证令牌
PAGEGLEAN_WEBHOOK_TOKEN=your-receiver-token
```

两项同时留空时关闭自动通知，公开接口仍可独立使用。密钥至少 32 字节，接收方使用相同密钥验签。Compose 已透传这两个变量。默认使用 HTTPS；如果接收端位于可信内网，可以明确配置 HTTP。配置由管理员控制，不会通过网页接口返回 URL 或密钥，发送时也不跟随 HTTP 重定向。

`PAGEGLEAN_WEBHOOK_TOKEN` 可选，配置后每次通知（包括重试和手动发送）都会附带 `Authorization: Bearer <token>` 请求头，其中 `<token>` 为配置的令牌原文。留空时不发送此请求头。令牌仅接受可见 ASCII 字符，不含空格或换行；它用于接收端认证，与 HMAC 签名密钥独立，不替代现有签名。令牌不会通过状态接口或错误信息返回。

### 时序与重试

- 每两秒检查一次最新六条的内容版本，仅在配置了 Webhook 时启动检查。
- 服务启动时只记录当前版本，不发送通知。停机期间发生的变化不会在重启时补发。
- 内容变化后等待 30 秒再发送，期间有新变化会合并并重新计时。
- 私人备注、标签、星标等未公开字段的变化不触发通知；归档更新了公开条目的标题或网页描述，则会触发。
- 每次请求最多等待 10 秒；HTTP 2xx 表示通知送达，其他状态或网络错误视为失败。
- 最多尝试五次，失败后的等待依次为 30、60、120、240 秒。
- 重试使用相同事件 ID；新的内容变化或手动发送使用新的 ID。
- 通知状态、合并等待和待重试任务仅在内存中，服务重启即丢弃。不保存发送历史，不补发。
- 设置页提供“发送更新通知”和“刷新状态”。手动发送会以当前接口版本替换待发通知，并尽快发送，即使内容没有变化也可使用。
- “通知已送达”仅表示接收端返回 2xx，不表示接收端的后续处理已完成。

接收方应按事件 ID 去重；网络中断可能使发送方无法得知接收方已经收到了事件。通知是更新提示，接收方以随后获取的公开 JSON 为准，不把通知当作完整变更日志。

### 请求格式与签名

请求方法为 `POST`，`Content-Type` 为 `application/json`：

```json
{
  "version": 1,
  "event": "public_bookmarks.changed",
  "eventId": "opaque-event-id",
  "revision": "sha256-content-hash",
  "feedUrl": "https://pageglean.example.com/public/bookmarks.json"
}
```

事件本身不携带书签内容。签名请求头：

- `X-PageGlean-Timestamp`：发送时间，Unix 秒。
- `X-PageGlean-Event-ID`：与 JSON 内的 `eventId` 一致。
- `X-PageGlean-Signature`：`sha256=` 加小写十六进制 HMAC-SHA256。

签名输入为 **时间戳的原始字符串 + `.` + HTTP 原始请求体字节**，密钥为 `PAGEGLEAN_WEBHOOK_SECRET`。接收方应先验证时间戳在允许窗口内（例如五分钟），再以恒定时间比较签名；不能先解析并重新序列化 JSON 后再计算签名。重试会重新生成时间戳和签名，但保持事件 ID 不变。

公开接口不需要跨域浏览器读取权限；服务端接收方可直接拉取。通知状态接口 `GET /api/publication` 和手动发送接口 `POST /api/publication/retry` 需要登录，后者返回 `202` 表示已排队。
