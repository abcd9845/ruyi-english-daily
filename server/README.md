# ruyi-site 服务端（阶段 2）

练习站的小服务端：托管**固定白名单**里的静态页 + 家庭码登录 + 大脑只读代理。关联 Issue #1。
纯标准库，`CGO_ENABLED=0` 静态编译，linux/amd64（Go 官方文档：Go 1.24 及以后要求 Linux 内核 ≥ 3.2，CentOS 7.5 / 3.10 满足）。

## 它做什么

| 路径 | 说明 |
|---|---|
| `GET /healthz` | 200，不依赖大脑 |
| `GET /`、其余静态页 | 只服务 `static-allowlist.txt` 里的文件（原样字节，`http.ServeContent`）。其他一律 404：`server.log`、`*.sh`、`.git/`、`.env`、`../`、`%2e%2e`、目录列表 |
| `POST /api/login` `{"code":"…"}` | 常量时间比较；成功发 `ruyi_sess`（HttpOnly、SameSite=Lax、Path=/、Secure 由开关决定）；错码不发 cookie；同 IP 每分钟最多 5 次失败，之后 429 |
| `GET /api/session` | `{"authenticated":bool}` |
| `POST /api/logout` | 清 cookie |
| `GET /api/config` | 需 cookie。`{"ntfy_url":"https://ntfy.sh/<topic>"|null}`，topic 来自 `RUYI_NTFY_TOPIC` |
| `GET /api/sets/{source}` | 需 cookie。题目列表（见下），图 URL 改写为 `/api/media/{source}/{sha256}` |
| `GET /api/media/{source}/{sha256}` | 需 cookie。服务端取票→取图→回图（`Content-Type: image/*`，`Cache-Control: private`） |

没有 cookie → 401。服务端**不写大脑**，不放任何密钥进页面。

### 对大脑只访问三个接口（按 `docs/题库图片支持-阶段2对接约定.md`）

```
GET  /v1/bank/questions?source=…&limit=500[&cursor=…]   Authorization: Bearer <bank:read>
POST /v1/media/{sha256}/ticket                           Authorization: Bearer <bank:read>（空正文）
GET  /v1/media/{sha256}?ticket=…                         不带 Authorization
```

- 票据缓存到过期前 30 秒；取图遇 401（`ticket_expired` / `ticket_invalid`）→ 丢弃票据、重取一次、重试一次。
- 只转发 png/jpeg/gif/webp（不转 svg）；响应体上限：列表 8 MiB、图 5 MiB；超时：列表 10 s、图 20 s；不跟随重定向。
- `source` 必须匹配 `^[a-z0-9][a-z0-9_.:-]{1,127}$` **且**在 `RUYI_ALLOWED_SOURCES` 内（默认只有 `math-intake.zz-yuekao-numberline-4y`）；
  `sha256` 必须是 64 位小写十六进制**且**属于该 source 当前题目的 media，否则 404，不会去找大脑要票。
- 错误只给分类（`brain_unavailable` / `brain_auth` / `brain_ticket`），不带 URL、头、票据。

## 配置（环境变量）

见 `deploy/ruyi-site.env.example`。缺必需变量启动即退出 1，报错只写变量名和原因，不打印值。

