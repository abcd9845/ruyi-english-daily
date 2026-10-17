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
| `RUYI_BRAIN_ID` | 否 | 发给大脑 `grade` 请求体 `brain` 和 `pick-practice` 查询串 `brain` 的练习脑 id，默认（不设）`ruyi`；格式 `^[a-z0-9][a-z0-9_-]{0,63}$`，不合规启动即退出（只报变量名）。用途：联调时指向测试脑（如 `qa-practice`）。**两枚令牌（`RUYI_BRAIN_TOKEN` 读、`RUYI_BRAIN_GRADE_TOKEN` 判分/选题）的 scope 必须与它匹配**，否则大脑会拒绝。`lookup`（取题文）本身不带 brain 参数。站点自己的 URL 上多带的 `brain=` 一律忽略 |
| `RUYI_BRAIN_INSECURE_TLS` | 否 | `1` = 对大脑跳过证书校验。**仅当大脑地址是回环（127.0.0.1/::1/localhost）且为 https 时允许**，否则启动报错 |
| `RUYI_BRAIN_CA_FILE` | 否 | 指定信任的 CA/证书 PEM（`ruyi-site` 用户须可读）。与 INSECURE 二选一，同时设置报错 |
| `RUYI_LOG_LEVEL` | 否 | `debug`/`info`（默认）/`warn`/`error` |
| `RUYI_NTFY_TOPIC` | 否 | 动态页提交用的 ntfy topic，登录后由 `/api/config` 下发；未设则按钮显示「提交通道未配置」 |
| `RUYI_ALLOWED_SOURCES` | 否 | 逗号分隔的 source 白名单 |
| `RUYI_BRAIN_GRADE_TOKEN` | 否 | **练习站令牌**，只在服务端，用于 `POST /v1/bank/grade` 以及 `GET /v1/brain/pick-practice`（读题/lookup/列表/媒体仍用 `RUYI_BRAIN_TOKEN`）。契约要目标练习脑的 write 权限，`bank:read` 令牌调它是 403）。**要用 `mode=due_first|wrong_only` 选题就必须配判分令牌**；没配 → 选题返回 501 `due_first_unavailable`/`wrong_only_unavailable`「大脑接口未就绪（选题需要配置判分令牌）」，**不会**偷偷改用读令牌；令牌被大脑 **401 或 403** 拒 → 502 `pick_forbidden`「选题令牌无权限」（401 与 403 都得到 `pick_forbidden`；仅 `due_first` / `wrong_only` 这类大脑选题模式，随机套不走此映射）。大脑侧要给它「读题库 + 只写 ruyi 一个脑」的范围；**scope 名由大脑定，本站不写死**（以后可能换成更窄的 `bank:grade`，换令牌即可，代码不用改）。**不能等于 `RUYI_BRAIN_TOKEN`**（启动即校验，报错只写变量名）。空 = 不判分，提交走降级（只归档 + 提醒）。令牌绝不下发浏览器、不入归档、不进日志/响应/ntfy |
| `RUYI_BRAIN_GRADE_PATH` | 否 | 判分接口路径，默认 `/v1/bank/grade`（契约见 `internal/brain/grade.go`） |
| `RUYI_BRAIN_LOOKUP_PATH` | 否 | 按 qref 取题文，默认 `/v1/bank/questions/lookup`（用读令牌） |
| `RUYI_BRAIN_PICK_PATH` | 否 | 选题接口，默认 `/v1/brain/pick-practice`（用判分令牌 `RUYI_BRAIN_GRADE_TOKEN`） |
| `RUYI_PICK_SKIP_DEFERRED` | 否 | `0`（默认）/`1`。选题结果里带 `deferred:*` 的题（参考答案待核/不支持题型/缺图）：`0` = 保留并在页面标「参考答案待核，不自动判」；`1` = 直接跳过 |
| `RUYI_ATTEMPTS_DIR` | 否 | 作答归档目录，默认 `/var/lib/ruyi-site/attempts`（0700，文件 0600） |
| `RUYI_GRADE_MAX_PER_HOUR` | 否 | 每套每小时提交上限（会话与 IP 各限），默认 5，范围 1–1000 |
| `RUYI_NTFY_BASE_URL` / `RUYI_NTFY_RETRY_DELAY` | 否 | ntfy 地址（默认 `https://ntfy.sh/`）/ 失败后第一次重试前的等待（默认 1m，第二次重试前翻倍）；一般不用配 |
| `RUYI_AUTO_SOURCES` / `RUYI_AUTO_TTL` | 否 | 允许自动出题的 source（默认空=关）/ 自动套有效期（默认 6h） |
| `RUYI_QUEUE_ENABLED` / `RUYI_QUEUE_NOTIFY_ENABLED` | 否 | `0`（默认）/`1`。S1 待解答队列 / 完成提醒轮询，见下「待解答队列」。队列需要 `RUYI_BRAIN_GRADE_TOKEN`；提醒需要 `RUYI_BRAIN_NOTIFY_TOKEN` 和 `RUYI_NTFY_TOPIC` |
| `RUYI_BRAIN_NOTIFY_TOKEN` / `RUYI_QUEUE_POLL_SECONDS` / `RUYI_QUEUE_STATE_DIR` / `RUYI_BRAIN_PENDING_PATH` | 否 | 第三枚令牌（只用于 events/ack，必须与另两枚不同）/ 轮询秒数 40–600（默认 45）/ 队列本地状态目录（默认 `/var/lib/ruyi-site/queue`，0700）/ 入队路径（默认 `/v1/bank/pending-answers`） |
| `RUYI_SETS_FILE` | 否 | 练习套配置 JSON（见下「通用套练习页」）。未设 → 没有套，`/api/practice/*` 404，其余功能不变。文件有误启动即退出，报错只写位置不写内容 |

