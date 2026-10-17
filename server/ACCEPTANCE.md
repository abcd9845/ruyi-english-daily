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
SHOTS=/some/dir test/acceptance.sh   # 约 3 分钟；SHOTS 非空且有 node + google-chrome 时追加无头浏览器检查并截图
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

## P1–P3：提交 / 判分 / 归档 / 提醒 / 自动出题

脚本 `test/acceptance.sh` 的 Q 段（curl + jq，对 mock 大脑、本地 ntfy 桩）、L4/L6（无头 Chrome，手机视口）。全部合成数据；mock 大脑已按**大脑最终契约**实现判分/lookup/选题三个接口（见下一节；判分接口用独立令牌、按 `ps:<client_attempt_id>:<qref>` 幂等；答案键只存在于 mock 里，且 mock 会记录是否有人拿 `bank:read` 令牌调判分口）。本地 ntfy 桩把每条 POST 写成一行 JSON，用来核对摘要。

| 验收点 | 用例 | 结果 |
|---|---|---|
| 判分走大脑：`mode:"graded"`、`brain_recorded:true`；结果为 对/错/待核对；摘要 `[总,已判,对,待核对,未答]` | Q1–Q1g | 通过 |
| `client_attempt_id` 幂等：同 id 再来 → `replayed:true`、大脑不重复写、ntfy 不重复、归档不重复；同 id 换答案 → 409 `idempotency_conflict`；跨套复用 → 409 | Q2–Q2d | 通过 |
| 校验：套外 qref（同 source 的备选题、别的题、编造）、题型与形状不符、空格数不对、超长、非 uuid、未登录、未知套、错误 Content-Type、GET；**全部不到达大脑** | Q3–Q3l | 通过 |
| 归档：目录 700、文件 600、属主；JSON Lines v2 字段（`client_attempt_id`、逐题 `recorded`，没有旧名）；答案原样；不含家庭码/令牌/判分令牌/会话密钥/topic/cookie/Authorization | Q4–Q4d | 通过 |
| ntfy：一条 POST 到 `/<topic>`、不带凭据；摘要 `如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。`；明细 JSON；无秘密 | Q5–Q5d | 通过 |
| 响应无答案/解析/标准答案/大脑内部 id；`/api/practice` 仍无答案 | Q6、Q6b | 通过 |
| 判分只用判分令牌，从未用 `bank:read` 令牌调判分口（mock 计数为 0） | Q7 | 通过 |
| 限速（每套每小时 4 次，会话 + IP；mock 的「每题每天 3 次」靠 `/_mock/advance-clock` 拨日子避开）：第 5 次 429 + `Retry-After`；重放不占额度；换 cookie 不重置；别的套另算 | Q8–Q8f | 通过 |
| 重启后同一个 `client_attempt_id` 从归档恢复为重放，不重判、不重发、不重写 | Q9、Q9b | 通过 |
| 自动出题：默认关（404）；需登录；source 须在 `RUYI_AUTO_SOURCES`；n 越界 400；`auto-<8>`；顺序/随机/同 seed 可复现；n 大于库存返回全部 + note；动态套可判分；TTL 后 404；旧参数 `wrong_only=1` → 走大脑选题（`mode:"wrong_only"`，3 题、2 题 deferred） | Q10–Q10n | 通过 |
| **降级** `mode:"archive_only"`：判分令牌未配 / 判分接口 404 / 判分接口 500 三种，全部题 `pending_key`、`correct:null`；仍归档、仍提醒（「未判分」，无对/错/大脑）；响应不提「大脑」 | Q11–Q11g | 通过 |
| 归档目录不可写：照常判分，`archived:false`，日志只有一条 error，不含路径/内容 | Q12、Q12b | 通过 |
| ntfy 不可达：200、`notify:"retrying"`，重试失败只记一条 warn，日志无 topic/地址 | Q13–Q13c | 通过 |
| 旧 17 个静态文件字节不变（开着判分功能时）；数轴页不变；日志无秘密/qref/答案/题干 | Q14–Q15b | 通过 |
| 配置：判分令牌等于读令牌 → 拒绝；非法值 → 退出且只报变量名；启动不碰归档目录 | Q16–Q17 | 通过 |
| 页面（大脑判分）：提交按钮 提交中…禁用、双击只发 1 个请求；请求体形状（`client_attempt_id`、`answered_at`、逐题 `rev`）；「已提交到大脑」+ 摘要；逐题 ✓/✗/待核对；输入锁定；刷新后恢复结果且不重发；「再做一次」；断网重试沿用同一 id、改答案换新 id | L4（55 项） | 通过 |
| 页面（降级）：显示「已提交（暂未判分）」，逐题「待核对」，**页面文字里不含「已记入大脑」「已提交到大脑」，也没有「大脑」二字，无 ✓/✗** | L6（17 项） | 通过 |

Go 单测另覆盖：判分线上契约编解码与"必须有大脑确认"（见下一节）、答案形状（单选/多选/填空/自由）、`SelectForPractice`（seq/random/seed/unsupported）、归档并发与撕裂行、幂等的并发（8 个并发同 id 只判一次）、限速、降级三态、ntfy 重试、配置解析、保留 id（`new`、`auto-*`）；`-race` 通过。

截图（不入库）：`practice-result-graded.png`、`practice-result-card-graded.png`、`practice-result-archive.png`、`practice-result-card-archive.png`。

## 大脑最终契约适配（Refs #10；依赖链 #11 → #12 → 本 PR）

> **范围声明**：权威契约文档（#35）本环境读不到，实现按任务给出的契约要点、大脑 issue #35 正文和 `auto-plan-assessment.md` 写成；**所有测试只对 mock，没有对线上联调**（这三个接口线上要等大脑 #35 部署）。
> 脚本 `test/acceptance.sh` 的 R 段（curl + jq，独立的 mock 大脑实例，状态不与别的段共享）、L7/L8（无头 Chrome）；Go 单测见 `internal/brain/{grade,contract}_test.go`、`internal/server/contract_test.go`。

