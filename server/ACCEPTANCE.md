# 阶段 2 验收记录（服务端 + 动态页，针对 mock 大脑）

> 范围：Issue #1 验收标准 1–9，对象是**本分支的服务端与动态页**，大脑用 `cmd/mockbrain`（按对接约定与大脑开发的「以代码为准」答复实现：路径、字段、错误码；60 题、题号形如 `1(1)`/`11(2)③` 且乱序返回、`figure_status` 各取值与缺键、票据过期/失效场景）。
> **没有访问生产大脑，没有部署，没有 SSH ECS。** 真实大脑与 ECS 上的验收由 大脑测试 / ECS运维 完成。
> 对接约定已按 ruyi-learning-center `main`（提交 `e4b5584`）重读：票据/取图/错误码/`media` 恒为数组均与本实现一致，新增的"同一图多 ordinal""上传错误码""删除图片"与练习站无关。
> 本文件不含任何秘密：家庭码、令牌、会话密钥、ntfy topic 都是每次运行随机生成的临时值，脚本不打印它们；Set-Cookie 的值已省略。

## 重要说明：真实环境里整页都是占位

数轴卷目前 60 题的图**都还没入库**。对真实大脑运行时，每题 `media=[]` 且 `figure_status` 为 `missing` / `unknown`（或字段还没上线而缺键），页面会**整页显示灰底「图未入库」**，这是预期，不是故障。验收"见图"（标准 1/5/7）要等图入库后在真实环境复测；本记录用 mock 数据验证了页面逻辑（见下 L、M 段与截图 `numberline-all-missing.png`）。

## 缺图规则（页面）

| 数据 | 页面 |
|---|---|
| 有 media（stem/figure/option） | 显示图（`figure_status=none` 但有 media 也显示） |
| 无 media，`missing` / `unknown` / 缺键 / 其他值 | 灰底「图未入库」 |
| 无 media，`none` | 不显示任何占位 |
| 无 media，`ok`（数据不一致） | 灰底「图未入库」；服务端记一条 warn（仅 qref） |

mock 覆盖：序号 17=`missing`、25=`none`（无图）、26=缺键（无图）、27=`unknown`（无图）、28=`ok` 但无图、29=`none` 但有图、30=缺键且有图，其余 `ok`。

## 怎么复现

```bash
cd server
make test                      # 单元测试
make build mockbrain           # dist/ruyi-site-linux-amd64、dist/mockbrain
SHOTS=/some/dir test/acceptance.sh   # 约 12 秒；SHOTS 非空且有 node + google-chrome 时追加无头浏览器检查并截图
```

`test/acceptance.sh` 起 mock 大脑（随机令牌）与新服务（随机家庭码/密钥/topic），静态目录 = `git archive origin/main` 解出的 17 个旧文件 + 动态页 + 故意放进去的 `server.log`、`server.pid`、`*.sh`、`.env`、`.git/config`，全程用 `curl` 访问。

## 验收标准对照

| # | 标准 | 用例（见下方日志分组） | 结果 |
|---|---|---|---|
| 1 | 登录后能看到图，图在题干下方 | D（API：60 题、56 张图全部 200 / PNG / `image/png`）；L（真实 Chrome 手机视口：登录表单→60 张卡片，56/56 张图加载成功，55/55 张题干图位于题干下方） | 通过（mock 数据；真实卷待图入库后复测） |
| 2 | 页面源码、所有响应（JS/JSON/头/Set-Cookie）搜不到令牌；令牌不在仓库 | F1–F6 | 通过 |
| 3 | 无票据/无 cookie 取图 401；伪造/过期票据同样 401 | C1–C6、C18；E1–E7；单元测试 `TestMediaTicketExpiredRetriesOnce` | 通过 |
| 4 | 旧页字节同 main | B1：17 个文件逐个 `sha256` 与 `git show origin/main:<path>` 一致；H6：大脑不可达时仍一致 | 通过 |
| 5 | 缺图题显示「图未入库」，其余有图题全部显示 | D5/D6/D6b（无 media 的恰为序号 17/25/26/27/28；`figure_status` 原样透传，缺键→`unknown`）；L（占位恰在 17/26/27/28；25 无占位无图；29 有图无占位）；M/L3（全部无图时 60 题全是占位，无裂图）；单元 `TestFigureStatusPassThroughAndWarn`（含 `ok`+空 media 的 warn） | 通过 |
| 6 | cookie 含 HttpOnly、SameSite=Lax；错码不发 cookie；改码后旧 cookie 失效 | C7–C17、G1–G11 | 通过 |
| 7 | 票据过期后图仍能显示 | E：mock 时钟拨快 1 小时（上游真回 `ticket_expired`）→ 仍 200；上游轮换签名密钥（`ticket_invalid`）→ 仍 200；"刷新"后 56 张图全部 200。未等真实 15 分钟 | 通过（模拟） |
| 8 | 服务端日志查不到令牌/票据/家庭码 | K1–K6；单元 `TestLogsContainNoSecrets`、`TestErrorsCarryNoTicketOrURL` | 通过 |
| 9 | 大脑不可达：页面可读提示，旧页不受影响 | H1–H8；L2（浏览器里显示提示 + 重试按钮）。新动态页大脑不可达时无题目，无法在新页提交；"提交仍走 ntfy"指旧页（浏览器直连 ntfy，不经本服务） | 通过 |