日志输出到 stderr（journald），只记方法、路径（不含 query）、状态码、耗时、来源 IP；另有一层写入器把所有已配置的秘密值（令牌、家庭码、会话密钥、topic）替换成 `[redacted]`。

## 静态白名单

`static-allowlist.txt` 由 `make allowlist`（`scripts/gen-allowlist.sh`）从 `git ls-files` 生成：仓库里除 `server/`、`docs/`、`.gitignore` 以外的所有受跟踪文件（旧页 17 个 + `m/numberline-4y/index.html`）。
`go test` 会拿它与 `git ls-files` 比对，不一致就失败；新增页面 = 改清单 + 重新编译，需要评审。清单编进二进制，不读磁盘上的清单。

## 通用套练习页（P0，Issue #10）

一套题 = 一个页面：`/m/practice/?set=<id>`，题目由 `RUYI_SETS_FILE` 配置，公式用本地 KaTeX 渲染。P0 只作答并把草稿存在本机；**提交 / 判分 / 归档 / 提醒见下一节（P1–P3）**。

套配置（样例 `deploy/sets.example.json`，只有占位 qref；真实套的 qref 只放在部署环境的文件里，不进仓库）：

联调用的另一份样例 `deploy/sets.qa-practice.example.json`（**联调用，非线上**）：套 `qa-practice-1/2/alt`，其中 source 与 qref 全是**占位值**（`<qa-practice-source>` / `<qref-lowercase-N>`，不是合法值，原样加载会启动失败）——**使用前需替换成大脑给的真实 source key 和小写 qref**，真实标识只放在部署环境的文件里，不进公开仓库；配合 `RUYI_BRAIN_ID=qa-practice` 使用；题目都是 pending_key，只验提交 / 落库 / 幂等 / 每日 3 次上限，不验判对错；`qa-practice` 的选题预期返回空。不要用于线上。

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

**大脑抽象**：`internal/brain.Bank` = `ListBySource` / `LookupByQrefs` / `SelectForPractice` / `Grade`。已按大脑「最终契约」适配（Refs #10），**⚠ 这三个接口线上要等大脑 #35 部署后才存在**，现在只对 mock 测过：

