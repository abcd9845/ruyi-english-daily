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
| `GET /api/sets/{source}` | 需 cookie。题目列表（见下），按题号排序，图 URL 改写为 `/api/media/{source}/{sha256}`，附 `figure_status` |
| `GET /api/media/{source}/{sha256}` | 需 cookie。服务端取票→取图→回图（`Content-Type: image/*`，`Cache-Control: private`） |

没有 cookie → 401。服务端**不写大脑**，不放任何密钥进页面。

### 对大脑只访问三个接口（按 `docs/题库图片支持-阶段2对接约定.md`）

```
GET  /v1/bank/questions?source=…&limit=500[&cursor=…]   Authorization: Bearer <bank:read>
POST /v1/media/{sha256}/ticket                           Authorization: Bearer <bank:read>（空正文）
GET  /v1/media/{sha256}?ticket=…                         不带 Authorization
```

- 列表请求**只发 `source`、`limit`、`cursor`** 三个参数（大脑只接受 13 个已知参数，其余 400；`include_answer` 需要 `bank:write`，**本服务绝不发**，也不发 `status`）。一次 `limit=500` 取完（数轴卷 active 共 60 题），按 `next_cursor` 翻页直到 `null`。
- 返回字段：`qref, number, qtype, stem, options, media, figure_status`；`options`/`number` 没有时键省略；默认不含答案/讲解。
- **题号排序**：`number` 是字符串，形如 `1(1)`、`11(2)③`、`18(4)`。服务端用 `^(\d+)\((\d+)\)(.*)$` 取（大题号, 小题号, 后缀）排序，后缀按字符串比较；解析不到（含没有 number）的排最后，保持原顺序（稳定）。页面显示库里的 `number` 原文；另给 1..N 的位置序号 `ordinal`。
- **缺图语义**（`figure_status` 是大脑新增的只读派生字段，**可选**，缺键 = `unknown`）：
  | 情况 | 页面 |
  |---|---|
  | 有 media（stem/figure/option） | 显示图（`none` 但有 media 也照样显示） |
  | 无 media，`missing` / `unknown` / 缺键 / 其他值 | 灰底「图未入库」 |
  | 无 media，`none` | 不显示任何占位 |
  | 无 media，`ok`（数据不一致） | 灰底「图未入库」，服务端记一条 warn（只含 qref） |
  **注意：数轴卷目前 60 题的图都还没入库，真实环境里整页都是占位，这是预期。**
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
| `RUYI_FAMILY_CODE` | 是 | 家庭码（≥5 个字符）。改它 → 旧 cookie 全部失效。**码较短**：安全上依赖限速（同 IP 每分钟 5 次失败）；以后可换更长的码，换码只需改 env 并重启 |
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
| `RUYI_BRAIN_GRADE_TOKEN` | 否 | **判分令牌**（单独的令牌，不能等于 `RUYI_BRAIN_TOKEN`；只应能"判分并记录一次作答"）。空 = 不判分，提交走降级（只归档 + 提醒） |
| `RUYI_BRAIN_GRADE_PATH` | 否 | 判分接口路径，默认 `/v1/bank/grade`（**草案**，契约集中在 `internal/brain/grade.go`） |
| `RUYI_ATTEMPTS_DIR` | 否 | 作答归档目录，默认 `/var/lib/ruyi-site/attempts`（0700，文件 0600） |
| `RUYI_GRADE_MAX_PER_HOUR` | 否 | 每套每小时提交上限（会话与 IP 各限），默认 5，范围 1–1000 |
| `RUYI_NTFY_BASE_URL` / `RUYI_NTFY_RETRY_DELAY` | 否 | ntfy 地址（默认 `https://ntfy.sh/`）/ 失败后重试等待（默认 1m）；一般不用配 |
| `RUYI_AUTO_SOURCES` / `RUYI_AUTO_TTL` | 否 | 允许自动出题的 source（默认空=关）/ 自动套有效期（默认 6h） |
| `RUYI_SETS_FILE` | 否 | 练习套配置 JSON（见下「通用套练习页」）。未设 → 没有套，`/api/practice/*` 404，其余功能不变。文件有误启动即退出，报错只写位置不写内容 |

