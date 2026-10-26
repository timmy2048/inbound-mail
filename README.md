# 来件

只收信、不发信的邮箱。后台添加域名，按数量生成随机地址，每个地址一条永久查看链接。没生成过的收件人，SMTP 和 HTTP 收信都直接拒收。

示例域名：`richrich.cc.cd`（DNS 在 Cloudflare 上）。

## 架构

一个 Go 进程里有三件事：

- HTTP：管理员后台、免登录的查看页，以及可选的 `POST /ingest`
- SMTP：入站监听，只把信交给已经生成的地址
- 定时清理：启动时以及之后每 15 分钟，删除超过保留期的信

数据在 SQLite，附件在数据目录里。`docker compose up` 就是全部。

公网 25 端口进得来时：

```
发件服务器 --SMTP--> 来件（拒收未知地址）--> SQLite + 附件文件
```

公网 25 端口进不来时（Oracle Cloud 上的 VPS 就是这样）：

```
发件服务器 --SMTP--> Cloudflare Email Routing --Worker--> POST /ingest --> 来件
```

两条路用同一套解析和存储。管理员走 `/admin`，持有链接的人走 `/m/{token}`。

用内置的 [go-smtp](https://github.com/emersion/go-smtp)，不另跑 maddy 或 Haraka。收件人白名单和网页在同一个进程里，生成地址后立刻能收，不用再同步一份 MTA 配置。这版不发信，也不需要出站队列。反垃圾和证书自动化以后可以加在前面，不必现在拆成两个服务。

## 本地运行

```bash
cp .env.example .env
# 改掉 ADMIN_PASSWORD

docker compose up --build
```

- 后台和查看页：<http://127.0.0.1:8787>
- 本机 SMTP：`127.0.0.1:2525`

不用 Docker 时：

```bash
set -a && source .env && set +a
go run ./cmd/laijian
```

`.env` 里的 `HTTP_ADDR=:8787` 给本机 `go run` 用。容器里的监听端口由 `docker-compose.yml` 改成 `:8080`，再映射到宿主机 8787，避免和 `.env` 打架。

首次登录用户名填 `admin`（只给密码管理器看），密码是 `ADMIN_PASSWORD`。

## 环境变量

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听。本机示例用 `:8787` |
| `SMTP_ADDR` | `:2525` | 入站 SMTP |
| `SMTP_DOMAIN` | `localhost` | EHLO 主机名 |
| `PUBLIC_BASE_URL` | `http://127.0.0.1:8787` | 查看链接的前缀 |
| `ADMIN_PASSWORD` | 必填 | 明文，或 `$2a$` / `$2b$` / `$2y$` bcrypt |
| `RETENTION_DAYS` | `3` | 超过这么多天的信删除 |
| `ATTACHMENT_MAX_BYTES` | `1048576` | 单个附件保存上限，超出的截断 |
| `MESSAGE_MAX_BYTES` | `8388608` | 整封信的 SMTP 上限，超出的拒收 |
| `MAIL_DATA_DIR` | `./data` | 附件目录 |
| `DATABASE_PATH` | `$MAIL_DATA_DIR/mail.db` | SQLite 文件 |
| `SERVER_PUBLIC_IP` | `203.0.113.10` | 写进 DNS 说明的公网 IPv4 |
| `SMTP_TLS_CERT` / `SMTP_TLS_KEY` | 空 | 两个都填才广告 STARTTLS |
| `INGEST_TOKEN` | 空 | `POST /ingest` 的共享密钥。留空则这个地址不存在 |

生成 bcrypt（密码不要放在命令行参数里）：

```bash
printf '%s' '你的密码' | go run ./cmd/hashpw
```

哈希里有 `$`。用 `.env` 的 `env_file` 传入即可。不要在 compose 文件里写 `${ADMIN_PASSWORD}`，否则 `$2a` 会被当成变量。

仓库里没有密钥，只有 `.env.example`。

## 公网 25 端口进不来

Oracle Cloud 以及一些别的网络会丢掉入站 TCP 25。这种机器收不到公网 SMTP，不要把 MX 指到 VPS 上的 `mail.<域名>`，也不要在安全组里等 25 端口。改用 Cloudflare Email Routing：MX 指到 Cloudflare，Worker 把原文 POST 进来。

实验室或本机如果 25 端口能用，继续用下面的「直接 SMTP」。同一个域名不要两条路一起开。走 Email Routing 时，MX 只能是 Cloudflare 的。

`INGEST_TOKEN` 留空时，`POST /ingest` 和别的未知地址一样返回 404，信不会进库。设成一长串随机值并重启之后才打开：

```bash
openssl rand -base64 32
```

同一串写进来件的 `.env`，以及 Worker 的 secret `INGEST_TOKEN`。不要提交。

Worker 发这样的请求（`X-Ingest-Token` 也可以代替 `Authorization`）：

```http
POST /ingest
Authorization: Bearer <INGEST_TOKEN>
Content-Type: message/rfc822
X-Envelope-To: 收件人
X-Envelope-From: 信封发件人

<RFC822 原文>
```

也接受 `Content-Type: application/json`，正文是 `{"to","from","raw_base64"}`。`raw_base64` 是原文的标准 base64。整封信仍然受 `MESSAGE_MAX_BYTES` 限制，附件截断规则不变。成功是 `201`，JSON 为 `{"id","recipient"}`。

| 状态 | `error` | Worker |
| --- | --- | --- |
| 401 | `unauthorized` | 抛错，让 Email Routing 当临时失败重试 |
| 400 | `bad_request` / `bad_address` / `empty_message` | `setReject`，永久拒收 |
| 413 | `message_too_large` | `setReject` |
| 422 | `unknown_recipient` | `setReject("Recipient address rejected")`，和 SMTP 550 同一条规则 |
| 500 | `temporary_failure` | 抛错，让 Email Routing 重试 |
| 404 | 没开 ingest，或地址写错 | `setReject` |

没生成过的地址、以及删掉之后的地址，都是 422，不落库。大小写和尖括号会先规范化，再查已经生成的邮箱。

### Cloudflare 上还要自己做

代码和 wrangler 配置不会替你打开路由，也不会带上密钥或服务器地址。Worker 在 `workers/cf-email-ingest/`，说明写在那个目录的 README 里。按这个顺序：

1. 用来件的 `INGEST_TOKEN` 重启服务。确认公网能访问 HTTP（compose 映射的是宿主机 `8787`），不是 TCP 25。能加 HTTPS 反向代理就用 HTTPS，避免密钥走明文。
2. 后台打开 [Email Routing](https://dash.cloudflare.com/?to=/:account/email-service/routing)：**Compute** → **Email Service** → **Email Routing** → **Onboard Domain**，选域名（示例是 `richrich.cc.cd`）。让 Cloudflare 写入它的 MX（`route1.mx.cloudflare.net`、`route2`、`route3`）。删掉指向 `mail.<域名>` 或这台 VPS 的 MX。
3. 在 `workers/cf-email-ingest` 里登录，把两个值写成 secret。`INGEST_URL` 是第 1 步的公网地址，例如 `https://<你的主机>/ingest`，不要写 `127.0.0.1`。`INGEST_TOKEN` 和来件相同。两个都不要写进文件再提交。

   ```bash
   cd workers/cf-email-ingest
   npm install
   npx wrangler login
   printf '%s' "$INGEST_URL" | npx wrangler secret put INGEST_URL
   printf '%s' "$INGEST_TOKEN" | npx wrangler secret put INGEST_TOKEN
   npx wrangler deploy
   ```

4. 建一条把信交给这个 Worker 的规则。Worker 名字是 `laijian-cf-email-ingest`。
   - 后台：**Routing Rules** → **Catch-all** → 动作 **Send to a Worker** → 选这个 Worker → Active。
   - 或者在 `wrangler.jsonc` 加上 `"addresses": ["*@richrich.cc.cd"]`（根域 catch-all，Wrangler 4.113 及以上），再 `npx wrangler deploy`。Wrangler 会先显示将要改的规则。
5. 后台生成一个地址，从外面发一封信，打开查看链接确认收进了。未生成的地址应被拒收。

环境变量就两个，都用 `wrangler secret put` 写入，不进仓库：`INGEST_URL`（来件的 `POST /ingest` 公网地址）、`INGEST_TOKEN`（和来件相同的共享密钥）。

## 直接 SMTP（25 端口可用时）

后台「添加域名」。添加之后的页面就是这条路径的 DNS 清单。以 `richrich.cc.cd` 为例，其他域名同样填，只是把名字换掉。25 端口进不来的机器跳过这一节，用上一节。

1. **MX**
   - 名称：`@`（有的面板要填 `richrich.cc.cd`）
   - 邮件主机：`mail.richrich.cc.cd`
   - 优先级：`10`
   - 代理：**仅 DNS（灰云）**。不要打开橙色云。SMTP 不能走 Cloudflare 的 HTTP 代理。
2. **A**
   - 名称：`mail`（完整是 `mail.richrich.cc.cd`）
   - IPv4：`SERVER_PUBLIC_IP`，也就是这台机器的公网地址
   - 代理：**仅 DNS（灰云）**
3. 解析生效后，只向生成出来的地址发信。

`203.0.113.10` 是文档地址，后台会提示它收不到公网的信。上机前改成 VPS 的公网 IPv4 并重启。本地注入不依赖这条记录。

SPF、DKIM、DMARC 这版不配。只收信时不是必须；以后若要拒收伪造来源，再补。

## 邮箱、链接、CSV

生成时只填数量。本地部分固定 10 位，字符是小写 `a-z` 和数字 `0-9`。一次最多 500 个。

一个地址对应一条查看链接，形如 `http://127.0.0.1:8787/m/{token}`。链接就是读信凭证，不需要登录。本版不能作废，也不能轮换。删掉邮箱后，链接失效，该地址开始拒收。

导出 CSV 的列：`address,magic_link,created_at,domain`。文件带 UTF-8 BOM，方便 Excel 打开中文。没有邮件导出。后台导航是全部邮箱，「导出这个域名的 CSV」只含当前域名。

同一地址可以收很多封，查看页按收到时间排列，新的在上面。时间显示为北京时间。

## 本地注入测试信

25 端口在很多环境里不能用，所以开发端口是 2525。生成地址后：

```bash
go run ./cmd/inject -smtp 127.0.0.1:2525 -to 本地部分@richrich.cc.cd
```

可选参数：`-from`、`-subject`、`-body`、`-attach 文件`。未登记的地址会得到 `550`，命令以非零状态退出。

Docker 已经把容器的 2525 映射到本机 2525，注入命令不用改。然后打开该地址的查看链接。

## 保留和附件

默认删除 **超过 3 天** 的信（按收到时间，严格早于 `现在 - RETENTION_DAYS × 24 小时`）。刚好满 3 天的还留着。邮箱和查看链接不删。

单个附件超过 `ATTACHMENT_MAX_BYTES`（默认 1MB）时，信仍然收下，附件只保存前面那一段，查看页标明已截断。整封信超过 `MESSAGE_MAX_BYTES`（默认 8MB）则在 SMTP 阶段拒收。

HTML 正文放在沙箱 iframe 里，外链图片不加载，避免发件人用图片追踪谁打开了信。

## 测试

```bash
go test ./...
```

覆盖这几件事：10 位本地部分的字符集、保留期边界、附件截断、未知收件人拒收、后台添加 `richrich.cc.cd` 后的 DNS 文案、生成邮箱、CSV、魔法链接里能看到注入的信，以及 HTTP ingest（密钥、未知地址、过大、JSON 原文进查看页）。

Worker 的投递判断在 `workers/cf-email-ingest`：

```bash
cd workers/cf-email-ingest && npm test
```

## 这版不做

- 对外发信
- SPF / DKIM / DMARC
- 多个管理员
- 查看链接作废或轮换
- 反垃圾、灰名单
- 把服务部署到备用 VPS

上备用 VPS、而且那台机器的 25 端口进得来时，还是这份 compose：把端口改成 `"25:2525"`（容器内仍监听 2525，不必 root），`SERVER_PUBLIC_IP` 改成公网 IPv4，`PUBLIC_BASE_URL` 改成实际访问地址，防火墙放行 25。需要 STARTTLS 时挂上证书文件并设置 `SMTP_TLS_CERT` 和 `SMTP_TLS_KEY`。HTTPS 可以以后在前面加一层反向代理，本服务不签发证书。

25 端口进不来的机器不要做上面的端口映射。设 `INGEST_TOKEN`，按「公网 25 端口进不来」把 MX 交给 Cloudflare。