| 契约点 | 用例 | 结果 |
|---|---|---|
| 判分请求体：`brain:"ruyi"`、`client_attempt_id`、逐题 `qref/answer/rev/answered_at/session_ref`，不含任何令牌/家庭码 | R2、R2b；单测 `TestGradeWireContract` | 通过 |
| 四种状态一批：`needs_review` / `graded` / `pending_key` / `unsupported`；`unsupported` → `recorded:false`、无 `attempt_id`、大脑只写 3 行；`attempt_id` 是整数 | R1–R1f | 通过 |
| ntfy 提醒如实说「3 题记入大脑，其余仅留档」 | R1g | 通过 |
| 限额校验：≤50 题、qref 不重复、填空 ≤10 格、`attempt_kind` 只能 correction/variant、`rev` 须整数（都不到达大脑） | R7–R7c；单测 `TestValidateGradeRequest`、`TestContractFillOverTenBlanksIsNotSentToTheBrain`（>10 格） | 通过 |
| 幂等：同 id 同答案 → 站点重放（不再调大脑）；同 id 换答案 → 409 `idempotency_conflict`+`retry_new_id`，用户改答案换新 id 成功 | R3、R3b、R4d；浏览器 L7 | 通过 |
| 网络重试沿用同一个 id：站点归档丢失、大脑已有 → `duplicate:true` + 同一批 `attempt_id`，大脑不新增写入 | R9、R9b；单测 `TestGradeRetriesOnlyTransientFailuresWithTheSameBody` | 通过 |
| 429（同题每天 >3 次）：整批拒绝，页面文案「今天这题的次数已用完，已留档，未记入大脑」，**归档**（`archive_only`/`daily_cap`，`recorded:false`）、不提醒、重放仍 429 | R4、R4b、R4c、R4c2、R4c3；浏览器 L7、L10 | 见本次验收 |
| 「今天约还剩 N 次」估算、提交前确认（取消不丢草稿）、其它 429 文案区分、「（不入大脑）」徽章 | 单测 `dailylimit_test.go`；浏览器 L10 | 见本次验收 |
| 409 `rev_mismatch`：文案「题目已更新，请刷新」，页面重新取题（带新 `rev`）、答案保留、换新 id | R5–R5c；浏览器 L7 | 通过 |
| 404 未知 qref：整批不写、不调判分口；套页面里缺的题只进 `missing_qrefs`，不崩 | R6、R6b；单测 | 通过 |
| `attempt_kind=correction` 透传 | R8 | 通过 |
| `recorded:false` 绝不显示「已提交到大脑」：逐题判断：全部 `recorded:false` →「已提交（仅留档，未记入大脑）」；部分记录 →「已提交到大脑（N / M 题）」并标出未记录的题（「不自动判分」+ 附注）；归档模式 / 接口未部署 →「已提交（暂未判分）」 | 浏览器 L7、L8、L6；R10 | 通过 |
| 令牌只在服务端：两个令牌都不在任何响应体/响应头、practice 页面、归档文件、ntfy 内容、日志里（grep 标记串）；判分只用判分令牌（mock 计数 `grade_with_bank_read_token`=0）；启动校验 判分令牌 ≠ 读令牌，报错不含值 | R2c、R11–R12b、R17、R18、Q4d、Q5d、Q7、Q15、Q16 | 通过 |
| 404/501 优雅降级（不 500）：lookup 未部署 → 退回整卷列表（无 `rev`），判分照常；选题未部署 → 501「大脑接口未就绪」，随机套不受影响；判分未部署（501 或 404）→ `archive_only` + `brain_note:"not_ready"`；判分 500 → `archive_only`/`unavailable` | R13–R15d；单测 `TestLookupFallsBackToTheListWhenNotDeployed` 等 | 通过 |
| 选题 `mode=due_first|wrong_only`：`origin`/`reasons`；`deferred:*` 默认保留并标「参考答案待核，不自动判」，`RUYI_PICK_SKIP_DEFERRED=1` 跳过；非法 `mode` → 400；`/api/practice/new` 默认仍关 | R14d、R16–R16c、Q10；浏览器 L7（错题标签、待核标记） | 通过 |
| lookup：≤100 一批（分块）、`missing` 不崩、他源/多余项忽略、带 `rev`/`media`/`figure_status`、无答案 | 单测 `TestLookupWireAndMissing`、`TestLookupChunksOf100`；P11（键白名单含 `rev`） | 通过 |
| 配置：`RUYI_BRAIN_LOOKUP_PATH` / `RUYI_BRAIN_PICK_PATH` / `RUYI_PICK_SKIP_DEFERRED` 非法值 → 退出且只报变量名 | R17b；单测 `TestGradeEnvRejects` | 通过 |
| v1 旧归档仍可重放（`brain_attempt_id` 旧格式），不调大脑 | 单测 `TestContractV1ArchiveStillReplays` | 通过 |

自检：`go vet ./...` 干净；`gofmt -l` 无输出；`go test ./... -count=1` 全过（135 个顶层测试）；`CGO_ENABLED=1 go test -race ./...` 全过；`test/acceptance.sh`（`SHOTS=<目录>`，含无头 Chrome）**406 通过 / 0 失败**；`test/browser.js` 沿用（L1/L2/L3 全过）。

## 契约核对修正（S 节；依据大脑开发的核对与 `练习站自动方案接口.md`）

