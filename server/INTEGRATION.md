# INTEGRATION：`release/integ-1017` 集成构建说明

本文件只是**说明**（不含任何真实值，占位用 `<…>`；功能代码全部来自下表各 PR，集成时没有夹带新功能）。分支从 `origin/main`（`dcb1668`）起，按下面的顺序用 `git merge --no-ff` 逐个并入；**不合并到 main，不部署**。

## 1. PR → 分支 → head → 集成分支中的 merge commit

| 顺序 | PR | 分支 | 并入时的 head commit | 集成分支中的 merge commit | 冲突 |
|---|---|---|---|---|---|
| 1 | #11 | `feat/practice-set-p0` | `a89f02d` | `c0379f7` | 无 |
| 2 | #12 | `feat/practice-set-p1-p3` | `125c03e` | `be35b8f` | 无 |
| 3 | #14 | `fix/ntfy-config-base` | `fa71b21` | `531c4f9` | 无 |
| 4 | #13 | `feat/brain-contract-adapter` | `e3bb61b`（含 `565ce9b`、`e6819d9`） | `dbda083` | 无 |
| 5 | #16 | `feat/local-smoke-set` | `9f2d2a5` | `fac1db7` | 有（§3-1） |
| 6 | #17 | `feat/brain-id-config` | `f4631e3` | `957548a` | 有（§3-2、§3-3） |

依赖：#12 基于 #11；#14、#13 基于 #12；#16、#17 基于 #13（互为兄弟）。每个 PR 的原始提交都在集成分支历史里（`git log --first-parent` 看 merge，`git log --graph` 看原提交）。

merge 之后的提交（都不改功能）：

| commit | 内容 |
|---|---|
| `test(acceptance)…`（`cfc2800`） | 修 #16 与 #17 合并后验收脚本的互相覆盖（§3-3），仅测试脚本 |
| `docs(deploy)…`（`53630df`） | `DEPLOY-P3.md` §6 回滚加一句指向 §8.3；§8.2 加「每次重启 1 次只读 lookup 探测」 |
| `docs/test: replace real brain identifiers…`（`2fa464f`） | PM 拍板：`deploy/sets.qa-practice.example.json` 里的真实 source key / qref 换成占位值（`<qa-practice-source>`、`<qa-practice-source-2>`、`<qa-practice-source-3>`、`<qref-lowercase-1…7>`）；`internal/sets/sets_test.go` 的样例加载单测与 `test/acceptance.sh` 的 U4i–U4l 改为先校验模板结构（套 id、占位数量与编号）、再把占位值替换成夹具值（`qa-fixture.src…`）后走真实校验并启动加载；`README.md` 说明「占位值，使用前需替换成大脑给的真实 source key 和小写 qref；联调用，非线上」。只改样例 / 测试 / 说明，二进制不变 |
| `docs: INTEGRATION.md` | 本文件（本文件的提交就是「到此为止」的最后一个提交；最终 head 以分支 head / 交付报告为准，文中不写自身 hash） |

## 2. 合并顺序

#11 → #12 → #14 → #13 → #16 → #17（#13 在 #14 之后并入，没有文本冲突；#14 的 ntfy 基址改动与 #13、#16 对 `grade.go` / `server.go` 的改动自动合并，之后全套测试通过）。

## 3. 冲突解决记录（只做「保留双方意图」的最小合并）