## 本次按大脑开发答复新增的检查

| 项 | 用例 |
|---|---|
| 列表请求只发 `source`/`limit`/`cursor`（不发 `status`、`include_answer` 等） | 单元 `TestListQueryOnlySourceLimitCursor`（即使浏览器请求带 `include_answer=true&status=…`，也不会转发）、`TestPaginationAndFiltering`；mock 对其他参数回 400；D12b |
| 题号排序（大题, 小题, 后缀；解析不到的排最后且稳定） | 单元 `TestSortByMajorMinorSuffix`（`2(10)` 在 `2(2)` 之后、`11(2)①②③`、`weird`/缺失/`5` 排最后且保持原序、相同键稳定）；D4a（mock 原始列表确为乱序）、D4b–D4d（站点输出有序，60 个互不相同，大题 1..18，含 `11(2)③`，首 `1(1)` 末 `18(3)`）；L（页面显示库里的 `number` 原文） |
| `figure_status` 可选字段 | 单元 `TestFigureNormalization`（缺键/未知值→`unknown`）；D6b/D6c |
| 无答案 | D8b（输出无 answer/explanation 键）；服务端代码从不引用 `include_answer` |
| `options` 缺省整键省略 | D8a |

家庭码最短长度为 5 个字符：单元 `TestFamilyCodeMinLength`（5 字符通过，4 字符报错，报错只含变量名与 "min 5"，不回显值）。

附加：A（白名单与目录遍历 404）、I（对大脑的 TLS 三种模式）、J（启动校验，报错不含值）、G5（优雅关闭）。

## P0：通用套练习页（Issue #10）

脚本 `test/acceptance.sh` 的 P 段（服务端/静态/KaTeX/回归）和 L4/L5（无头 Chrome，手机视口 390×844）。全部用合成数据：mock 大脑多一个 source `math.synthetic.p0-demo`（5 道合成数学题，每题都带 answer/explanation/solution 字段，用来证明站点会剥掉）。

| 验收点 | 用例 | 结果 |
|---|---|---|
| 未设 `RUYI_SETS_FILE` → `/api/practice/*` 404，其余不变 | P1、P2 | 通过 |
| 未登录 401；非法 id 400；未知 id 404；穿越形 id 被拒 | P3–P5b | 通过 |
| 只返回 qrefs 白名单内的题（5 题里取 3 题），按配置顺序，ordinal 1..N | P7、P7b | 通过 |
| `missing_qrefs`（配置有、库里没有 → 200 不 500）；配置顺序压过大脑顺序 | P9 | 通过 |
| 响应无 answer/explanation/solution 字段；无令牌/Bearer/ticket；只含约定字段 | P10–P12 | 通过 |
| `blanks` 由服务端按「非公式部分 ≥4 个 `_`/`＿`」计数 | P8，Go 单测 `TestCountBlanks` | 通过 |
| 页面与 KaTeX 内嵌：静态目录里没有 `m/practice` 也能打开 | P14–P16 | 通过 |
| `/vendor/katex/` 只放行内嵌精确文件，其余 404（含 `..`、`%2e%2e`、ttf/woff、目录） | P17，Go 单测 | 通过 |
| 页面/CSS 无外部域名引用；CSS 只引用内嵌 woff2 | P18、P19、P19b | 通过 |
| 旧 17 个静态文件字节与 `origin/main` 一致；数轴页与 API 回归 | P20–P24（以及 B1/H6） | 通过 |
| 套文件有误 / 不存在 → 启动即退出，报错不含文件内容；示例文件可用 | P25–P27 | 通过 |
| 日志不含令牌/家庭码/qref/题干 | P28、P29 | 通过 |
| 公式渲染（`.katex` 15 个、无 `.katex-error`、$$ 块 2 个）；填空框 3/0/2；HTML 不执行 | L4（39 项） | 通过 |
| 草稿写入 `ruyi-practice-<set>-v1`、刷新恢复、清空；不含家庭码 | L4 | 通过 |
| 手机：无横向溢出、触控目标 ≥44px、输入框 ≥16px、请求全部同源 | L4 | 通过 |