| 接口 | 用途 | 没部署（404/501/405）时 |
|---|---|---|
| `POST /v1/bank/questions/lookup`（≤100 qref，返回 `{items,missing}`，无答案，带 `rev`/`media`/`figure_status`） | 取题文；`missing` 的题不崩，页面列在 `missing_qrefs` | 退回整卷列表接口（P1–P3 行为），30 秒内不再探测；页面没有 `rev` |
| `GET /v1/brain/pick-practice?source=&n=&mode=due_first\|wrong_only&brain=ruyi`（`n` 1..10，站点超过 10 会先降到 10 并提示；只发 source/n/date/brain/mode） | 大脑选题（错题/到期复习），每题带规范 qref、`origin`（只有 review\|wrong）和 `reasons`（`due_on:YYYY-MM-DD` / `wrong_count:N` / `deferred:unsupported_qtype\|pending_key\|figure_missing`） | `/api/practice/new?mode=due_first\|wrong_only` → **501「大脑接口未就绪」**（不是 500）；不带 `mode` 的随机套照旧 |
| `POST /v1/bank/grade`（独立令牌；契约见下） | 判分并记入大脑 | 照常归档 + 提醒，`mode:"archive_only"`、`brain_note:"not_ready"`，页面显示「暂未判分」，绝不说「已提交到大脑」 |

**404 靠结构区分，不靠错误码字符串**：未部署路由的兜底 404 是 `not_found` / "not found" 且没有 `details`；判分的「未知 qref」404 带 `details.qrefs`（消息 "question not found"）；lookup 部署后永不 404（未知 qref 进 `missing`），所以 lookup 的 404/501/405 都当「未部署」；选题 404 "source not found"/"brain not found" 是明确错误（`pick_source_not_found`，不是「未就绪」），400 `ambiguous_source` → `pick_source_ambiguous`。启动时（只在配了练习套文件时）会对 lookup 做**一次只读探测**（一个明显不存在的 qref，不写任何东西），结果只写日志，不影响启动。

其它失败（令牌没配、大脑 5xx/超时、回复不合规）同样降级为 `archive_only`（`brain_note`：`not_configured` / `unavailable` / `not_sent`）。契约编解码集中在 `internal/brain/grade.go`、`contract.go`。

**`POST /api/practice/{set_id}/grade`（需登录，JSON）**：

```json
{"client_attempt_id":"<uuid>","answered_at":"<RFC3339，可选>","attempt_kind":"correction|variant（可选）",
 "answers":[{"qref":"<套内 qref>","answer":"B","rev":1}]}
```

`rev` 是页面从大脑 lookup 拿到的题目版本（整数，可选），原样带给大脑；大脑回 409 `rev_mismatch` 就说明题目已更新。`answered_at` 由页面和 `client_attempt_id` 一起固定（重试时不变，大脑会比对），`attempt_kind` 只允许 `correction` / `variant`，`session_ref` 在 #22 里由服务端填套 id，S1 起改填 `client_attempt_id`（队列开着时与入队的 `session_ref` 一致）。**关队列（`RUYI_QUEUE_ENABLED=0`）不会还原 grade 请求里的 `session_ref`，它仍是 cid（大脑侧不依赖这个值）；只有换回 #22 二进制才还原成套 id。**向大脑发的请求体：`{brain:"ruyi", client_attempt_id, items:[{qref, answer, rev?, attempt_kind?, answered_at?, session_ref?, payload?}]}`，**严格契约**（大脑对未知字段、grade 上除 `?brain=` 以外的 query 一律 400），所以站点发之前自己校验：qref 小写上线（站点内仍用套里的原串匹配回显）、`answered_at` 只在 now-7d..now+1min 内的 RFC3339 才发（否则整段省略）、`rev` 须是 ≥1 的整数、`session_ref` 1..200、`payload` ≤4096 字节、答案是字符串或 ≤10 个字符串且每个 ≤500 字（超过的题**不发**，记为不自动判分、只留档）、`client_attempt_id` 须匹配 `^[A-Za-z0-9._:-]{1,160}$`、批量 1..50 题。幂等键是 `ps:<client_attempt_id>:<规范 qref>`（≤198 字符；别名先解析成规范 qref，所以用别名再交同一个 id 是重放）。回复 201（有新写入）或 200（全是重放/没有写回）；同键同答案重放 200 `duplicate:true`，**同键但 `answered_at` 与第一次不同 → 409 `idempotency_conflict`**，没带 `answered_at` 则按重放（`attempt_kind`/`session_ref`/`payload` 不参与比较）。