| 修正点 | 用例 | 结果 |
|---|---|---|
| 404 靠结构区分：grade 的 `details.qrefs` 404 =「未知题」；兜底 `not_found`/"not found" = 未部署 → 降级；lookup 任何 404/501/405 = 未部署（退回列表）；选题 "source not found" 是明确错误，与「未就绪」不同 | S1–S1e；单测 `TestErrorBodyIsParsedAndSanitized`、`TestLookupAnyNotFoundMeansNotDeployed`、`TestPickSourceNotFoundIsNotNotDeployed` | 通过 |
| 429 `rate_limited`/"daily grade limit reached"/`details.qrefs`/`Retry-After`（上海日、3 次、整批拒绝）；`Retry-After` 透传 | S2；R4 系列 | 通过 |
| `answered_at`：窗口外不发；同键换 `answered_at` → 409；不带 → 重放；201/200 | S3–S4d；单测 `TestAnsweredAtWindow`、`TestGradeAcceptsCreatedAndOK` | 通过 |
| 严格 400 全在站点发送前自校验（未知字段、小写 qref、cid 正则、1..50、答案 ≤10 项、每项 ≤2000 字符（按字符计，不是字节；大脑 PR #47）、`attempt_kind`、`session_ref`、`payload`、query 白名单）；整次运行 mock 记录到的 400 只来自 S5/S5b/S5c5 的 5 次直连探测 | S5–S5d、S10、S10b；单测 `TestGradeBodyIsStrictAndLowerCase`、`TestValidateAgainstTheDocumentLimits` | 通过 |
| `rev_mismatch` 只用 `details.qref/rev/current_rev` 定位，不看 `details.index` | S6；单测 | 通过 |
| 别名与大小写：lookup/选题回规范 qref，grade 回显发送的串；按发送串匹配；别名与规范 qref 同一幂等键 | S7–S7d；单测 `TestLookupAliasesAndCase` | 通过 |
| 选题 `n` ≤10（站点超过先降到 10）、reasons 词表、`origin` 只有 review\|wrong | S8；单测 `TestPickNeverAsksForMoreThanTenAndSendsOnlyKnownQuery` | 通过 |
| 启动时一次只读 lookup 探测（只写日志、不影响启动） | S0、S0b、S9、S9b；单测 `TestProbeLookup` | 通过 |
| `needs_review`（单位尾巴看不清）显示「待核对」 | R1（四态同批）；`contract_doc_test.go` | 通过 |
| 令牌：仍是 bank:read + 单脑写令牌，只在服务端；日志/响应零泄漏 | S11、R11、R18 | 通过 |

## 构建产物

```
$ file dist/ruyi-site-linux-amd64
dist/ruyi-site-linux-amd64: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped
$ ldd dist/ruyi-site-linux-amd64
not a dynamic executable
$ sha256sum dist/ruyi-site-linux-amd64
f468e823f60e5cbf751a6a0ef1df9ffd374e0f01f244088453e060d6895119e6  dist/ruyi-site-linux-amd64
```

本 PR 的静态二进制（`ruyi-site-contract-linux-amd64`，8,482,976 字节，同一源码两次构建 sha256 相同）。P0 起内嵌了页面和 KaTeX：二进制比 `main` 上的版本大约 644 KiB（7,663,776 → 8,323,232 字节，同一工具链对比）；P1–P3 再增加约 112 KiB（→ 8,437,920 字节）。

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
     Set-Cookie (value elided): ruyi_sess=<value>; Path=/; Expires=Mon, 16 Nov 2026 11:56:48 GMT; Max-Age=2592000; HttpOnly; SameSite=Lax
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
grep: /tmp/tmp.i3tBX3kIbY/all-fresh/_api_media_math-intake.zz-yuekao-numberline-4y_19657038d590051de9672997cd2d38902c52a55392e28d7bc9410950d854e95a.raw: binary file matches
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
       time=2026-10-17T19:56:48.304+08:00 level=WARN msg="login failed" ip=127.0.0.1
       time=2026-10-17T19:56:49.095+08:00 level=WARN msg="brain request failed" op=media-lookup err="media not in source"
       time=2026-10-17T19:56:47.803+08:00 level=DEBUG msg=request method=GET path=/healthz status=200 ms=0 ip=127.0.0.1

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
PASS  P11 allowed item keys only (=["blanks","figure_status","media","options","ordinal","qref","qtype","rev","stem_md"])
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