日志输出到 stderr（journald），只记方法、路径（不含 query）、状态码、耗时、来源 IP；另有一层写入器把所有已配置的秘密值（令牌、家庭码、会话密钥、topic）替换成 `[redacted]`。

## 静态白名单

`static-allowlist.txt` 由 `make allowlist`（`scripts/gen-allowlist.sh`）从 `git ls-files` 生成：仓库里除 `server/`、`docs/`、`.gitignore` 以外的所有受跟踪文件（旧页 17 个 + `m/numberline-4y/index.html`）。
`go test` 会拿它与 `git ls-files` 比对，不一致就失败；新增页面 = 改清单 + 重新编译，需要评审。清单编进二进制，不读磁盘上的清单。

## 通用套练习页（P0，Issue #10）

一套题 = 一个页面：`/m/practice/?set=<id>`，题目由 `RUYI_SETS_FILE` 配置，公式用本地 KaTeX 渲染。P0 只作答并把草稿存在本机；**提交 / 判分 / 归档 / 提醒见下一节（P1–P3）**。

套配置（样例 `deploy/sets.example.json`，只有占位 qref；真实套的 qref 只放在部署环境的文件里，不进仓库）：

```json
{"sets":[{"id":"demo-1","title":"示例套","source":"<大脑 source key>","qrefs":["<qref>","<qref>"]}]}
```

- `id` 必须匹配 `[a-z0-9_-]{1,64}`、不得重复；`qrefs` 不能空、不得重复；未知字段、尾随内容都会报错。
- `GET /api/practice/{set_id}`（需登录）：用现有只读大脑接口拉整卷（只发 `source/limit/cursor`），按 qrefs 白名单过滤、按配置顺序返回
  `{set_id,title,items:[{ordinal,qref,qtype,stem_md,options?:[{key,text_md}],blanks?,figure_status,media}],missing_qrefs}`。
  不返回答案/解析（客户端只解码需要的字段，且从不发 `include_answer`）。配置里有但库里没取到的 qref 进 `missing_qrefs`（不 500）。
  `blanks` = 题干**非公式部分**里连续 ≥4 个 `_`/`＿` 的段数（与页面画的输入框数一致）。
- 图片仍走 `/api/media/{source}/{sha256}`；只因套而开放的 source 只放行白名单题目的图，`/api/sets/{source}` 对它仍是 404。
- 页面 `server/web/m/practice/index.html` 与 KaTeX（`server/vendor/katex`，仅 woff2，版本与哈希见 `vendor/README.md`）都 `go:embed` 进二进制：**部署不需要往 ECS 静态目录拷这些文件**。
  `/vendor/katex/…` 只放行内嵌的精确文件，其它 `/vendor/…` 都是 404；页面带 CSP（`default-src 'none'`，只允许本站脚本/样式/字体/连接）。
- 页面 Markdown 只支持最小安全子集：先转义 HTML，再保护数学片段，处理段落、换行、`**粗体**`、`` `行内代码` ``；公式 `$…$` `$$…$$` `\(…\)` `\[…\]`（`trust` 关，解析失败回退原文）。
- 草稿键 `ruyi-practice-<set_id>-v1`（`{v,set,a:{qref:答案},t}`，不含家庭码）；作答区比旧页大、字号 18px、触控目标 ≥44px。
- 图：只有大脑明确标 `figure_status=missing` 的题才显示「图未入库」灰块；`unknown/none` 不显示（本期的题本来就没有图）。

## 提交、判分、归档、提醒、自动出题（P1–P3）

上线清单与回滚见 **`DEPLOY-P3.md`**。

**大脑抽象（P1）**：`internal/brain.Bank` = `ListBySource` / `LookupByQrefs` / `SelectForPractice` / `Grade`。大脑还没有对应接口的部分由适配层模拟（整卷拉取 + 白名单 / 随机 / 顺序）；
`wrong_only`、`due_first` 没有大脑接口，一律返回 `ErrUnsupported`（HTTP 501），不自己编逻辑。判分的线上契约（草案 `POST /v1/bank/grade`）全部写在 `internal/brain/grade.go`，换契约只改那一个文件。