1. **#16 并入**：`server/internal/config/grade_config_test.go` —— 两边都在文件末尾新增测试函数（#14 的 `TestNtfyBaseURLTrailingSlash`；#16 的 `TestReservedLocalSourceRefused`、`TestSetsFileLocalSetFailsAtLoad`）。解决：**两边的测试函数全部保留**，顺序为 #14 的在前、#16 的在后，不改任何断言。
2. **#17 并入**：`server/internal/config/grade_config_test.go` —— 同样是末尾追加冲突（#17 的 `TestBrainIDUnsetStaysEmpty`、`TestBrainIDAccepts`、`TestBrainIDRejectsAndNeverEchoesTheValue`）。解决：保留已有的全部测试，把 #17 的三个函数原样追加在文件末尾。
3. **#17 并入**：`server/test/acceptance.sh` —— 两边都新增了一个 `section "T. …"`（#16：本地套 T1–T22；#17：RUYI_BRAIN_ID）。解决：#16 的 T 段原样保留；#17 的段改名为 **`U.`**，其测试编号 `T1…T12` 改为 `U1…U12`（只改显示用的名字），两段先后顺序执行。随后发现 #17 的段会把 `$WORK/sets-t.json` 覆盖掉（该文件后面还被 #16 的浏览器用例 L9 使用，导致 L9/L9c 失败），于是单独用一个 `test(acceptance)` 提交把 #17 段里的文件名改为 `sets-u.json`。没有改任何断言内容。
4. `grade.go`（postNtfy / ntfy 标题）、`server.go`、`README.md`、`DEPLOY-P3.md`、`config.go`、`testenv_test.go`：git 自动合并，无文本冲突；合并后 `go build / vet / test` 全绿，ntfy 标题（#14 基址 + #16 的 `【测试】` RFC 2047 标题）两者行为都保留（有测试和验收覆盖）。

5. **真实题标识已清除（`2fa464f`）**：#17 的样例文件曾含真实 source key / qref，现已换成占位值；当前分支树中不再出现这些标识。**但 #17 的提交 `f4631e3`（及公开分支 `feat/brain-id-config`、本分支经 merge #17 带入的历史）里仍含旧值**，只有 force-push 改写历史才能抹掉，未获授权，所以没做。

不需要重新生成 `static-allowlist.txt` / vendor（未运行 `scripts/gen-allowlist.sh`；`allowlist_test.go` 通过）。

## 4. 最终二进制