== Q. P1–P3 — grading through the brain, client_attempt_id idempotency, archive, ntfy heads-up, dynamic sets ==
PASS  Q1 grade demo-1 -> 200 (=200)
PASS  Q1b mode graded, brain confirmed, ids echoed (=["graded",true,"11111111-1111-4111-8111-111111111111","demo-1",false])
PASS  Q1c per-question verdicts (wrong key->false is NOT leaked as an answer; 1 right, 1 wrong, 1 pending_key) (=[["graded",true],["graded",false],["pending_key",null]])
PASS  Q1d summary total/graded/correct/pending/unanswered (=[3,2,1,1,0])
PASS  Q1e archived and notified (=[true,"sent"])
PASS  Q1f the brain was asked exactly once (grade_new +1) (=1)
PASS  Q1g response has only the documented keys (replayed appears only on a replay) (=["archived","brain_recorded","client_attempt_id","mode","notify","recorded_count","results","set_id","summary"])
PASS  Q2 same client_attempt_id again -> 200 replayed:true with the stored result (=[true,"graded",1])
PASS  Q2b same client_attempt_id with different answers -> 409 idempotency_conflict, retry_new_id (nothing re-graded) (=409 ["idempotency_conflict",true])
PASS  Q2c no second brain write, no second notification, one archive line (=1 0 1 1)
PASS  Q2d same id on another set -> 409 (=409)
PASS  Q3 qref of the same source but outside the set (alternate q4) -> 400 (=400)
PASS  Q3b qref outside the set (q5) -> 400 (=400)
PASS  Q3c invented qref -> 400 (=400)
PASS  Q3d choice answered with an array -> 400 (=400)
PASS  Q3e wrong blank count -> 400 (=400)
PASS  Q3f blank over the length cap -> 400 (=400)
PASS  Q3g client_attempt_id must be a uuid -> 400 (=400)
PASS  Q3h anonymous -> 401 (=401)
PASS  Q3i unknown set -> 404 (=404)
PASS  Q3j wrong content type -> 415 (=415)
PASS  Q3k GET on the grade URL -> 405 (=405)
PASS  Q3l none of the rejected requests reached the brain's grade endpoint (=0)
PASS  Q4 attempts dir is 700 and the file 600, owner is the site user (=700 600 box)
PASS  Q4b record fields (JSON Lines, version 2, client_attempt_id naming, per-item recorded, mode) (=[2,"11111111-1111-4111-8111-111111111111","demo-1","演示套","math.synthetic.p0-demo","graded",true,3,"none"])
PASS  Q4c answers are archived as given (fill blanks as arrays) (=[["1","2","3"],"A",["x","y"]])
PASS  Q4d family code not in the attempts archive
PASS  Q4d bearer token not in the attempts archive
PASS  Q4d grade token not in the attempts archive
PASS  Q4d session secret not in the attempts archive
PASS  Q4d ntfy topic not in the attempts archive
PASS  Q4d cookie name not in the attempts archive
PASS  Q4d Authorization header not in the attempts archive
PASS  Q5 ntfy: one POST, to /<topic>, no credentials (=[1,true,true,true])
PASS  Q5b ntfy summary line (heads-up wording, not an approval request) (=如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。)
PASS  Q5c ntfy detail JSON carries client_attempt_id and mode, no secrets (=["11111111-1111-4111-8111-111111111111","graded"])
PASS  Q5d family code not in the ntfy message
PASS  Q5d bearer token not in the ntfy message
PASS  Q5d grade token not in the ntfy message
PASS  Q5d session secret not in the ntfy message
PASS  Q6 grade response g1: no answer, explanation, key or brain attempt id
PASS  Q6 grade response g2: no answer, explanation, key or brain attempt id
PASS  Q6 grade response g2b: no answer, explanation, key or brain attempt id
PASS  Q6b practice response still has no answers/explanations
PASS  Q7 the grade call never used the bank:read token (mock saw 0) (=0)
PASS  Q8 submission 2 of 4 accepted
PASS  Q8 submission 3 of 4 accepted
PASS  Q8 submission 4 of 4 accepted
PASS  Q8b 5th fresh submission in the hour -> 429 (=429)
PASS  Q8c 429 carries Retry-After
PASS  Q8d replay of an accepted attempt is still served while limited (=true)
PASS  Q8e a fresh login from the same address does not reset the limit (=429)
PASS  Q8f another set has its own allowance (=200)
PASS  Q9 after a restart the same client_attempt_id is replayed from the archive (=[true,"graded",1])
PASS  Q9b ... without grading, notifying or archiving again (=0 0)
PASS  Q10 RUYI_AUTO_SOURCES empty (default) -> /api/practice/new is off (404) (=404)
PASS  Q10b dynamic sets require login (401) (=401)
PASS  Q10c a source outside RUYI_AUTO_SOURCES -> 400 (=400)
PASS  Q10d n=0 -> 400 (=400)
PASS  Q10d n=21 -> 400 (=400)
PASS  Q10d n=abc -> 400 (=400)
PASS  Q10e n=2 -> 200 (=200)
PASS  Q10f set id looks like auto-<8>
PASS  Q10g response: url, count, truncated, expiry present (=[true,2,false,true])
PASS  Q10h the dynamic set is a normal practice set (seq order 1,2) (=example.set:math:1,example.set:math:2)
PASS  Q10i grading a dynamic set works (graded, 1 right) (=["graded",1,true])
PASS  Q10j n larger than the source returns all (5) with truncated + note (=[5,true,20,true])
PASS  Q10k legacy wrong_only=1 -> the brain's pick (mode wrong_only, 3 picks, 2 deferred) (=200 ["wrong_only",3,2])
PASS  Q10l same seed -> same selection (=example.set:math:3,example.set:math:2,example.set:math:4)
PASS  Q10m after the TTL (3 s) the dynamic set is gone (404) (=404)
PASS  Q10n a made-up auto id -> 404 (=404)
PASS  Q11 [grade token not configured] -> 200 mode archive_only, brain_recorded false (=200 ["archive_only",false,true,"sent"])
PASS  Q11b [grade token not configured] every result is pending_key with correct:null, summary pending 3 (=[[["pending_key",null]],3,0,0])
PASS  Q11c [grade token not configured] ntfy says 未判分 and never 对/错/大脑 (=10)
PASS  Q11d [grade token not configured] exactly one more notification (=1)
PASS  Q11e [grade token not configured] response never says the brain recorded anything
PASS  Q11f [not configured] the brain grade endpoint was never called (=0)
PASS  Q11 [grade endpoint missing (404)] -> 200 mode archive_only, brain_recorded false (=200 ["archive_only",false,true,"sent"])
PASS  Q11b [grade endpoint missing (404)] every result is pending_key with correct:null, summary pending 3 (=[[["pending_key",null]],3,0,0])
PASS  Q11c [grade endpoint missing (404)] ntfy says 未判分 and never 对/错/大脑 (=10)
PASS  Q11d [grade endpoint missing (404)] exactly one more notification (=1)
PASS  Q11e [grade endpoint missing (404)] response never says the brain recorded anything
PASS  Q11 [grade endpoint failing (500)] -> 200 mode archive_only, brain_recorded false (=200 ["archive_only",false,true,"sent"])
PASS  Q11b [grade endpoint failing (500)] every result is pending_key with correct:null, summary pending 3 (=[[["pending_key",null]],3,0,0])
PASS  Q11c [grade endpoint failing (500)] ntfy says 未判分 and never 对/错/大脑 (=10)
PASS  Q11d [grade endpoint failing (500)] exactly one more notification (=1)
PASS  Q11e [grade endpoint failing (500)] response never says the brain recorded anything
PASS  Q11g [QD] one archive line, mode archive_only, 600 perms (=[1,"archive_only"] 600)
PASS  Q11g [QO] one archive line, mode archive_only, 600 perms (=[1,"archive_only"] 600)
PASS  Q11g [QF] one archive line, mode archive_only, 600 perms (=[1,"archive_only"] 600)
PASS  Q12 unwritable archive dir: still graded, archived:false (both submissions) (=["graded",false]["graded",false])
PASS  Q12b exactly one error log line for it, without path or content (=1 0)
PASS  Q13 ntfy unreachable: 200, graded, notify:retrying, archived (=200 ["graded","retrying",true])
PASS  Q13b the retry failure is logged once as a warning (=1)
PASS  Q13c no ntfy topic or address in the log
PASS  Q14 all 17 old static files still byte-identical to origin/main with grading enabled (diffs) (=0)
PASS  Q14b numberline page unchanged (=12c9b7a7b6e78b3b33206717ee52bf2fe03f84768c02b99413f101dac022c02d)
PASS  Q15 bearer token not in the logs of the P1–P3 sites
PASS  Q15 grade token not in the logs of the P1–P3 sites
PASS  Q15 family code not in the logs of the P1–P3 sites
PASS  Q15 session secret not in the logs of the P1–P3 sites
PASS  Q15 ntfy topic not in the logs of the P1–P3 sites
PASS  Q15 qref not in the logs of the P1–P3 sites
PASS  Q15 answer text not in the logs of the P1–P3 sites
PASS  Q15 stem text not in the logs of the P1–P3 sites
PASS  Q15 client answer not in the logs of the P1–P3 sites
PASS  Q15b degraded grading is logged (kind only)
PASS  Q16 grade token equal to the read token -> refused, names the variable, no value
PASS  Q16b RUYI_GRADE_MAX_PER_HOUR invalid -> refused, variable named
PASS  Q16b RUYI_ATTEMPTS_DIR invalid -> refused, variable named
PASS  Q16b RUYI_AUTO_SOURCES invalid -> refused, variable named
PASS  Q16b RUYI_NTFY_BASE_URL invalid -> refused, variable named
PASS  Q16b RUYI_BRAIN_GRADE_PATH invalid -> refused, variable named
PASS  Q17 startup does not touch the attempts dir until the first submission (lazy create)