**`POST /api/practice/{set_id}/grade`（需登录，JSON）**：

```json
{"client_attempt_id":"<uuid>","answers":[{"qref":"<套内 qref>","answer":"B"}]}
```

- 答案形状按题型校验：单选 `"B"`（必须是选项 key）、多选 `["A","C"]`、填空 `["…","…"]`（个数 = 题干空数，每格 ≤200 字）、自由作答 `"…"`（≤2000 字）；未知字段/控制字符/超长/不属于该套的 qref 一律 400，且**不会到达大脑**。没作答的题不用传。
- `client_attempt_id` 是**大脑侧幂等键**：同 id 重复提交返回第一次的结果（`replayed:true`），不重复判分、不重复归档、不重复提醒；重启后从归档文件恢复。页面里同一份答案重试沿用同一个 id，改了答案换新 id。
- **只有大脑确认**（回复里 `recorded:true`、带大脑自己的 `attempt_id`、回显了 `client_attempt_id`）才是 `mode:"graded"` / `brain_recorded:true`，页面才显示「已提交到大脑」。
  判分令牌没配 / 大脑不可用 / 回复没有确认 → `mode:"archive_only"`：所有题 `pending_key`、`correct:null`，页面显示「已提交（暂未判分）」，**绝不出现**「已记入大脑 / 已提交到大脑」。`pending_key`、`unsupported` 一律「待核对」，不显示对错。
- 限速：每套每小时 `RUYI_GRADE_MAX_PER_HOUR` 次新提交（会话与来源 IP 各自计），超了 429 + `Retry-After`；重复提交同一个 id 与校验失败不占额度。
- 响应只有 `client_attempt_id,set_id,mode,brain_recorded,results[{qref,status,correct}],summary{total,graded,correct,pending,unanswered},archived,notify,replayed`，没有答案、解析、大脑内部 id。
- 本站**不持有大脑写权限**：唯一的写是上面这个判分令牌调用的判分接口；本站不直接写大脑任何别的东西。

**归档（P3）**：每次新提交追加一行 JSON 到 `$RUYI_ATTEMPTS_DIR/attempts.jsonl`（目录 0700、文件 0600、写后 fsync；内容见 `DEPLOY-P3.md`），不含 cookie/令牌/家庭码/topic。目录写不了：照常判分和提醒，响应 `archived:false`，日志只一条 error（不带路径/内容）。它是大脑不可用时的兜底凭据。

**ntfy 提醒（P3）**：服务端向 `RUYI_NTFY_TOPIC` 发一条"打招呼"，如 `如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。`（降级时是 `…已提交（未判分）。`），正文后附 JSON 明细。**不是审批环节**，失败不影响判分，异步重试一次，日志里不出现 topic/地址。

**自动出题（3.5）**：`GET /api/practice/new?source=&n=1..20[&order=random|seq][&seed=N]`（需登录；`RUYI_AUTO_SOURCES` 为空时整个入口 404）。
返回 `{set_id:"auto-<8位>",url,title,requested,count,truncated,note?,expires_at}`；题不够 n 就全给并带 `note`；套存在内存里，`RUYI_AUTO_TTL` 后失效，其 qref 白名单同样约束判分与取图。`wrong_only=1` → 501 `{"error":"wrong_only_unavailable"}`。

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

- 家庭码最短 5 个字符，较短，防护主要靠登录限速（同 IP 每分钟最多 5 次失败，之后 429）。建议后续换更长的码；换码只改 env 文件并 `systemctl restart ruyi-site`，旧 cookie 随之全部失效。

- 来源 IP 取自 TCP 对端地址，不信任 `X-Forwarded-For`。若以后放在反向代理后面，限速会变成"所有人共用一个 IP"，需要先加可信代理配置。
- 目前是明文 http；家庭码与 cookie 可被链路窃听（方案已记录）。
- 数轴页（`/m/numberline-4y/`）用 `textContent` 渲染题干，不解析 LaTeX/Markdown；通用练习页才解析。
- 站点自己不判对错：`bank:read` 取不到标准答案（`include_answer` 无 `bank:write` 返回 403），服务端也绝不发该参数；对错只来自大脑判分接口的回复，拿不到就显示「待核对」。