截图（不入库）：`practice-top.png`、`practice-q1.png`、`practice-q1-filled.png`、`practice-q3.png`、`practice-free.png`。

## 构建产物

```
$ file dist/ruyi-site-linux-amd64
dist/ruyi-site-linux-amd64: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped
$ ldd dist/ruyi-site-linux-amd64
not a dynamic executable
$ sha256sum dist/ruyi-site-linux-amd64
c7d1dad9ceab7d832f4ac55c26056e0074792e4a9bffc84f144cf8c15eeead6c  dist/ruyi-site-linux-amd64
```

P0 起内嵌了页面和 KaTeX：二进制比 `main` 上的版本大约 644 KiB（7,663,776 → 8,323,232 字节，同一工具链对比）。

同一源码、两次干净构建（`make clean && make build`）的 sha256 相同（`-trimpath -buildvcs=false`）。工具链 Go 1.27.2，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`。
**未在真实的 CentOS 7.5 / 内核 3.10 上运行过**；依据是 Go 官方文档的最低内核要求（Go 1.24 及以后 ≥ 3.2）和静态链接。建议 ECS运维 先在目标机上空跑一次 `/opt/ruyi-site/ruyi-site`（缺环境变量时应打印配置错误并以 1 退出，说明二进制能在该内核上运行）。

## 完整运行日志（脚本输出，无秘密）

```text
== A. Static allowlist / hardening ==
PASS  A1 /healthz 200 (=200)
PASS  A2 / serves index.html (=200)
PASS  A3 / body == index.html (bytes)
PASS  A4 GET /server.log -> 404
PASS  A4 GET /server.pid -> 404
PASS  A4 GET /start-server.sh -> 404
PASS  A4 GET /stop-server.sh -> 404
PASS  A4 GET /.env -> 404
PASS  A4 GET /.git/config -> 404
PASS  A4 GET /.git/ -> 404
PASS  A4 GET /.git -> 404
PASS  A4 GET /../server.log -> 404
PASS  A4 GET /%2e%2e/server.log -> 404
PASS  A4 GET /%2E%2E/server.log -> 404
PASS  A4 GET /..%2fserver.log -> 404
PASS  A4 GET /units/x/../../server.log -> 404
PASS  A4 GET /units/ -> 404
PASS  A4 GET /units -> 404
PASS  A4 GET /m/ -> 404
PASS  A4 GET /m -> 404
PASS  A4 GET /days/../server.log -> 404
PASS  A4 GET //server.log -> 404
PASS  A4 GET /%00 -> 404
PASS  A4 GET /nonexistent -> 404
PASS  A5 /days -> 301 (=301)
PASS  A6 /days/ -> 200 (index) (=200)
PASS  A7 dynamic page served (=200)
PASS  A8 POST static -> 405 (=405)
PASS  A9 mp3 Range -> 206 (=206)