- 路径：`/workspace/ruyi-plan/dist/ruyi-site-integ-linux-amd64`（+ `.sha256`），在构建机上，不进仓库。
- sha256：`8d7abf3565b6a15971d6710db520872656695085f593e9724466bce0a58d3aa1`
- 构建参数（同 `Makefile` 的 `build` 目标）：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '-s -w' -o <out> ./cmd/ruyi-site`（在 `server/` 下）。
- Go：`go1.27.2 linux/amd64`（`go.mod` 写的是 `go 1.26.0` / `toolchain go1.26.9`；本机 Go 更新，没有降级使用 1.26.9）。同源两次构建 sha256 一致；`ldd` 为 not a dynamic executable。
- 样例 / 测试 / README 的占位值改动（`2fa464f`）之后实际重编：sha256 **与上一版相同**（`8d7abf35…`），因为这些文件都不进二进制（二进制嵌入的只有 `static-allowlist.txt`、`web/`、`vendor/katex`）。
- 注意：这个 sha 只对应「Go 1.27.2 + 本分支内容」；换 Go 版本重编会得到不同的 sha。

## 5. 与线上现用二进制（#11+#12+#14，sha `1cc0c775…`）的差异

新增（来自 #13、#16、#17；#11/#12/#14 的内容线上已有）：

- **契约适配（#13）**：判分 `POST /v1/bank/grade`、取题 `POST /v1/bank/questions/lookup`、选题 `GET /v1/brain/pick-practice` 按大脑「最终契约」对接；错误码映射（401/403/404/409 `rev_mismatch`/409 `idempotency_conflict`/429 当日上限/501/5xx → 页面状态，永不 5xx、不谎称「已记入大脑」）；`rev` 过期提示「题目已更新，请刷新」；同一份答案重试沿用同一 `client_attempt_id` 与 `answered_at`；`unsupported` 的题 `recorded:false`；接口未部署时（404/501）按 not_ready 降级为仅留档。
- **pick 改用判分令牌（#17，`f4631e3`）**：`due_first` / `wrong_only` 的 pick-practice 请求用 `RUYI_BRAIN_GRADE_TOKEN`（大脑对 pick 要 write scope，只读令牌会 403）；**没配判分令牌时 pick 直接 501「not ready」，不会退回用只读令牌**。lookup / 列表 / 图片仍用 `RUYI_BRAIN_TOKEN`。
- **`RUYI_BRAIN_ID`（#17）**：判分 body 的 `brain` 与 pick 查询的 `brain` 可配，默认 `ruyi`，联调用 `qa-practice`；非法值启动即退出且不回显。站点 URL 上的 `?brain=` 一律忽略。
- **本地测试套（#16）**：`sets.json` 支持 `"local": true` + `questions`（`smoke-test` 套，题干「测试题，随便填」）；新增 `POST /api/practice/<id>/check`（服务端判对错，答案不下发）；提交固定 `archive_only` / `brain_note: test_set`，**提交时 0 次调大脑**；归档行 `test:true`；ntfy 标题/正文带「【测试】」；保留 source `local.smoke`（写进 `RUYI_AUTO_SOURCES` / `RUYI_ALLOWED_SOURCES` 会启动即退出）。
- **启动探测**：配了 `RUYI_SETS_FILE` 时，启动后异步做 1 次只读 lookup 探测（明显不存在的 qref），只写日志，不影响启动。
- **升级时的兼容性提醒**：旧二进制遇到 `sets.json` 里的 smoke 条目会启动失败，回滚前须先删除该条目（`DEPLOY-P3.md` §6 / §8.3）。

新增环境变量（**全部可选**，不配 = 默认值；线上现有变量一个都不用改）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `RUYI_BRAIN_ID` | `ruyi` | 判分 body / pick 查询里的大脑 id；格式 `^[a-z0-9][a-z0-9_-]{0,63}$` |
| `RUYI_BRAIN_LOOKUP_PATH` | `/v1/bank/questions/lookup` | lookup 路径，大脑改路径时才动 |
| `RUYI_BRAIN_PICK_PATH` | `/v1/brain/pick-practice` | pick 路径，同上 |
| `RUYI_PICK_SKIP_DEFERRED` | `0` | `1` = 丢掉大脑标 `deferred:*` 的选题；默认保留并标注 |

已有变量的语义变化（来自 #17）：`RUYI_BRAIN_GRADE_TOKEN` 现在**同时**用于判分和 pick-practice（见上；#13 的 pick 用的是读令牌，#17 才改），其范围须同时满足两者，且仍必须与 `RUYI_BRAIN_TOKEN` 不同。

## 5.1 上线 / 回滚注意

- **`ntfy_url` 依赖线上 `RUYI_NTFY_BASE_URL`（#14）**：登录后 `/api/config` 返回的 `ntfy_url` 是 `RUYI_NTFY_BASE_URL` + topic。线上要显示 `ntfy-me.com` 那个地址，必须在线上 env 里已设 `RUYI_NTFY_BASE_URL`（`ntfy-me.com` 只是本仓库测试里的占位基址）；**不设则默认 `https://ntfy.sh/`**，`ntfy_url` 就会指向 ntfy.sh。上线前先核对线上 env。
- **回滚前必须先删 `sets.json` 里的 `smoke-test` 条目**：旧二进制不认识 `local` / `questions` 字段，会启动即退出（`DEPLOY-P3.md` §6 / §8.3）。
- **回滚后浏览器若还开着集成版页面**：集成版页面提交时会带 `answered_at`（和 `rev`），旧二进制严格解析、不认识这些字段，返回 400「提交内容格式不对」（已用旧二进制 `1cc0c775…` 实测）；**刷新页面**（加载旧二进制自带的旧版页面）即可。

## 6. 验证摘要

`gofmt -l`（不含 vendor）无输出；`go vet ./...` 通过；`CGO_ENABLED=0 go test ./... -count=1` 全包通过；`CGO_ENABLED=1 go test -race ./... -count=1` 全包通过；`test/acceptance.sh`（带 SHOTS 与 headless Chrome、mockbrain）全过。另跑了集成专项断言：`/api/config` 的 `ntfy_url` 跟随 `RUYI_NTFY_BASE_URL`；本地套提交 0 次调大脑；`RUYI_BRAIN_ID=qa-practice` 时 grade body / pick 查询的 brain 正确、pick 用判分令牌；契约错误映射；对 main 新增行的公开库安全 grep。

