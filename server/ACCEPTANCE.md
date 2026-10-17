# 阶段 2 验收记录（服务端 + 动态页，针对 mock 大脑）

> 范围：Issue #1 验收标准 1–9，对象是**本分支的服务端与动态页**，大脑用 `cmd/mockbrain`（按对接约定的路径、字段、错误码实现，60 题、第 17 题无图、票据过期/失效场景）。
> **没有访问生产大脑，没有部署，没有 SSH ECS。** 真实大脑与 ECS 上的验收由 大脑测试 / ECS运维 完成。
> 本文件不含任何秘密：家庭码、令牌、会话密钥、ntfy topic 都是每次运行随机生成的临时值，脚本不打印它们；Set-Cookie 的值已省略。

## 怎么复现

```bash
cd server
make test                      # 单元测试（白名单、登录/限速/cookie、篡改/过期/改码、401、source 注入、日志脱敏、TLS、配置）
make build mockbrain           # dist/ruyi-site-linux-amd64、dist/mockbrain
SHOTS=/some/dir test/acceptance.sh   # 约 10 秒；SHOTS 非空且有 node + google-chrome 时追加无头浏览器检查并截图
```

`test/acceptance.sh` 起 mock 大脑（随机令牌）与新服务（随机家庭码/密钥/topic），静态目录 = `git archive origin/main` 解出的 17 个旧文件 + 动态页 + 故意放进去的 `server.log`、`server.pid`、`*.sh`、`.env`、`.git/config`，全程用 `curl` 访问。

## 验收标准对照

| # | 标准 | 用例（见下方日志分组） | 结果 |
|---|---|---|---|
| 1 | 登录后能看到图，图在题干下方 | D（API：60 题、60 张图全部 200 / PNG / `image/png`）；L（真实 Chrome 手机视口：登录表单→60 张卡片，60/60 张图加载成功，59/59 张图位于题干下方） | 通过 |
| 2 | 页面源码、所有响应（JS/JSON/头/Set-Cookie）搜不到令牌；令牌不在仓库 | F1–F6：对页面源码 + 全部已捕获响应（头+体）grep 令牌、`Bearer`/`Authorization`、票据形态、会话密钥、家庭码；`git grep` 仓库 | 通过 |
| 3 | 无票据/无 cookie 取图 401；伪造/过期票据同样 401 | C1–C6（无 cookie、带伪造 `?ticket=`、伪造 cookie 全部 401，且大脑零请求）；C18 篡改 cookie；E1–E7（上游票据过期/失效 → 服务端重取一次）；单元测试 `TestMediaTicketExpiredRetriesOnce`（连续两次 401 → 502，只重试一次） | 通过 |
| 4 | 旧页字节同 main | B1：17 个文件逐个 `sha256`，与 `git show origin/main:<path>` 一致；H6：大脑不可达时仍一致 | 通过 |
| 5 | 第 17 题显示「图未入库」，其余有图题全部显示 | D5/D6（API：无图题恰为 [17]，其余 59 题各 ≥1 图）；L（DOM：占位仅出现在「第 17 题」）；截图 `numberline-q17-placeholder.png` | 通过 |
| 6 | cookie 含 HttpOnly、SameSite=Lax；错码不发 cookie；改码后旧 cookie 失效 | C7–C17、G1–G11：错码 401 无 Set-Cookie；属性逐项断言；5 次失败后 429（右码也拒）；改 `RUYI_FAMILY_CODE`/`RUYI_SESSION_SECRET` 后旧 cookie 401，同配置重启后仍有效；`RUYI_COOKIE_SECURE=1` 加 Secure | 通过 |
| 7 | 票据过期：停留超过票期后刷新/重新点开，图仍能显示 | E：把 mock 时钟拨快 1 小时（缓存票据在上游已过期，上游真的回了 `ticket_expired`）→ 同一张图仍 200；再让上游轮换签名密钥（`ticket_invalid`）→ 仍 200；"刷新"后 60 张图全部 200。**注意：** 未实测真实 15 分钟等待，用 mock 时钟/密钥轮换走同一条 401→重取→重试路径 | 通过（模拟） |
| 8 | 服务端日志（含错误日志）查不到令牌/票据/家庭码 | K1–K5：对全部实例日志（300+ 行，含登录失败、大脑不可达、TLS 错误、限速）grep 令牌、两个家庭码、会话密钥、topic、cookie 值、票据形态、`Authorization`/`Cookie`/`?ticket=`；带 `?ticket=…`+`Authorization`+`Cookie` 头的请求不留痕。另有单元测试 `TestLogsContainNoSecrets`（原始日志，不经脱敏层）与 `TestErrorsCarryNoTicketOrURL` | 通过 |
| 9 | 大脑不可达：页面给可读提示，旧页不受影响，提交仍可用 | H1–H8：`/api/sets` → 502 + `brain_unavailable` + 「题库暂时连不上，请稍后再试」；`/healthz` 200；旧页 17 个字节不变；`/api/config`（含 ntfy 地址）仍可用；L2：浏览器里显示该提示 + 重试按钮（`numberline-brain-down.png`）。**说明：** 新动态页在大脑不可达时没有题目，因而无法在新页提交；"提交仍走 ntfy"指旧页，旧页提交是浏览器直连 ntfy，本服务不在链路上 | 通过 |