== B. Criterion 4 — old pages are byte-identical to origin/main (sha256 of each of the 17 files) ==
  ok   .nojekyll  e3b0c44298fc1c14…
  ok   analysis/index.html  0149fa239ae1ebc7…
  ok   days/2026-09-19.html  627eb6097babd56f…
  ok   days/2026-09-20-math-p1.html  059275c253753a52…
  ok   days/2026-09-20-u2.html  edddd8c3a65aa1d3…
  ok   days/2026-10-14-fl-water-redo.html  a73622e90e5dd022…
  ok   days/2026-10-15-evanmoor-w18d2.html  76bd1e5e07e84328…
  ok   days/index.html  7efed83643acefd5…
  ok   index.html  988658ea1d5b0963…
  ok   plan.html  af878baa1d2cc3ef…
  ok   units/evanmoor-w18d2-2026-10-15/index.html  4087b30a74c8b841…
  ok   units/fl-water-redo-2026-10-14/index.html  7647f943e6286609…
  ok   units/math-p1-2026-09-20/answer.html  f566cd0bce5604a3…
  ok   units/math-p1-2026-09-20/index.html  e243492ffef0e017…
  ok   units/u2-2026-09-20/answer.html  5c74833fd614425b…
  ok   units/u2-2026-09-20/index.html  dca7a7e652836f09…
  ok   units/u2-2026-09-20/listening.mp3  4eda7c4bed14da6a…
PASS  B1 all 17 files served byte-identical
PASS  B2 directory URL with index works (old links) (=200)

== C. Criteria 3 & 6 — auth ==
PASS  C1 sets without cookie -> 401 (=401)
PASS  C2 config without cookie -> 401 (=401)
PASS  C3 media without cookie -> 401 (=401)
PASS  C4 media with forged ticket param, no cookie -> 401 (=401)
PASS  C5 media with forged cookie -> 401 (=401)
PASS  C6 brain not contacted by any unauthenticated request (=0)
PASS  C7 wrong code -> 401 (=401)
PASS  C8 wrong code: no Set-Cookie header
PASS  C9 right code -> 200 (=200)
     Set-Cookie (value elided): ruyi_sess=<value>; Path=/; Expires=Mon, 16 Nov 2026 10:30:34 GMT; Max-Age=2592000; HttpOnly; SameSite=Lax
PASS  C10 cookie name ruyi_sess
PASS  C11 HttpOnly
PASS  C12 SameSite=Lax
PASS  C13 Path=/
PASS  C14 no Secure when RUYI_COOKIE_SECURE=0
PASS  C15 Max-Age = 720h (30 days)
PASS  C16 /api/session authenticated with cookie
PASS  C17 /api/session anonymous = false
PASS  C18 tampered cookie -> 401 (=401)
PASS  C19 genuine cookie -> 200 (=200)
PASS  C20 /api/config hands the ntfy url to a logged-in browser
PASS  C21 logout -> 200 (=200)
PASS  C22 logout clears the cookie

== D. Criteria 1, 5 — data/images through the proxy ==
PASS  D1 /api/sets 200 (=200)
PASS  D2 60 questions (=60)
PASS  D3 ordinals 1..60 in order (=true)
PASS  D4a mock list is NOT in number order (scrambled like updated_at DESC) (=true)
PASS  D4b site output IS sorted by (major, minor, suffix) (=true)
PASS  D4c 60 distinct numbers, majors 1..18, includes 11(2)③ (=true)
PASS  D4d first/last numbers (=["1(1)","18(3)"])
PASS  D5 questions with no media = ordinals [17,25,26,27,28] (=[17,25,26,27,28])
PASS  D6 questions with >=1 image (=55)
PASS  D6b figure_status passed through (absent key -> unknown) (=[[17,"missing"],[25,"none"],[26,"unknown"],[27,"unknown"],[29,"none"],[30,"unknown"]])
PASS  D6c figure_status is never empty (=true)
PASS  D7 image urls are rewritten to /api/media/{source}/{sha} (=true)
PASS  D8a items without options omit the key (fill_blank) (=false)
PASS  D8b no answer/explanation fields in output (=0)
PASS  D8 each item carries a qref (=true)
PASS  D9 choice options passed through (Q1 has A-D) (=["A","B","C","D"])
PASS  D10 Q5 has stem image + option-B image (=[["stem",""],["option","B"]])
PASS  D11 all 56 images fetched: 200, PNG magic, Content-Type image/png
PASS  D12 image Cache-Control: private
PASS  D12b client code never references include_answer / status / other list parameters
PASS  D13 sets source '..%2f..%2fv1%2fbank%2fsources' -> 404
PASS  D13 sets source 'a%2fb' -> 404
PASS  D13 sets source 'math-intake.zz-yuekao-numberline-4y%3Finclude_answer%3Dtrue' -> 404
PASS  D13 sets source 'MATH' -> 404
PASS  D13 sets source 'other.valid-source' -> 404
PASS  D13 sets source '%2e%2e' -> 404
PASS  D14 well-formed sha not in source -> 404 (=404)
PASS  D15 sha path traversal -> 404 (=404)