- 答案形状按题型校验：单选 `"B"`（必须是选项 key）、多选 `["A","C"]`、填空 `["…","…"]`（个数 = 题干空数，每格 ≤200 字）、自由作答 `"…"`（≤2000 字符，按字符计、不是字节；与大脑的上限相同，同一个常量 `brain.MaxAnswerLen`，≤2000 字符正常发大脑判分）；未知字段/控制字符/超长（含自由作答超过 2000 字符）/不属于该套的 qref 一律 400，且**不会到达大脑**。没作答的题不用传。
- `client_attempt_id`（1–160 位 `[A-Za-z0-9._:-]`；页面用 UUID）是**大脑侧幂等键**的一部分：同 id、同答案重复提交返回第一次的结果（`replayed:true`），不重复判分、不重复归档、不重复提醒；重启后从归档文件恢复；归档丢了而大脑已有 → 大脑回 `duplicate:true` 和同一个 `attempt_id`，不重复记账。网络重试沿用同一个 id，用户改了答案换新 id。
- **同 id 不同答案 → 409 `idempotency_conflict`（`retry_new_id:true`）**：站点自己先比对答案指纹，大脑也可能回这个码。页面收到后**丢掉旧 id、换新 id 自动重发一次**（再冲突就停下来提示），答案不丢。
- 校验（都不会到大脑）：每批 ≤50 题、qref 不能重复、填空 ≤10 格；`answered_at` 须是 RFC3339、`attempt_kind` 只能 `correction|variant`、`rev` 须是 ≥1 的整数。
- 大脑的拒绝映射成明确的页面状态：**429 + 每日封顶**（大脑回 `rate_limited` / "daily grade limit reached" + `details.qrefs` + `Retry-After`；口径：同一脑同一题每个上海日最多写 3 次，`pending_key`/`needs_review` 算、`replay` 与 `unsupported` 不算，整批拒绝；大脑**不返回剩余/上限数字**）→ 站点回 **429 `daily_cap`**（`Retry-After` 透传），**这次提交仍写入 `attempts.jsonl`**（`mode:"archive_only"`、`brain_note:"daily_cap"`、每题 `recorded:false`、被点名的题 `capped:true`），但**不提醒家长**；页面提示「今天这题的次数已用完，已留档，未记入大脑」，答案保留可改，下一次提交换新 `client_attempt_id`。同一 id 重放只会得到当时的 429 记录，不会再问大脑。**其它**429（不像每日封顶）→ `brain_rate_limited`「大脑这会儿请求太多…」，不归档、答案保留；站点自己的小时限速仍是 `too_many_submissions`。409 `rev_mismatch`、404 未知题等其余映射不变（整批都没写，不归档、不提醒）。
- **「今天约还剩 N 次」**：`GET /api/practice/<set>/quota`（需登录）→ `{day, limit:3, used:{qref:n}, basis:"site_archive"}`。`used` = 本站归档里、当天（Asia/Shanghai）大脑确认 `recorded:true` 且非 `duplicate` 的次数；本地测试套、v1 老归档、`unsupported`、降级归档、被 429 拒的提交都不算。**这是按本站提交记录的估算**：看不到别处对大脑的写入，也看不到「大脑写了但回复没到」的那种；页面据此显示「今天约还剩 N 次」并在提交前弹确认（「每题每天只能提交3次，本次提交后约还剩N次」；多题时说明本次题数和剩余最少的一题，已用完的题会警告「这次只会留档、不会记入大脑」）。大脑自己的 429 拒绝记在本机（localStorage）并优先于估算。取消确认 = 不发请求、草稿原样保留。
- 页面对没有被大脑记录的题（`recorded` 不是 true：`unsupported`、`archive_only`、`not_sent`、`daily_cap`）在徽章和说明里加「（不入大脑）」；大脑确认记录的题不加。
- **逐题 `recorded`**（大脑回复里每题的 `recorded`、整数 `attempt_id`、回显 `client_attempt_id`）才是「已记入大脑」的唯一依据。状态：`graded`（有 `correct`）/ `pending_key`（无参考答案，只记答案）/ `needs_review`（只记答案，页面「待核对」）/ `unsupported`（`recorded:false`，大脑什么都没写，页面「不自动判分」）。页面逐题判断，只有 `recorded:true` 的题算已进大脑：全部 `recorded:true` →「已提交到大脑」；**部分** `recorded:true` →「已提交到大脑（N / M 题）」（M = 已作答的题数），并标出未记录的题（该题「不自动判分」+ 附注）；**全部** `recorded:false` →「已提交（仅留档，未记入大脑）」；`archive_only` →「已提交（暂未判分）」。**`recorded:false` 的题绝不出现「已提交到大脑」**，也不显示对错。
  判分令牌没配 / 接口未部署 / 大脑不可用 / 回复不合规 → `mode:"archive_only"`：所有题 `pending_key`、`correct:null`，`recorded:false`。