附加：A（白名单与目录遍历 404、`/days`→301、Range）、I（对大脑的 TLS：默认严格校验拒绝自签名；`RUYI_BRAIN_INSECURE_TLS=1` 可用；`RUYI_BRAIN_CA_FILE` 可用）、J（缺必需变量/非回环 INSECURE/二选一冲突 → 退出码 1，报错不含任何值）、G5（SIGTERM 优雅关闭）。

## 构建产物

```
$ file dist/ruyi-site-linux-amd64
dist/ruyi-site-linux-amd64: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped
$ ldd dist/ruyi-site-linux-amd64
not a dynamic executable
$ sha256sum dist/ruyi-site-linux-amd64
3fd666c8d63891af75ee2168d7fe30c99e677eb840b5792f72382e0b9bcf0afd  dist/ruyi-site-linux-amd64
```

同一源码、两次干净构建的 sha256 相同（`-trimpath -buildvcs=false`）。工具链 Go 1.26.9，`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`。
**未在真实的 CentOS 7.5 / 内核 3.10 上运行过**（box 上没有该环境）；依据是 Go 官方文档的最低内核要求（Go 1.24 及以后 ≥ 3.2）和静态链接。建议 ECS运维 先在目标机上空跑一次 `/opt/ruyi-site/ruyi-site`（缺环境变量时应打印配置错误并以 1 退出，说明二进制能在该内核上运行）。

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
     Set-Cookie (value elided): ruyi_sess=<value>; Path=/; Expires=Mon, 16 Nov 2026 08:02:18 GMT; Max-Age=2592000; HttpOnly; SameSite=Lax
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
PASS  D4 question numbers 1..60 in numeric order (mock list is reversed) (=true)
PASS  D5 no-media questions = exactly [17] (=[17])
PASS  D6 every other question has >=1 image (=59)
PASS  D7 image urls are rewritten to /api/media/{source}/{sha} (=true)
PASS  D8 each item carries a qref (=true)
PASS  D9 choice options passed through (Q1 has A-D) (=["A","B","C","D"])
PASS  D10 Q5 has stem image + option-B image (=[["stem",""],["option","B"]])
PASS  D11 all 60 images fetched: 200, PNG magic, Content-Type image/png
PASS  D12 image Cache-Control: private
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
PASS  E6 after expiry, reload fetches all 60 images (no broken) (=0)
PASS  E7 Authorization never sent on ticketed image GET (=0)

== F. Criterion 2 — no token / ticket / secrets in anything the browser can see ==
PASS  F1 bearer token not in any response/header/Set-Cookie/page source (31 captures)
PASS  F2 no 'Bearer'/'Authorization' strings in responses or page source
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
     log lines inspected: 302
PASS  K1 bearer token not in logs
PASS  K1 family code not in logs
PASS  K1 second family code not in logs
PASS  K1 session secret not in logs
PASS  K1 ntfy topic not in logs
PASS  K2 cookie value not in logs
PASS  K3 no ticket-shaped strings in logs
PASS  K4 request with ?ticket=…, Authorization and Cookie headers left no trace in logs (query/headers never logged)
PASS  K4b the request itself was logged (path only)
PASS  K5 failure events are logged (sanity: logs are not empty)
     sample log lines (verbatim):
       time=2026-10-17T16:02:18.187+08:00 level=WARN msg="login failed" ip=127.0.0.1
       time=2026-10-17T16:02:19.046+08:00 level=WARN msg="brain request failed" op=media-lookup err="media not in source"
       time=2026-10-17T16:02:17.529+08:00 level=DEBUG msg=request method=GET path=/healthz status=200 ms=0 ip=127.0.0.1

== L. Browser (headless Chrome, mobile viewport) — criteria 1, 5, 9, submit flow ==
PASS gate shown when not logged in
PASS wrong code shows message
PASS login through the form renders questions
PASS 60 question cards — cards=60
  dom: {"cards":60,"withImg":59,"images":60,"loaded":60,"broken":0,"placeholders":["第 17 题"],"below":59,"bad":[]}
PASS every image loaded (no broken images) — images=60 loaded=60
PASS images are below the stem — below=59/59
PASS only the no-media question shows 图未入库 — ["第 17 题"]
PASS draft saved under ruyi-mi-numberline-4y-v1 (answers keyed by qref) — answers=2
PASS family code not in localStorage/sessionStorage/document.cookie
PASS session cookie is HttpOnly (invisible to JS)
PASS submit posts one payload
PASS payload shape (type/learner/set/answers/qref, no family code) — answered=2/60
PASS ntfy url came from /api/config (https://ntfy.sh/…)
PASS logout shows gate again

14/14 browser checks passed
PASS  L1 browser checks (all passed)
PASS gate shown when not logged in
PASS wrong code shows message
PASS brain down: readable message shown
PASS brain down: no question cards, no sticky submit bar
PASS brain down: retry button offered

5/5 browser checks passed
PASS  L2 brain-down page shows readable notice (all passed)

TOTAL: 129 passed, 0 failed
```

## 未覆盖 / 需要后续验证

- 真实大脑的接口行为（本次是 mock；列表接口的路径与字段见 `README.md` 与 PR 的"开放问题"）。
- 真实 15 分钟票据到期；真实 4 MiB 级大图与慢网。
- iOS Safari / 微信内置浏览器（本次只有桌面 Chrome 的手机视口模拟）。
- ECS 上的 systemd 219 单元（`deploy/ruyi-site.service` 只按 219 文档核对过指令名，没在 219 上跑过）。