`2fa464f` 之后全部重跑并通过：gofmt、vet、`go test`、`-race`、acceptance（458 通过 / 0 失败，其中样例模板检查为 U4i–U4l 四条）。

## 7. `release/integ-1017b`（= `release/integ-1017` + #18 + #19）

`release/integ-1017` 本身未改动（head `ef90301`）。`release/integ-1017b` 从它起，依次 `--no-ff` 并入两个 PR，**无冲突**：

| 顺序 | PR | 分支 | 并入时的 head | 内容 |
|---|---|---|---|---|
| 1 | #18 | `feat/answer-limit-2000` | `abd17c2` | 自由作答上限 500 → 2000 字符（Unicode 码点；`brain.MaxAnswerLen`） |
| 2 | #19 | `feat/ntfy-async-concise` | `6f805e5` | 家长通知异步（提交最多等 1 s）、单次 10 s 超时、最多 3 次尝试、正文简洁、失败日志带类别与耗时、关机排空 12 s |

- 二进制：`/workspace/ruyi-plan/dist/ruyi-site-integ1017b-linux-amd64`（+ `.sha256`），构建机上，不进仓库；构建参数同 §4，Go `go1.27.2`。**sha256 以 `.sha256` 文件与交付报告为准**（文中不写自身 hash）。
- 与 `ruyi-site-integ-linux-amd64` 的行为差异只有上表两项；没有新增环境变量（`RUYI_NTFY_RETRY_DELAY` 含义变为「第一次重试前的等待，第二次翻倍」）。
- 验证：`gofmt`、`go vet`、`go test`、`CGO_ENABLED=1 go test -race`、`test/acceptance.sh`（`SHOTS=<dir>`，含无头 Chrome 页面检查）462 通过 / 0 失败。
- 部署提示：关机时 `ruyi-site` 会再等最多 12 s 排空通知；systemd 默认 `TimeoutStopSec=90s` 足够。线上日志出现 `ntfy attempt failed … kind=… phase=… ms=…` 即可区分 TCP 卡住 / TLS 卡住 / 超时 / HTTP 状态码。

## 后续（单 E，不属于上面的合并清单）

`feat/daily-limit-ux`（基于 `release/integ-1017b` @ `29ff748`）：每题每天 3 次的页面提示 / 二次确认、大脑 429 `daily_cap` 也留档、`GET /api/practice/<set>/quota`、「（不入大脑）」文案。需换二进制并重启；无新环境变量。详见该 PR 与 `README.md`「大脑的拒绝映射」。

## 8. S1 `feat/queue-s1`（基于 `feat/daily-limit-ux` #22；依赖链 #18/#19 → #22 → S1）

- 依据：`/workspace/loop/ruyi-brain/site-interface-contract.md`（大脑接口契约 v1.0）。实现、mock（`internal/mockqueue`，`cmd/mockbrain` 在 `MOCK_QUEUE=1` 时启用）和测试都按它对齐。
- 范围：入队 + 状态页 + 完成提醒（见 README「待解答队列」）。不含：图片上传（S2）、重新分析按钮（S3，`reanalyze_enabled=false` 时隐藏；站点不调用 reanalyze / review / void / stats）。
- 与已批准 S1-plan 的差异（只列冲突）：（a）详情默认不带正文 → 站点用 `include=result`；（b）`session_ref`/`session_total` 顶层发送（plan A1 原为不发/不确定）；（c）ack 只收 `{up_to_id}`（plan 备选的 `ids[]` 不用）；（d）新增 `capabilities`，用于判定 judged 是否开启、超时小时数，**不盲试** `kind_not_enabled`；（e）入队响应的 `daily_remaining` / `new_attempt_written` 成为「约还剩 N 次」的首选来源（plan A3 原为推断）；（f）`purged` 是独立状态且 purged 事件仍会发出（plan 按 expired+purged 处理）；（g）`retry_after_s` 在 `error.details`，另有 `Retry-After` 头；（h）批量读默认 `limit=100`；（i）stats 对站点三枚令牌 403，站点不用；（j）events 单页最大 200，站点用 100。
- 与大脑终稿可能不同、已集中在 `internal/brain/queue.go` 头部注释的假设（C1–C6，只影响该文件）：C1 入队成功项缺 `ok` / `new_attempt_written` / `daily_remaining` 时，按 grade 状态推断「新写作答」（unsupported）并退回本地估算「约还剩 N 次」；C2 `result` 只在 `include=result` 时出现，`review.verdict` 在 ①b 前恒为 none；C3 `kinds.<kind>.deadline_hours` 只作文案兜底（入队响应的 `deadline_at` 优先）；C4 未知 state → 页面「状态更新中」，未知 reason → 视为该条永久不可入队；C5 `item_id` 整数 / 字符串都收，统一为字符串；C6 `session_total` = 本次批内条数（剔除坏条目重提时同步改小；后台重试同值）。另：逐项原因 `daily_cap` 站点按「该题今天不可入队」处理（剔除并标「未排队」），契约把它归为批次级/暂时性——**需 PM/大脑确认这一取舍**。