== E. Criterion 7 — expired/invalid ticket does not break images ==
PASS  E1 image still 200 after tickets expired upstream (=200)
PASS  E2 bytes are a PNG
PASS  E3 brain really answered 401 ticket_expired to the cached ticket
PASS  E4 server fetched a fresh ticket and retried once (+1 ticket)
PASS  E5 image still 200 after brain rotated its ticket key (ticket_invalid -> refresh) (=200)
PASS  E6 after expiry, reload fetches all 56 images (no broken) (=0)
PASS  E7 Authorization never sent on ticketed image GET (=0)

== F. Criterion 2 — no token / ticket / secrets in anything the browser can see ==
PASS  F1 bearer token not in any response/header/Set-Cookie/page source (31 captures)
PASS  F2 no 'Bearer'/'Authorization' strings in responses or page source
grep: /tmp/tmp.ELIIXhHEVU/all-fresh/_api_media_math-intake.zz-yuekao-numberline-4y_19657038d590051de9672997cd2d38902c52a55392e28d7bc9410950d854e95a.raw: binary file matches
PASS  F3 no ticket-shaped string (v1.<exp>.<mac>) in page/JSON/headers/bodies (our own session cookie in Set-Cookie excluded: it is the HMAC cookie, not a brain ticket)
PASS  F4 session secret not in any response
PASS  F5 family code not in page source or JSON
PASS  F6 run-time secrets not in the repo (tracked files)

== G. Rate limit / code change (Criterion 6) ==
PASS  G1 wrong codes x7: 5x401 then 429 (=401 401 401 401 401 429 429)
PASS  G2 429 carries Retry-After
PASS  G3 even the right code is refused while limited (=429)
PASS  G4 429 sets no cookie
PASS  G5 SIGTERM: graceful shutdown logged (exit 0)
PASS  G6 old cookie after RUYI_FAMILY_CODE change -> 401 (=401)
PASS  G7 old code no longer accepted (=401)
PASS  G8 new code accepted (=200)
PASS  G9 cookie after RUYI_SESSION_SECRET change -> 401 (=401)
PASS  G10 cookie survives restart with identical config (=200)
PASS  G11 RUYI_COOKIE_SECURE=1 adds Secure

== H. Criterion 9 — brain unreachable ==
PASS  H1 login still works (=200)
PASS  H2 /api/sets -> 502 (=502)
PASS  H3 readable Chinese message + code in JSON
PASS  H4 /healthz 200 without brain (=200)
PASS  H5 /api/config (ntfy url for submit) works without brain
PASS  H6 all 17 old pages still byte-identical while brain is down (diffs) (=0)
PASS  H7 dynamic page itself still served (=200)
PASS  H8 page source has readable brain-error handling (see browser test for rendering)

== I. TLS to the brain (self-signed, loopback like production) ==
PASS  I1 strict verification (default) rejects self-signed -> 502 (=502)
PASS  I2 log says 'brain tls error' (no URL/ticket)
PASS  I3 RUYI_BRAIN_INSECURE_TLS=1 works with self-signed (=200)
PASS  I4 RUYI_BRAIN_CA_FILE works with the mock CA (=200)
PASS  I5 image over TLS via CA file (=200)

== J. Startup validation (no values in errors) ==
PASS  J1 missing RUYI_STATIC_DIR -> exit 1, names the variable, prints no values
PASS  J1 missing RUYI_FAMILY_CODE -> exit 1, names the variable, prints no values
PASS  J1 missing RUYI_SESSION_SECRET -> exit 1, names the variable, prints no values
PASS  J1 missing RUYI_BRAIN_BASE_URL -> exit 1, names the variable, prints no values
PASS  J1 missing RUYI_BRAIN_TOKEN -> exit 1, names the variable, prints no values
PASS  J2 INSECURE_TLS refused for a non-loopback brain
PASS  J3 INSECURE_TLS + CA_FILE together refused