== R. Brain final contract (#35): statuses, errors, idempotency, token hygiene, degradation — mock brain only ==
PASS  R1 four statuses in one batch -> 200 (needs_review / graded / pending_key / unsupported) (=200 ["needs_review","graded","pending_key","unsupported"])
PASS  R1b recorded per item: only unsupported is recorded:false; correct only on graded (=[[true,null],[true,true],[true,null],[false,null]])
PASS  R1c brain_recorded true, recorded_count 3 of 4; summary carries unsupported:1 (=[true,3,1,3])
PASS  R1d every recorded item has the brain's integer attempt_id, the unsupported one has none (=["number","number","number","null"])
PASS  R1e the brain wrote 3 rows (grade_new_item), not 4 (=3)
PASS  R1f archive says the same: per-item recorded flags (=["graded",[true,true,true,false]])
PASS  R1g ntfy line says 3 of 4 went to the brain, the rest archive-only (never a flat 已提交) (=如意练习：四态套 4 题，对 1，待核对 2，不自动判 1，已提交（3 题记入大脑，其余仅留档）。attempt 00000001。)
PASS  R2 wire format: brain=ruyi, id, items with qref/answer/rev/answered_at/session_ref, no token or family code in the body (=["ruyi","00000001-aaaa-4aaa-8aaa-aaaaaaaaaaaa",4,["example.set:math:1",1,"2026-10-17T08:00:00Z","mix-4"],false])
PASS  R2b grade body carries no token or family code
PASS  R2c the grade call used the grade token only (read token never accepted for grading) (=0)
PASS  R3 same client_attempt_id, changed answer -> 409 idempotency_conflict + retry_new_id (site-side check, brain not called) (=409 ["idempotency_conflict",true] 0)
PASS  R3b ... the user's re-submit with changed answers gets a NEW id and is accepted (new write, wrong this time) (=200 ["graded",false,null])
PASS  R4 daily cap: 3rd write of the same questions the same day is still fine (=200)
PASS  R4b 4th write the same day -> 429 for the whole batch, with the page copy 今天这题提交次数到上限 (=429 ["daily_cap",true])
PASS  R4c ... nothing archived and nothing notified for the refused batch (=0 0)
PASS  R4d replay of an accepted attempt is still served after the cap (site archive, no brain call) (=[true,"graded"])
PASS  R5 stale rev -> 409 rev_mismatch with the page copy 题目已更新，请刷新; nothing archived (=409 ["rev_mismatch",true] 0)
PASS  R5b the practice API serves the current rev (2) so a reload fixes it (=[2])
PASS  R5c ... resubmitting with rev 2 is accepted (=200 graded)
PASS  R6 a qref the brain does not know (lookup missing) -> 404 unknown_qref, nothing written, grade endpoint not called (=404 unknown_qref 0 0)
PASS  R6b the set page still loads (missing qref listed, no crash) (=200 [1,["example.set:math:99"]])
PASS  R7 duplicate qref in a batch -> 400 (never sent) (=400)
PASS  R7b non-integer rev -> 400 (=400)
PASS  R7c attempt_kind other than correction|variant -> 400 (=400)
PASS  R8 attempt_kind=correction is accepted and forwarded to the brain (=200 ["correction"])
PASS  R9 network retry with the same id after the site lost its archive: brain answers duplicate:true with the SAME attempt ids (=200 [1001,1002,1003,null] [true,true,true,null])
PASS  R9b ... no new brain writes (grade_new_item +0; the brain counts all 4 items as duplicates) (=0 4)
PASS  R10 archive-only submit (no grade token): every result recorded:false, brain_recorded false (=["archive_only",false,0,false])
PASS  R11 neither token appears in any R response (bodies or headers)
PASS  R11b practice page has no token
PASS  R12 token marker not in the attempts archive, ntfy content, or the site log (grep)
PASS  R12b grade token in no response header of the whole run
PASS  R12c refusals are logged (kind/status/code only)
PASS  R13 lookup endpoint missing (501): the set page falls back to the bank list, 200, no rev (=200 [1,"none"])
PASS  R13b ... and grading still works without rev (graded, recorded) (=200 ["graded",1])
PASS  R14 pick endpoint missing (501): explicit 大脑接口未就绪, 501 (not 500) (=501 ["wrong_only_unavailable",true])
PASS  R14b due_first likewise (=501)
PASS  R14c plain random sets (P1–P3 behavior) keep working while pick is missing (=200)
PASS  R14d unknown mode -> 400 (=400)
PASS  R15 grade endpoint missing (501): 200 archive_only, brain_note not_ready, nothing recorded (not 500) (=200 ["archive_only","not_ready",false,0,true])
PASS  R15b ... the answer is archived with mode archive_only (=["archive_only","not_ready",false])
PASS  R15c grade route 404 (not unknown_qref) is also 'not ready', not an error page (=200 not_ready)
PASS  R15d brain failing (500): archive_only, brain_note unavailable (=200 ["archive_only","unavailable"])
PASS  R16 default: deferred picks are kept and counted (marked 参考答案待核 by the page) (=200 ["wrong_only",3,2])
PASS  R16b RUYI_PICK_SKIP_DEFERRED=1: deferred picks are dropped (1 left) (=200 [1,0])
PASS  R16c pick items carry origin and a deferred mark, never an answer; question text came from lookup (=[["wrong",false],["wrong","pending_key"],["wrong","unsupported_qtype"]])
PASS  R17 RUYI_BRAIN_GRADE_TOKEN == RUYI_BRAIN_TOKEN is refused at startup (no value printed)
PASS  R17b RUYI_BRAIN_LOOKUP_PATH invalid -> refused, variable named
PASS  R17b RUYI_BRAIN_PICK_PATH invalid -> refused, variable named
PASS  R17b RUYI_PICK_SKIP_DEFERRED invalid -> refused, variable named
PASS  R18 bearer token not in the R site logs
PASS  R18 grade token not in the R site logs
PASS  R18 family code not in the R site logs
PASS  R18 answer text not in the R site logs
PASS  R18 answer text 2 not in the R site logs

== S. Corrections against the brain developers' contract check — mock brain only ==
PASS  S0 startup probe (one read-only lookup of a non-existent qref): 'lookup is deployed' logged, startup unaffected
PASS  S0b the probe wrote nothing (no grade call, no 400s) (=0 0)
PASS  S1 grade 404 carrying details.qrefs (question not found) -> the site's unknown-question 404, nothing recorded (=404 unknown_qref 0)
PASS  S1b catch-all 404 (not_found / 'not found', no details) = route not deployed -> archive_only / not_ready, 200 (=200 ["archive_only","not_ready"])
PASS  S1d pick 404 'source not found' for a nonexistent source: an explicit error, NOT 'not deployed' (=404 ["pick_source_not_found",false])
PASS  S1e pick catch-all 404 = not deployed -> 501 大脑接口未就绪 (distinct from S1d) (=501 ["wrong_only_unavailable",true])
PASS  S2 4th write the same Shanghai day -> 429 daily_cap, one unified message, Retry-After passed through (seconds), nothing archived (=429 daily_cap ok 0)
PASS  S3 answered_at older than 7 days is left out (never sent): 200 graded, no field on the wire, no 400 (=200 graded false 0)
PASS  S3b answered_at in the future (> now+1min) is left out, not sent (=200 false 0)
PASS  S3c a valid answered_at is forwarded as RFC 3339 (=200 true)
PASS  S4 brain: new write -> 201 (=201)
PASS  S4b brain: replay without answered_at -> 200 duplicate (=200 true)
PASS  S4c brain: same key + a different answered_at -> 409 idempotency_conflict (=409 idempotency_conflict)
PASS  S4d brain: attempt_kind / session_ref / payload do not take part in the comparison (replay, 200) (=200)
PASS  S5 grade: unknown body field -> 400 invalid_json (mock mirrors the brain) (=400 invalid_json)
PASS  S5b grade: any query other than ?brain= -> 400; upper-case qref -> 400; answered_at out of range -> 400 (=400 400 400)
PASS  S5c an answer of exactly 2000 characters is SENT to the brain (mode graded, not archive_only/not_sent; this free-text qref is 'unsupported' at the mock, so recorded:false) and the brain accepts it (no 400) (=200 ["graded","unsupported"] 1 0)
PASS  S5c2 2000 Chinese characters (6000 bytes) count as 2000 characters: sent, accepted by the brain (=200 ["graded","unsupported"] 2 0)
PASS  S5c3 2001 characters (ASCII or Chinese): the site refuses with 400 itself (free-text cap = the brain's cap); the brain is never called, nothing truncated (=400 400 0 0)
PASS  S5c4 brain mock: 10 blanks x 2000 characters -> 201 (=201)
PASS  S5c5 brain mock: one blank of 2001 characters -> 400 validation_failed (=400 validation_failed)
PASS  S5d a rev below 1 never reaches the brain: the site answers 400 bad_rev itself (=400 bad_rev 5)
PASS  S6 rev_mismatch 409 (details qref/rev/current_rev, no index) -> 题目已更新，请刷新 (=409 ["rev_mismatch",true])
PASS  S7 set page keeps the set's own strings (alias and mixed case); lookup answered canonical qrefs (=200 ["example.set:math:2-old","Example.Set:Math:1"] [[]])
PASS  S7b grade with an alias and a mixed-case qref: lower-cased on the wire, echoes matched back to the page's strings, both graded + recorded (=200 ["graded",2,[["example.set:math:2-old",true],["Example.Set:Math:1",true]]])
PASS  S7c on the wire: lower-case only (=["example.set:math:2-old","example.set:math:1"])
PASS  S7d alias and canonical qref share one idempotency key (no second write) (=0)
PASS  S8 n above 10 is lowered to 10 before asking the brain (no 400 at the brain), page gets a note (=200 0 true)
PASS  S1c lookup 404 (catch-all) = not deployed -> fallback to the list, set page still 200 (=200 [1])
PASS  S9 probe against a brain without lookup: a warning, startup continues
PASS  S9b ... and the site is healthy (=200)
PASS  S10 the site never produced a 400-class request to the brain in this section (grade_400 only from the 5 direct mock probes S5/S5b x3/S5c5) (=5 0 0)
PASS  S10b the R section never produced one either (=0 0 0)
PASS  S11 bearer token not in the S site logs
PASS  S11 grade token not in the S site logs
PASS  S11 family code not in the S site logs

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
  dom: {"title":"演示套","docTitle":"演示套 · 如意","tex":15,"katex":15,"display":2,"err":0,"raw":0,"mathml":15,"blanks":[3,0,2],"radios":[0,4,0],"areas":[0,0,0],"script":0,"pwned":"undefined","q3code":["x<y"],"q3strong":["解方程组"],"optsHaveKatex":[true,true,true,true],"noOptionRawDollar":true,"stemRawDollar":false,"qno":["第 1 题","第 2 题","第 3 题"],"submit":{"disabled":false,"text":"提交","shown":true},"noteOnPage":true,"resultShown":false,"verdicts":0,"fontOk":true,"sameOrigin":true,"resources":["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]}
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
PASS submit button is enabled and says 提交; no result UI before submitting — {"disabled":false,"text":"提交","shown":true}
PASS no correct/incorrect UI before submitting
PASS KaTeX woff2 font actually loaded from this origin
PASS every request is same-origin (nothing from a CDN) — ["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]
PASS only the embedded files were fetched (page assets) — ["/vendor/katex/fonts/KaTeX_Main-Regular.woff2","/vendor/katex/fonts/KaTeX_Math-Italic.woff2","/vendor/katex/fonts/KaTeX_Size4-Regular.woff2","/vendor/katex/katex.min.css","/vendor/katex/katex.min.js"]
  layout: {"scrollW":390,"iw":390,"small":0,"minFont":18,"body":18,"wide":0,"blankW":135}
PASS no horizontal page overflow at 390px — {"scrollW":390,"iw":390,"small":0,"minFont":18,"body":18,"wide":0,"blankW":135}
PASS touch targets >= 44px (blanks, options, buttons) — too small: 0
PASS inputs use >= 16px font (no iOS zoom); body is 18px — minFont=18 body=18
PASS blank boxes are wide (>= 110px) — min width 135
  draft keys: ["v","set","a","t","cid","sig","at","res"] ls keys: ["ruyi-practice-demo-1-v1"]
PASS draft saved under ruyi-practice-demo-1-v1 with set, timestamp and qref-keyed answers — {"example.set:math:1":["5/4","","-3"],"example.set:math:2":"B","example.set:math:3":["","y=1"]}
PASS family code not in localStorage / sessionStorage / document.cookie
PASS session cookie is HttpOnly (invisible to JS)
PASS progress shows answered count
PASS after reload (cookie kept) the cards come back without the gate
PASS draft restored after reload (blanks and the chosen option) — {"q1":["5/4","","-3"],"q2":[false,true,false,false],"q2sel":1,"q3":["","y=1"]}
PASS 「清空」 removes the draft and the inputs — {"ls":null,"vals":"","checked":0}
PASS [graded] demo set loads for the submit flow
PASS [graded] submitting nothing shows a hint and sends no request
PASS [graded] while submitting the button is disabled (提交中…) and a double click sends one request — {"dis":true,"text":"提交中…","n":1,"clear":true}
PASS [graded] result card appears
PASS [graded] request carries client_attempt_id (uuid), answered_at (fixed per id), and only qref/answer(/rev) rows; multi-key answers are arrays/strings by type — {"client_attempt_id":"b3909f9b-db80-43bd-a511-9d6bb0d1dc89","answered_at":"2026-10-17T11:57:17.479Z","answers":[{"qref":"example.set:math:1","answer":["5/4","","-3"],"rev":1},{"qref":"example.set:math:2","answer":"B","rev":1},{"qref":"example.set:math:3","answer":["","y=1"],"rev":1}]}
PASS [graded] graded mode: 「已提交到大脑」 with the brain-confirmed summary — 已提交到大脑 / 共 3 题 · 答对 1 · 答错 1 · 待核对 1 · 未作答 0
PASS [graded] graded mode: per-question badges (wrong, right, pending) and no answer key anywhere — ["✗ 不对|vd wrong","✓ 答对了|vd ok","待核对|vd pend"]
PASS [graded] inputs locked after submitting, 再做一次 offered, submit button hidden
PASS [graded] draft keeps client_attempt_id and the stored result (no family code, no tokens)
PASS [graded] after reload the result is shown again without a new request
PASS [graded] 「再做一次」 clears answers, result and attempt id; form is editable again — {"ls":null,"vals":"","locked":false,"submit":"block","result":"none","vd":0,"btnOk":true}
PASS [graded] offline submit: message, button usable again, answers kept
PASS [graded] retry succeeds
PASS [graded] the retry reused the same client_attempt_id (idempotent) — ["a17456ce-1b56-487a-92f5-5d27b659e064","a17456ce-1b56-487a-92f5-5d27b659e064"]
PASS [graded] the retry reused the same answered_at (the brain compares it on a replay) — ["2026-10-17T11:57:19.944Z","2026-10-17T11:57:19.944Z"]
PASS [graded] edited answers after a failed attempt use a fresh client_attempt_id — ["ba349f9a-9414-4cb6-9ddc-f56baccd319e","d2757bbb-88fa-457d-a224-6fe68fd9e1e5"]
PASS config order wins (3 then 1) and the missing qref only yields a notice
PASS free-form question gets a wide answer area (textarea)
PASS textarea answer saved as a string under ruyi-practice-free-3-v1 — {"example.set:math:4":"1/2+1/3=5/6\n过程：通分"}
PASS answer area is tall and full-width on a phone — {"h":173.984375,"w":342,"iw":390}
PASS unknown set: friendly message, no cards
PASS logout shows the gate and removes the questions

55/55 browser checks passed
PASS  L4 practice page + submit flow in headless Chrome, brain grades (all passed)
PASS  L5 no code/token in the log of the browser-driven practice run
PASS  L5b the browser-driven attempts were archived (JSON Lines, 600) (=600)
PASS [archive] demo set loads for the submit flow
PASS [archive] submitting nothing shows a hint and sends no request
PASS [archive] while submitting the button is disabled (提交中…) and a double click sends one request — {"dis":true,"text":"提交中…","n":1,"clear":true}
PASS [archive] result card appears
PASS [archive] request carries client_attempt_id (uuid), answered_at (fixed per id), and only qref/answer(/rev) rows; multi-key answers are arrays/strings by type — {"client_attempt_id":"9d2917d3-5d83-43e5-8808-baef8631f893","answered_at":"2026-10-17T11:57:40.501Z","answers":[{"qref":"example.set:math:1","answer":["5/4","","-3"],"rev":1},{"qref":"example.set:math:2","answer":"B","rev":1},{"qref":"example.set:math:3","answer":["","y=1"],"rev":1}]}
PASS [archive] archive-only: heading says 已提交（暂未判分） — 已提交（暂未判分） / 共 3 题 · 已作答 3 · 未作答 0
PASS [archive] archive-only: every question is 待核对 — no ✓/✗ verdict at all — ["待核对|vd pend","待核对|vd pend","待核对|vd pend"]
PASS [archive] archive-only: page never says 已记入大脑 / 已提交到大脑 / mentions 大脑 — text scanned: 596 chars
PASS [archive] inputs locked after submitting, 再做一次 offered, submit button hidden
PASS [archive] draft keeps client_attempt_id and the stored result (no family code, no tokens)
PASS [archive] after reload the result is shown again without a new request
PASS [archive] 「再做一次」 clears answers, result and attempt id; form is editable again — {"ls":null,"vals":"","locked":false,"submit":"block","result":"none","vd":0,"btnOk":true}
PASS [archive] offline submit: message, button usable again, answers kept
PASS [archive] retry succeeds
PASS [archive] the retry reused the same client_attempt_id (idempotent) — ["1e5e0c37-6264-48b4-9e2f-7951ee639076","1e5e0c37-6264-48b4-9e2f-7951ee639076"]
PASS [archive] the retry reused the same answered_at (the brain compares it on a replay) — ["2026-10-17T11:57:42.968Z","2026-10-17T11:57:42.968Z"]
PASS [archive] edited answers after a failed attempt use a fresh client_attempt_id — ["efe44121-9674-4f93-8c75-444a47bc96c2","0383ae99-92b1-45ae-80d4-bcb6f808c0df"]

17/17 browser checks passed
PASS  L6 degraded (archive-only) submit flow: the page never claims the brain recorded it (all passed)
PASS [contract] demo set loads
PASS [contract] rev from the brain lookup is sent with every answer — [1,1,1]
PASS [contract] graded: 已提交到大脑 (all three recorded:true)
PASS [contract] stale rev: message 题目已更新，请刷新 and the page re-fetched the questions
PASS [contract] after the refresh the answers are still there and nothing was marked submitted — {"q1":"5/4||-3","q2":1,"shown":false}
PASS [contract] resubmitting after the refresh succeeds with the new rev and a NEW client_attempt_id
PASS [contract] ... q2 now carries rev 2; ids differ between the refused and the accepted request — [["58fb7b0e",[1,1,1]],["c2445d4a",[1,2,1]]]
PASS [contract] idempotency_conflict: automatic single resubmit succeeds
PASS [contract] ... the second request used a new client_attempt_id (same answers) — ["e5b4c3d1","41d6fc23"]
PASS [contract] a repeating idempotency_conflict stops after one automatic retry and shows a message — requests=2
PASS [contract] 429: 「今天这题提交次数到上限」, answers stay, no result card — 今天这题提交次数到上限了（同一题每天最多记 3 次），明天再试。答案还在。
PASS [contract] free-form set loads
PASS [contract] unsupported (recorded:false): heading 已提交（仅留档，未记入大脑）, badge 不自动判分, never 已提交到大脑 — 已提交（仅留档，未记入大脑） / 不自动判分
PASS [contract] /api/practice/new?mode=wrong_only creates a set from the brain pick — {"count":3,"deferred":2,"expires_at":"2026-10-17T11:58:53Z","mode":"wrong_only","note":"2 题参考答案待核，不自动判","requested":3,"set_id":"auto-7jqfuwkw","title":"错题订正（3 题）","truncated":false,"url":"/m/practice/?set=auto-7jqfuwkw"}
PASS [contract] picked set loads
PASS [contract] 错题 tags and 参考答案待核，不自动判 on the two deferred questions — {"tags":"错题,错题,错题","pend":"q2:参考答案待核，不自动判,q3:参考答案待核，不自动判","title":"错题订正（3 题）"}

16/16 browser checks passed
PASS  L7 final-contract page states in headless Chrome: rev refresh, new-id retry, 429, recorded:false, picked-set tags (all passed)
PASS  L7b no code/token in the log of the contract browser run
PASS [not-ready] demo set loads
PASS [not-ready] heading 已提交（暂未判分）, 大脑接口还没准备好, every badge 待核对, no verdict — 已提交（暂未判分） / 待核对,待核对,待核对

2/2 browser checks passed
PASS  L8 grade endpoint not deployed: the page says 暂未判分 / 大脑接口还没准备好, never 已提交到大脑 (all passed)

TOTAL: 406 passed, 0 failed
```

## 未覆盖 / 需要后续验证

- 真实大脑的接口行为（本次是 mock）；真实卷的 `number` 形态与 60 题互不相同需在真实数据上复核。
- 真实 15 分钟票据到期；真实 4 MiB 级大图与慢网。
- iOS Safari / 微信内置浏览器（本次只有桌面 Chrome 的手机视口模拟）。
- ECS 上的 systemd 219 单元（只按 219 文档核对过指令名，没在 219 上跑过）；`ReadWritePaths=/var/lib/ruyi-site` 与目录属主 700 的组合需要在目标机上实测（见 `DEPLOY-P3.md`）。
- **三个接口都只对 mock 测过，要等大脑 #35 部署后对真实接口再跑一遍 Q/R/S 段**：确认练习站令牌的 scope 名（本站不写死）、真实 404/429/409 的错误体与大脑开发给出的结构一致（mock 已按该结构实现，见 S 节）、`reasons` 词表。
- 契约依据：`/workspace/shared/练习站自动方案接口.md` 与大脑开发的核对答复（文档没有描述错误体的结构，错误体以大脑开发的答复为准）。
- 选题（`due_first` / `wrong_only`）未部署时是 501「大脑接口未就绪」。
- ntfy 的真实投递（ECS 到 `ntfy.sh` 的出网）只用本地桩验证过。