### 8.x 修复单（PR #24 复审）补记

- **`RUYI_QUEUE_ENABLED=0` 是总开关**：其余队列变量全部忽略，出错只 warn（`Config.Warnings`），不拒绝启动。回滚只改这一个值（DEPLOY-P3 §9.2）。
- **`session_ref` / `session_total`（大脑确认）**：`session_ref` 一律传 `client_attempt_id`，单题也传、`session_total=1`（不传 null）。`session_total` = 该 session 下**实际入队**条目数；同一 `session_ref` 前后必须一致，不一致大脑返 400（整请求，`session_total_mismatch`，不是逐项 422）。站点把发出的值存在 `links.jsonl`（`session_total`），补发沿用；剔除重发（大脑未写入）才减小。mock 同样返回 400。
- **老大脑 404**：入队端点不存在返回 404（不是 422）→ 「大脑未升级」：未排队（`not_ready`），不重试、不计失败次数。新大脑 422 才按逐项 `queueable_reason` 剔除重发。
- **入队重试**：按时间窗口（指数退避封顶 5 分钟，到题的处理时限 24h/48h 为止）而非次数；页面 POST 不占自动重试。`retry_exhausted` 进家长提醒。
- **归档 `queue` 行是提交时快照**；`daily_remaining` 在补发窗口内可能与大脑差 1（见 README）。
- **`session_total_mismatch` 的状态码（P3-7，待大脑确认）**：契约文字写 422（批级，逐项 `queueable_reason=null`），大脑实际/mock 为整请求 400。站点**以 `error.code` 为准**，两种状态码一样处理：标「未排队」（`session_total_mismatch`），不重试、不计失败次数（正常不会发生：total 已存档并沿用，只在本地状态损坏时出现）。mock 默认 400，`Set("mismatch_status","422")` 切到契约形态；测试两种都覆盖。
- **r3 页面文案**：首屏结果卡/段落/题目行/额度行不再出现「不入大脑」（徽章除外，仅用于没发到大脑的题）「家长会核对」「家长会看」「未记入大脑」；daily_cap 一律「今天这题已经交满 3 次，这次答案先存档，没有 AI 初评；明天再交一次才会排队。」。服务端 API 的 429 `message`（`msgDailyCap`）与家长提醒文本不变（队列关闭时与 #22 逐字节一致），页面对 daily_cap 429 不再直接显示该 message，而显示上面的新文案。
- **r3 追加核对（大脑契约确认）**：①`answered_at` 是入队幂等键的一部分：grade 时发出的值存进 `links.jsonl`，补发/剔除重发/重启后沿用同一值（无值则一直不带），mock 对同一 `ps:<cid>:<qref>` 的 answered_at/rev/attempt_kind/answer 任一不同回 409 `idempotency_conflict`；②ack 体恰为 `{"up_to_id":<JSON 整数>}`（mock 对字符串/浮点/null/缺字段/`ids[]` 回 400 `validation_failed`）；③400 `validation_failed` 是站点 bug 路径：标「未排队」（`invalid`）、warn、不重试、不计失败次数，422 逐条原因才剔除重发，429 按 `Retry-After`（`daily_cap` 不重试）；④页面状态以 compared/judged 为首次初评：`proposed` 仍显示「AI 判卷中」且不展示任何拟答案正文；⑤单题/补做传 `session_ref=<cid>`、`session_total=实际入队数`（单题=1）。
- **读/写令牌必须不同**：`config.go` 在设了 `RUYI_BRAIN_GRADE_TOKEN` 时拒绝它与 `RUYI_BRAIN_TOKEN` 同值（与队列开关无关）。一个同时带读写权限的令牌（如联调 id26）不能同时填这两个变量，需另签只读令牌（`bank:read`+`brain:<slug>:read`）。见 `loop/ruyi-practice/JOINT-S1-plan.md` §1。
- **r4（仅页面文案/小修）**：①页面按 `/api/config` 的 `"queue":true`（仅队列开时才有这个键；关时响应与 #22 逐字节一致）切文案：关队列的 daily_cap/额度行不提「排队」「AI 初评」（「今天这题已经交满 3 次，这次答案先存档；明天再交一次才会记入大脑。」），开队列用 r3 文案；②`retry_exhausted` 页面文案改为「暂时没能排队，答案已存档」（不承诺通知：家长提醒里确有「N 题没能排队（答案在存档里）」，但只在 poller 开启、且这一套都处理完后才发，notify 关闭时不会发）；③入队失败自动重试期间，题目行/段落统一说「正在排队」，不再同时出现「不入大脑」「没有 AI 初评」；④提交前确认框：开队列时说「先存档，没有 AI 初评、不会记入大脑」，关队列沿用「留档、不会记入大脑」；API 429 的 `msgDailyCap`（含「未记入大脑」）不变；⑤`.notify-started` 标记改为 notify.json 第一次写成功时才建：poller 在空队列上开过、关掉、有提交后再开，是 INFO（first start），不是 WARN。
- **r5（上线前必修）**：①`compare_outcome` 的真实含义（大脑PM确认）是**多个 AI 助手的拟答案两两比对**：match=至少 2 个不同助手全部一致；mismatch=有不一致（不选边，`proposed_answer` 为 null）；n/a=不足 2 个助手；**不是**拿 AI 答案对孩子作答，数学条目也没有「答对/答错」结论。页面/API 的 `compare_text` 因此改为：match →「两位 AI 助手的答案一致，待家长核」；mismatch / n/a（以及只有 `needs_manual`）→「AI 没有一致答案，需要家长看」；match 但缺拟答案和解析 →「AI 初评已出，详情请家长查看」。全站不再有「和你的一致/不一致」「你答对/答错」类措辞（grep 位置：`internal/server/queue_view.go` 的 `CompareText`；页面本身只显示服务端给的 `compare_text`；通知、README、DEPLOY 里没有同类措辞）。只在 state=compared/judged 才画（`proposed` 不画，不变）。②没有 `proposed_answer`/`rationale`（mismatch 的正常情形）时不画 AI 答案区，状态框只有标签「AI 初评 · 待家长核」加上面那句。③P3-E：开队列的确认框不再写「大脑会拒收整批」，改成「这几题这次只会先存档，没有 AI 初评、不会记入大脑；其中能直接判分的题满了，大脑还会把同批的其他题一起退回，也只存档」（页面分不出题目走 grade 路径还是入队路径，所以把两条路径都说清）。④P3-F：重新打开页面时，旧结果里「正在排队」的题以状态 API 为准（放弃排队→`retry_exhausted`，或后来排上了），状态框、题目行、段落统一用「暂时没能排队，答案已存档」。⑤P3-G：`/api/config` 在队列开且完成提醒 poller 开时多一个 `"notify":true`（否则不加；队列关时仍与 #22 逐字节一致）；页面只在 `notify` 为真时才写「AI 初评出来后家长手机会收到提醒」，否则只说「稍后回来看」。