== K. Criterion 8 — server logs ==
     log lines inspected: 298
PASS  K1 bearer token not in logs
PASS  K1 family code not in logs
PASS  K1 second family code not in logs
PASS  K1 session secret not in logs
PASS  K1 ntfy topic not in logs
PASS  K2 cookie value not in logs
PASS  K3 no ticket-shaped strings in logs
PASS  K4 request with ?ticket=…, Authorization and Cookie headers left no trace in logs (query/headers never logged)
PASS  K4b the request itself was logged (path only)
PASS  K6 inconsistent figure_status=ok/no media produced a warn log (qref only)
PASS  K5 failure events are logged (sanity: logs are not empty)
     sample log lines (verbatim):
       time=2026-10-17T18:30:34.109+08:00 level=WARN msg="login failed" ip=127.0.0.1
       time=2026-10-17T18:30:34.784+08:00 level=WARN msg="brain request failed" op=media-lookup err="media not in source"
       time=2026-10-17T18:30:33.617+08:00 level=DEBUG msg=request method=GET path=/healthz status=200 ms=0 ip=127.0.0.1

== P. P0 practice page (Issue #10) — sets file, /api/practice, embedded page + KaTeX ==
PASS  P1 no RUYI_SETS_FILE: /api/practice/demo-1 -> 404 (even logged in) (=404)
PASS  P2 no RUYI_SETS_FILE: old /api/sets still 200 (=200)
PASS  P3 anonymous /api/practice/demo-1 -> 401 (=401)
PASS  P3b anonymous + illegal id -> still 401 (login first) (=401)
PASS  P4 illegal set id -> 400 (=400)
PASS  P5 unknown set id -> 404 (=404)
PASS  P5b traversal-shaped id refused (400)
PASS  P6 demo-1 -> 200 (=200)
PASS  P7 whitelist + config order (3 questions of 5 in the brain; alternates excluded) (=example.set:math:1,example.set:math:2,example.set:math:3)
PASS  P7b ordinals 1..N, set_id/title, empty missing_qrefs (=[[1,2,3],"demo-1","演示套",[]])
PASS  P8 blanks counted by the server (3 / none / 2, full-width included) (=[3,0,2])
PASS  P8b options delivered as {key,text_md} (=["A","B","C","D"])
PASS  P9 config order beats brain order; configured-but-absent qref -> missing_qrefs (200, not 500) (=[["example.set:math:3","example.set:math:1"],["example.set:math:99"]])
PASS  P10 d1 has no answer/explanation/solution (mock sends all three on every question)
PASS  P10 d2 has no answer/explanation/solution (mock sends all three on every question)
PASS  P10b control: the mock really does send the answer marker upstream
PASS  P11 allowed item keys only (=["blanks","figure_status","media","options","ordinal","qref","qtype","stem_md"])
PASS  P12 no token / Bearer / ticket in the practice response or headers
PASS  P13 mock saw the practice source list requests (only source/limit/cursor accepted by the mock, else 400) (=true)
PASS  P14 the static dir has no m/practice (nothing hand-copied to the server)
PASS  P15 /m/practice/ -> 200 without login (page holds no data) (=200)
PASS  P15b html content-type + restrictive CSP
PASS  P15c /m/practice -> 301 (=301)
PASS  P16 /vendor/katex/katex.min.js (=200)
PASS  P16 /vendor/katex/katex.min.css (=200)
PASS  P16 /vendor/katex/LICENSE (=200)
PASS  P16 /vendor/katex/fonts/KaTeX_Main-Regular.woff2 (=200)
PASS  P16 /vendor/katex/fonts/KaTeX_Math-Italic.woff2 (=200)
PASS  P17 /vendor/ -> 404
PASS  P17 /vendor/katex/ -> 404
PASS  P17 /vendor/katex/fonts/ -> 404
PASS  P17 /vendor/katex/fonts/KaTeX_Main-Regular.ttf -> 404
PASS  P17 /vendor/katex/fonts/KaTeX_Main-Regular.woff -> 404
PASS  P17 /vendor/katex/nope.js -> 404
PASS  P17 /vendor/katex/../katex/katex.min.js -> 404
PASS  P17 /vendor/katex/%2e%2e/katex.min.js -> 404
PASS  P17 /vendor/other/x.js -> 404
PASS  P17 /vendor/modules.txt -> 404
PASS  P18 page source: no http(s):// or protocol-relative references (only site-relative paths)
PASS  P19 katex css: no external references; fonts only /vendor/katex/fonts/*.woff2
PASS  P19b every css url() is an embedded woff2
PASS  P20 all 17 old static files still byte-identical to origin/main (diffs) (=0)
PASS  P21 numberline page still served (=200)
PASS  P21b numberline page byte-identical to the repo file
PASS  P22 numberline API unchanged: 60 questions (=60)
PASS  P23 a set-only source is NOT listable through the old /api/sets (=404)
PASS  P24 junk files still 404 (=404)
PASS  P25 invalid sets file -> exit 1, names RUYI_SETS_FILE, echoes none of its content
PASS  P26 missing sets file -> refused
PASS  P27 deploy/sets.example.json is accepted at startup (kept running until the 2 s timeout)
PASS  P28 bearer token not in the practice-site log
PASS  P28 family code not in the practice-site log
PASS  P28 session secret not in the practice-site log
PASS  P28 ntfy topic not in the practice-site log
PASS  P28 qref not in the practice-site log
PASS  P28 answer marker not in the practice-site log
PASS  P28 stem text not in the practice-site log
PASS  P29 log has the set count and a count-only warning for the missing qref

== L. Browser (headless Chrome, mobile viewport) — criteria 1, 5, 9, submit flow ==
PASS gate shown when not logged in
PASS wrong code shows message
PASS login through the form renders questions
PASS 60 question cards — cards=60
  dom: {"cards":60,"withImg":55,"images":56,"loaded":56,"broken":0,"placeholders":["q17","q26","q27","q28"],"below":55,"bad":[],"q25_img":false,"q25_noimg":false,"q29_img":true,"q29_noimg":false,"labels":["第 1(1) 题","第 11(2)① 题","第 11(2)③ 题","第 18(3) 题"]}
PASS every image loaded (no broken images) — images=56 loaded=56
PASS images are below the stem — below=55/55
PASS placeholder 「图未入库」 only for missing / unknown / absent-key / ok-but-empty (ordinals 17,26,27,28) — ["q17","q26","q27","q28"]
PASS figure_status=none, no media: no placeholder and no image (ordinal 25)
PASS figure_status=none but media present: image still shown, no placeholder (ordinal 29)
PASS question numbers shown verbatim from the library, in sorted order — ["第 1(1) 题","第 11(2)① 题","第 11(2)③ 题","第 18(3) 题"]
PASS draft saved under ruyi-mi-numberline-4y-v1 (answers keyed by qref) — answers=2
PASS family code not in localStorage/sessionStorage/document.cookie
PASS session cookie is HttpOnly (invisible to JS)
PASS submit posts one payload
PASS payload shape (type/learner/set/answers/qref, no family code) — answered=2/60
PASS ntfy url came from /api/config (https://ntfy.sh/…)
PASS logout shows gate again

17/17 browser checks passed
PASS  L1 browser checks (all passed)
PASS  M1 brain has no figures yet (today's real state): 60 questions, all with empty media (=[60,60])
PASS gate shown when not logged in
PASS wrong code shows message
PASS login through the form renders questions
PASS no figures ingested: all 60 questions render with 「图未入库」 placeholders, no images — {"cards":60,"ph":60,"imgs":0}

4/4 browser checks passed
PASS  L3 whole page of placeholders when no figure is ingested (all passed)
PASS gate shown when not logged in
PASS wrong code shows message
PASS brain down: readable message shown
PASS brain down: no question cards, no sticky submit bar
PASS brain down: retry button offered

5/5 browser checks passed
PASS  L2 brain-down page shows readable notice (all passed)
PASS no ?set= : friendly message
PASS illegal set id: friendly message, no cards
PASS gate shown when not logged in
PASS wrong code shows message
PASS login through the form renders 3 question cards
  dom: {"title":"演示套","docTitle":"演示套 · 如意","tex":15,"katex":15,"display":2,"err":0,"raw":0,"mathml":15,"blanks":[3,0,2],"radios":[0,4,0],"areas":[0,0,0],"script":0,"pwned":"undefined","q3code":["x<y"],"q3strong":["解方程组"],"optsHaveKatex":[true,true,true,true],"noOptionRawDollar":true,"stemRawDollar":false,"qno":["第 1 题","第 2 题","第 3 题"],"submit":{"disabled":true,"text":"提交（即将开放）","shown":true},"noteOnPage":true,"verdicts":0,"fontOk":true,"sameOrigin":true,"resources":["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]}
PASS title from the set config — 演示套
PASS formulas rendered by local KaTeX (.katex present, none left as raw) — tex=15 katex=15 raw=0
PASS no .katex-error anywhere
PASS $$ blocks render as display math (2) — display=2
PASS no raw $ delimiters left in stems or options
PASS fill-in boxes: one per underscore run (3, 0, 2) — [3,0,2]
PASS single-choice: 4 radios on question 2 only — [0,4,0]
PASS options render math too
PASS no free-form answer area on questions that have blanks/options
PASS HTML is escaped, never executed (<script> text shown, no element, no side effect)
PASS Markdown: `code` and **bold** rendered; x<y kept literally — [["x<y"],["解方程组"]]
PASS question labels are plain ordinals
PASS submit button is disabled and says 即将开放; page explains draft-only — {"disabled":true,"text":"提交（即将开放）","shown":true}
PASS no correct/incorrect UI exists
PASS KaTeX woff2 font actually loaded from this origin
PASS every request is same-origin (nothing from a CDN) — ["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]
PASS only the embedded files were fetched (page assets) — ["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]
  layout: {"scrollW":390,"iw":390,"small":0,"minFont":18,"body":18,"wide":0,"blankW":135}
PASS no horizontal page overflow at 390px — {"scrollW":390,"iw":390,"small":0,"minFont":18,"body":18,"wide":0,"blankW":135}
PASS touch targets >= 44px (blanks, options, buttons) — too small: 0
PASS inputs use >= 16px font (no iOS zoom); body is 18px — minFont=18 body=18
PASS blank boxes are wide (>= 110px) — min width 135
  draft keys: ["v","set","a","t"] ls keys: ["ruyi-practice-demo-1-v1"]
PASS draft saved under ruyi-practice-demo-1-v1 with set, timestamp and qref-keyed answers — {"example.set:math:1":["5/4","","-3"],"example.set:math:2":"B","example.set:math:3":["","y=1"]}
PASS family code not in localStorage / sessionStorage / document.cookie
PASS session cookie is HttpOnly (invisible to JS)
PASS progress shows answered count
PASS after reload (cookie kept) the cards come back without the gate
PASS draft restored after reload (blanks and the chosen option) — {"q1":["5/4","","-3"],"q2":[false,true,false,false],"q2sel":1,"q3":["","y=1"]}
PASS 「清空」 removes the draft and the inputs — {"ls":null,"vals":"","checked":0}
PASS config order wins (3 then 1) and the missing qref only yields a notice
PASS free-form question gets a wide answer area (textarea)
PASS textarea answer saved as a string under ruyi-practice-free-3-v1 — {"example.set:math:4":"1/2+1/3=5/6\n过程：通分"}
PASS answer area is tall and full-width on a phone — {"h":173.984375,"w":342,"iw":390}
PASS unknown set: friendly message, no cards
PASS logout shows the gate and removes the questions

39/39 browser checks passed
PASS  L4 practice page in headless Chrome (all passed)
PASS  L5 no code/token in the log of the browser-driven practice run

TOTAL: 200 passed, 0 failed
```

## 未覆盖 / 需要后续验证

- 真实大脑的接口行为（本次是 mock）；真实卷的 `number` 形态与 60 题互不相同需在真实数据上复核。
- 真实 15 分钟票据到期；真实 4 MiB 级大图与慢网。
- iOS Safari / 微信内置浏览器（本次只有桌面 Chrome 的手机视口模拟）。
- ECS 上的 systemd 219 单元（只按 219 文档核对过指令名，没在 219 上跑过）。
