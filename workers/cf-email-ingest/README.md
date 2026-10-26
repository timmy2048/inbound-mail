# Cloudflare Email Routing → 来件

这个 Worker 只做一件事：把 Cloudflare Email Routing 交给它的一封信，原样 POST 到 来件的 `POST /ingest`。

`message.raw` 只能读一次，所以先缓冲成 `ArrayBuffer`，再发出去。来件返回 2xx 就收下。未知地址（422）、过大（413）或其它永久 4xx 会 `message.setReject`，发件方收到永久拒收。401、408、425、429、5xx 和网络错误会抛错，让 Email Routing 按临时失败重试，避免密钥还没配对或数据库抖一下就把信退掉。3xx 也是永久拒收：`INGEST_URL` 要写成最终地址，不要让它跳转。

密钥不会写进仓库。`INGEST_URL` 留空、不是 http(s)、或没有 `INGEST_TOKEN` 时，Worker 直接 `setReject("mail ingest is not configured")`，不会发请求。

## 环境变量

| 名字 | 放哪里 | 作用 |
| --- | --- | --- |
| `INGEST_URL` | `wrangler secret put`，本地是 `.dev.vars` | 来件的完整地址，例如 `http://mail.example.com:8787/ingest`。Worker 跑在 Cloudflare 上，`127.0.0.1` 和内网地址都到不了 VPS。 |
| `INGEST_TOKEN` | 同上 | 和来件 `INGEST_TOKEN` 同一串。请求头是 `Authorization: Bearer …`。 |

## 操作者还要自己做的

仓库里的配置不会打开 Email Routing，也不会写上真密钥。按这个顺序做：

1. 在来件的 `.env` 里设置 `INGEST_TOKEN`（`openssl rand -base64 32`），重启进程。不要把这串提交进 git。
2. 确认 Cloudflare 能访问来件的 HTTP，而不是 TCP 25。`docker compose` 已经把后台映射到宿主机 `8787`。Oracle Cloud 的安全组要放行这个 HTTP 端口。能上 HTTPS 反向代理就用 HTTPS。
3. 打开 [Email Routing](https://dash.cloudflare.com/?to=/:account/email-service/routing)：**Compute** → **Email Service** → **Email Routing** → **Onboard Domain**，选 `richrich.cc.cd`（或你的域名）。让 Cloudflare 写上它自己的 MX（`route1.mx.cloudflare.net`、`route2`、`route3`）。删掉指向 `mail.<域名>` 或 VPS 地址的 MX。这条路径不要给 `mail` 开橙色代理。
4. 在本目录安装依赖，登录，把 `INGEST_URL` 和 `INGEST_TOKEN` 写成 secret，再部署。`INGEST_URL` 用第 2 步那个公网地址。

   ```bash
   npm install
   npx wrangler login
   printf '%s' "$INGEST_URL" | npx wrangler secret put INGEST_URL
   printf '%s' "$INGEST_TOKEN" | npx wrangler secret put INGEST_TOKEN
   npx wrangler deploy
   ```

   `printf` 用的是你自己 shell 里的变量，不要把值写进仓库。`wrangler secret put` 会立刻发布一版。`wrangler.jsonc` 只声明这两个名字，不放值。

5. 把这个域名的收信规则指到 Worker `laijian-cf-email-ingest`。二选一：
   - 后台：**Email Routing** → **Routing Rules** → 打开 **Catch-all**，动作选 **Send to a Worker**，Worker 选 `laijian-cf-email-ingest`，保存为 Active。
   - 或者在 `wrangler.jsonc` 加上顶层 `"addresses": ["*@richrich.cc.cd"]`（只支持根域的 catch-all，需要 Wrangler 4.113 或更新），再执行 `npx wrangler deploy`。Wrangler 会先列出规则变更；破坏性变更要你确认。

6. 在来件后台生成一个地址，从别的邮箱发一封过去。查看链接里应能看到这封信。没生成过的地址会被拒收。

本地看 Worker 行为：`npm test`。`npx wrangler dev` 会在 `/cdn-cgi/local/email` 收一封原始邮件，用来手动打 `email()`。那之前把 `.dev.vars.example` 复制成 `.dev.vars` 并填上同一串 `INGEST_TOKEN`。

文档：[Email handler](https://developers.cloudflare.com/email-service/api/route-emails/email-handler/)、[路由规则](https://developers.cloudflare.com/email-service/configuration/email-routing-addresses/)、[接入域名](https://developers.cloudflare.com/email-service/get-started/route-emails/)。