| 变量 | 必需 | 说明 |
|---|---|---|
| `RUYI_LISTEN` | 否 | 默认 `:18082` |
| `RUYI_STATIC_DIR` | 是 | 静态目录（仓库检出/拷贝）。只读 |
| `RUYI_FAMILY_CODE` | 是 | 家庭码（≥6 位）。改它 → 旧 cookie 全部失效 |
| `RUYI_SESSION_SECRET` | 是 | cookie 签名密钥（≥16 位，建议 `openssl rand -hex 32`）。改它 → 旧 cookie 失效 |
| `RUYI_SESSION_TTL` | 否 | 默认 `720h` |
| `RUYI_COOKIE_SECURE` | 否 | `0`（默认）/`1`。http 下必须 0 |
| `RUYI_BRAIN_BASE_URL` | 是 | 如 `scheme://host:port`，不含路径尾斜杠/query |
| `RUYI_BRAIN_TOKEN` | 是 | `bank:read` 令牌 |
| `RUYI_BRAIN_INSECURE_TLS` | 否 | `1` = 对大脑跳过证书校验。**仅当大脑地址是回环（127.0.0.1/::1/localhost）且为 https 时允许**，否则启动报错 |
| `RUYI_BRAIN_CA_FILE` | 否 | 指定信任的 CA/证书 PEM（`ruyi-site` 用户须可读）。与 INSECURE 二选一，同时设置报错 |
| `RUYI_LOG_LEVEL` | 否 | `debug`/`info`（默认）/`warn`/`error` |
| `RUYI_NTFY_TOPIC` | 否 | 动态页提交用的 ntfy topic，登录后由 `/api/config` 下发；未设则按钮显示「提交通道未配置」 |
| `RUYI_ALLOWED_SOURCES` | 否 | 逗号分隔的 source 白名单 |

日志输出到 stderr（journald），只记方法、路径（不含 query）、状态码、耗时、来源 IP；另有一层写入器把所有已配置的秘密值（令牌、家庭码、会话密钥、topic）替换成 `[redacted]`。

## 静态白名单

`static-allowlist.txt` 由 `make allowlist`（`scripts/gen-allowlist.sh`）从 `git ls-files` 生成：仓库里除 `server/`、`docs/`、`.gitignore` 以外的所有受跟踪文件（旧页 17 个 + `m/numberline-4y/index.html`）。
`go test` 会拿它与 `git ls-files` 比对，不一致就失败；新增页面 = 改清单 + 重新编译，需要评审。清单编进二进制，不读磁盘上的清单。

## 构建与测试

```bash
cd server
make test        # 单元测试
make build       # dist/ruyi-site-linux-amd64（静态，CGO 关闭）
make acceptance  # 起 mock 大脑 + 新服务，跑 test/acceptance.sh（含无头 Chrome 检查时设 SHOTS=目录）
```

`go.mod` 指定 Go 1.26.9（官方 Go 稳定版；本地版本较旧时会自动下载该工具链）。`cmd/mockbrain` 只用于测试，不随部署。验收记录见 `ACCEPTANCE.md`。

## 部署要点（详见 `docs/phase2/deploy-18082.md`）

1. 二进制放 `/opt/ruyi-site/ruyi-site`（root:root 755），单元文件见 `deploy/ruyi-site.service`，env 见 `deploy/ruyi-site.env.example`。
2. **静态目录要更新**：ECS 上的 `/opt/ruyi-english-daily` 目前只是仓库的拷贝（不是 git 仓库）。动态页 `m/numberline-4y/index.html` 是新文件，必须同步到该目录（目录 755、文件 644），否则 `/m/numberline-4y/` 是 404。
3. 大脑在本机且是自签证书：`RUYI_BRAIN_INSECURE_TLS=1`（只对回环地址有效）；或把证书放到 `ruyi-site` 可读的位置并设 `RUYI_BRAIN_CA_FILE`。
4. 冒烟：`/healthz` 200；`/server.log`、`/.git/config` 404；登录后 `/api/sets/math-intake.zz-yuekao-numberline-4y` 返回 JSON。
5. 回滚：`systemctl stop ruyi-site`。

## 已知限制

- 来源 IP 取自 TCP 对端地址，不信任 `X-Forwarded-For`。若以后放在反向代理后面，限速会变成"所有人共用一个 IP"，需要先加可信代理配置。
- 目前是明文 http；家庭码与 cookie 可被链路窃听（方案已记录）。
- 页面用 `textContent` 渲染题干，题干里若有 LaTeX/Markdown 会原样显示。
- 没有"检查客观题"：`bank:read` 取不到标准答案（`include_answer` 需要 `bank:write`），页面不编造答案。