- 限速：每套每小时 `RUYI_GRADE_MAX_PER_HOUR` 次新提交（会话与来源 IP 各自计），超了 429 + `Retry-After`；重复提交同一个 id 与校验失败不占额度。
- 响应只有 `client_attempt_id,set_id,mode,brain_recorded,recorded_count,brain_note?,results[{qref,status,correct,recorded,attempt_id?,duplicate?}],summary{total,graded,correct,pending,unsupported,unanswered,recorded},archived,notify,replayed`，没有答案、解析（`attempt_id` 是大脑给的整数作答编号，不是秘密）。网络层：大脑超时/5xx 时用**同一个** `client_attempt_id` 和同一份请求体重试一次，靠大脑幂等不会重复记账。
- 本站**不持有大脑写权限**：唯一的写是上面这个判分令牌调用的判分接口；本站不直接写大脑任何别的东西。

**归档（P3，v2）**：每次新提交追加一行 JSON（v1 旧行仍可读；v2 有逐题 `recorded`/`attempt_id`/`duplicate`，没有令牌，`brain_note` 记降级原因） 到 `$RUYI_ATTEMPTS_DIR/attempts.jsonl`（目录 0700、文件 0600、写后 fsync；内容见 `DEPLOY-P3.md`），不含 cookie/令牌/家庭码/topic。目录写不了：照常判分和提醒，响应 `archived:false`，日志只一条 error（不带路径/内容）。它是大脑不可用时的兜底凭据。

**ntfy 提醒（P3）**：服务端向 `RUYI_NTFY_TOPIC` 发一条"打招呼"，如 `如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。`（降级时是 `…已提交（未判分）。`），后接「完成于 HH:MM:SS（北京时间）。」；**正文是一小段话，不含答案/JSON/qref**（完整记录在服务器 attempts 存档，按 attempt 前 8 位查）。**不是审批环节**：推送在后台协程里发，提交响应最多等 1 秒（`notify`：`sent` 首次已送达 / `retrying` 首次失败、后台重试 / `queued` 1 秒内还没结果 / `off` 未配 topic；重放为空），失败不影响判分与归档；单次尝试 10 秒超时，最多 3 次（首发 + 2 次重试，间隔 `RUYI_NTFY_RETRY_DELAY`、再翻倍）；同一 `client_attempt_id` 进程内只推一次。失败日志只写类别（`kind`=dns/dial/tls/timeout/http_status/canceled/other）、阶段、HTTP 状态码、耗时 ms，不出现 topic/地址/正文。

**自动出题（3.5）**：`GET /api/practice/new?source=&n=1..20[&order=random|seq][&seed=N]`（需登录；`RUYI_AUTO_SOURCES` 为空时整个入口 404）。
返回 `{set_id:"auto-<8位>",url,title,requested,count,truncated,note?,expires_at}`；题不够 n 就全给并带 `note`；套存在内存里，`RUYI_AUTO_TTL` 后失效，其 qref 白名单同样约束判分与取图。新增 `mode=due_first|wrong_only|random`（缺省 = random，**默认关，仍需 `RUYI_AUTO_SOURCES`**）：`due_first`/`wrong_only` 走大脑 `pick-practice`，题文用 lookup 取；返回多 `mode`、`deferred`（带参考答案待核/不支持题型/缺图的题数）；每题带 `origin`（页面显示「复习」/「错题」）和 `ref_pending`（页面标「参考答案待核，不自动判」，是否保留由 `RUYI_PICK_SKIP_DEFERRED` 决定）。选题接口未部署 → 501「大脑接口未就绪」。旧参数 `wrong_only=1` / `due_first=1` 仍然接受（等价于对应 `mode`）；旧的"没有大脑接口就返回 501"的草稿路径已删除——有接口就用，没接口才 501。lookup 里没有的 qref 被丢弃，不崩。

## 待解答队列（S1，`RUYI_QUEUE_ENABLED`，默认关）

基于大脑接口契约 v1.0（`pending-answers` 入队、批量/单条读、`capabilities`、`events` + `ack`）。**开关关闭时站点行为与 #22 完全一致**（`golden_off_test.go` 用 #22 代码生成的黄金文件逐字节对照；`/api/practice/attempts/*` 不被路由，和旧版一样 400）。

- **入队**：提交判分后，`pending_key` / `needs_review` / `unsupported` 且 `queueable≠false` 的答案再入队（顶层 `session_ref` 一律是 `client_attempt_id`（单题也传，`session_total=1`）；`session_total` = 该 `session_ref` 下**实际入队**的题数（grade 已判掉 / 提示不可入队的不计；整批原子，被拒就按逐项 `queueable_reason` 剔除永久不可入队的题，`session_total` 相应减小后重发一次）。同一 `session_ref` 的 `session_total` 必须前后一致，否则大脑整个请求返 400：站点把第一次发出的值记在 `links.jsonl`，补发沿用它（只有「被剔除」才会减小，且那时大脑还没写入）；老记录没有这个值时按「已入队数 + 本次要发的数」算）。已判分（graded）的不入队；网站自判（本地套）不入队。每次调用 3 秒超时，整个提交最多多花 7 秒；入队失败**不影响提交成功**，之后由后台（30 秒一轮）自动重试：指数退避 1、2、4 分钟，之后每 5 分钟一次，**一直重试到这道题的处理时限**（数学 24 小时 / 开放题 48 小时；入队前不知道题型时按 48 小时），到点仍没排上就转 `retry_exhausted`（页面「暂时没能排队，答案已存档，家长会看到」，家长提醒里会写「N 题没能排队」）。页面触发的 `POST …/enqueue` 只是多一次幂等补发，**不占自动重试次数**，也不改自动重试的时间表；**没有手动按钮**。**大脑还没有入队端点（404，老大脑）**：站点判定「大脑未升级」，这些题直接记「未排队」（`not_ready`），不重试、不计失败次数，页面按「未排队」显示。
- **答案由入队写入时**（`unsupported` 在 grade 里不落库）：网站把它当作已记录、计入「今天约还剩 N 次」；剩余次数优先取入队响应里的 `daily_remaining`。
- **状态页数据**：`GET /api/practice/attempts/<cid>/queue`（纯读，永不入队；5 秒缓存；批量读 `GET /v1/brains/ruyi/pending?session_ref=<cid>&include=result`，路由未部署时退化为逐条读）。页面 15 秒轮询，5 分钟后 60 秒，标签页隐藏时不轮询，全部处理完停止。状态映射见 `internal/server/queue_view.go`（待解答（排队中）→ AI 判卷中 → **AI 初评 · 待家长核**（显示 AI 全文，含数学拟答案，不折叠）→ 家长已核；超时文案数学 24 小时 / 开放题 48 小时；purged = 「家长已删除这道题的解析」，不显示正文）。`POST /api/practice/attempts/<cid>/enqueue`（每个提交每分钟最多 2 次）只重发入队失败的那几题，幂等。
- **完成提醒**（`RUYI_QUEUE_NOTIFY_ENABLED=1`）：独立 goroutine 每约 45 秒（±10%）读 `GET /v1/queue/events`（**只用第三枚 notify 令牌**）。顺序固定：**本地落盘（fsync + 原子替换 `notify.json`）→ `ack` → 才发 ntfy**，每套（`session_ref`）只发一条；发之前先把「发送中」落盘，发成功再追加 `sent.jsonl`。唯一可能重复的窗口：ntfy 返回 2xx 但「已发送」还没落盘就崩溃。重启后从本地状态补发，不会漏。
- **本地状态丢失 / 损坏的对账**：`notify.json` 缺失而 `sent.jsonl` 或 `links.jsonl` 非空，或 `notify.json` 解析失败（改名为 `notify.json.corrupt-<unix时间>` 留存，不删），启动日志写 `reconciling`，对最近 4 天里**没发过**提醒的提交各读一次大脑（批量读）补齐终态，再按正常流程发（已在 `sent.jsonl` 的不会重发）。事件被大脑丢失/从未到达的提交，在预期截止 + 1 小时后也会触发同样的一次对账。`links.jsonl` 丢失时，旧提交的状态接口返回 404（页面不再显示状态；大脑那边的数据不受影响，家长仍可在大脑里核）。
- **令牌**：读 / 写（`RUYI_BRAIN_GRADE_TOKEN`，也是入队令牌）/ notify 两两必须不同，启动时校验，错误只写变量名；令牌不进日志、页面、归档、状态文件。notify 令牌 403 只打 warn，**不会回退到写令牌**。

- **「今天已满 3 次」（`daily_cap`）**：该题这次答案照常存档、不入队、没有 AI 初评；页面写「今天这题已经交满 3 次，这次答案先存档，没有 AI 初评；明天再交一次才会排队。」，同一提交里别的题照常入队。家长的提交提醒里多一句「N 题未排队（今天已满 3 次），答案在存档里」。
- **归档里的 `queue` 行是提交那一刻的快照**（`enq` / `item_id` / `state:"queued"` / 截止时间 / 原因），**不随处理进度更新**；实时状态看 `links.jsonl`（入队结果）和大脑（`state`）。要核对「这题后来怎样了」别看归档的 `queue.state`。
- **「今天约还剩 N 次」在补发窗口内可能与大脑不一致**：入队补发成功之前，这道 `unsupported` 答案还没写进大脑（站点不把它算进已用次数）；补发成功后才以入队响应里的 `daily_remaining` 为准。窗口内家长可能多看到 1 次余量，真正的 3 次上限只由大脑执行。

新增环境变量见 `deploy/ruyi-site.env.example` 和 `DEPLOY-P3.md` §9。

## 本地测试套（smoke-test）

让家长自己验证「提交 → 归档 + ntfy」而不碰真题、不碰大脑。`sets.json` 里的"本地套"：

```json
{"id":"smoke-test","local":true,"title":"…","questions":[{"qref":"smoke:q1","qtype":"single_choice","stem":"【测试题，随便填】…","options":[{"key":"A","text":"1"},{"key":"B","text":"2"}],"answer":["B"]}]}
```

- 校验（加载失败即启动退出，报错只写位置不写内容）：`id` 必须以 `smoke-` / `test-` 开头；必须有 `questions`、不得写 `qrefs`；qref 必须以 `smoke:` 开头；`source` 固定为保留值 `local.smoke`（可不写，写了必须相等）；`qtype` 只支持 `single_choice`（`answer` 是 1 个选项 key）和 `fill_blank`（`answer` 每个空一条，忽略首尾空格和大小写）。真实套不得使用 `smoke-`/`test-` id 前缀、`smoke:` qref 前缀、`local.smoke` source、`questions`。`local.smoke` 出现在 `RUYI_AUTO_SOURCES` / `RUYI_ALLOWED_SOURCES` 启动即退出。
- `GET /api/practice/{id}`：用内联题构造，**不调大脑**，响应多一个 `local:true`，**没有**答案字段。
- `POST /api/practice/{id}/check`（需登录，仅本地套，真实套 404）：`{"answers":[{"qref":"smoke:q1","answer":"B"}]}` → `{set_id,test:true,results:[{qref,status:"graded|unanswered",correct}]}`。现有页面**没有**客观题「检查」（对错只在提交后由大脑判分后显示），所以本地套的「检查」是新增的最小实现：服务端用 `answer` 字段自判，不归档、不推送、不调大脑。
- `POST /api/practice/{id}/grade`：**永不调 `Bank.Grade`**（即使配了判分令牌），也不调 lookup；固定 `mode:"archive_only"`、`brain_note:"test_set"`、`test:true`，逐题 `status:"graded"` + 服务端判的 `correct`、`recorded:false`；归档行带 `"test":true`；ntfy 标题（RFC 2047）和正文都以「【测试】」开头，正文 JSON 带 `"test":true`。每小时提交上限、idempotency、家庭码门禁、草稿 key `ruyi-practice-<set_id>-v1` 都沿用。
- 部署 / 回滚 / 识别测试提交：见 `DEPLOY-P3.md` §8；示例 `deploy/sets.smoke.example.json`（完整文件）、`deploy/sets.smoke-entry.json`（要追加的条目）。

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
- 站点自己不判对错：读令牌取不到标准答案（`include_answer` 无 `bank:write` 返回 403），服务端也绝不发该参数；对错只来自大脑判分接口的回复，拿不到就显示「待核对」。
