# DEPLOY-P3：练习套提交 / 判分 / 归档 / 提醒 的上线清单

适用：`feat/brain-contract-adapter`（依赖链 **#11 → #12 → 本 PR**，须按顺序合；Refs #10）。本文只是**清单**，不含任何真实值；占位用 `<…>`。

> **⚠ 接口需等大脑 #35 部署**：`POST /v1/bank/grade`、`POST /v1/bank/questions/lookup`、`GET /v1/brain/pick-practice` 这三个接口线上现在**不存在**。在它们部署之前上线本版本也是安全的——站点会自动降级（lookup 退回整卷列表；选题 → 501「大脑接口未就绪」；判分 → 只归档 + 提醒，页面显示「暂未判分」，绝不说「已提交到大脑」）。**不要**在大脑部署前配 `RUYI_BRAIN_GRADE_TOKEN`；配了也只会得到 404 → 降级，不会 500。本分支所有测试只对 mock，**没有**对线上联调。
本分支**不改** systemd 单元文件、不改 allowlist、不改旧页、不改家庭码逻辑、不改大脑写接口。下面的"单元改动"是**部署时在 ECS 上手工做的**。

## 0. 部署前要先确认的事（需要家长 / 大脑 PM 拍板）

| # | 事项 | 谁确认 | 说明 |
|---|---|---|---|
| 1 | **大脑 #35 部署后复核契约** | 大脑 PM | 站点按「最终契约」对接：`POST /v1/bank/grade`（`{brain:"ruyi",client_attempt_id,items:[{qref,answer,rev?,attempt_kind?,answered_at?,session_ref?,payload?}]}`，≤50 题、qref 不重复、填空 ≤10 格；回复逐题 `{qref,status,correct?,attempt_id,recorded,duplicate}`；状态 `graded/pending_key/needs_review/unsupported`），错误 404（带 `details.qrefs` 的才是未知 qref，整批不写；不带 details 的兜底 `not_found` = 路由未部署）/409 `rev_mismatch`（`details` 为 qref/rev/current_rev）·`idempotency_conflict`/429 `rate_limited`（同题同脑每个上海日 >3 次，带 `details.qrefs` 与 `Retry-After`）；成功 201（新写入）/200（全是重放）。契约集中在 `server/internal/brain/grade.go`、`contract.go`，路径可用 `RUYI_BRAIN_GRADE_PATH` / `RUYI_BRAIN_LOOKUP_PATH` / `RUYI_BRAIN_PICK_PATH` 改。已按大脑开发的契约核对结论修正（见 PR 描述）：404 靠 `details.qrefs`/消息/哪个接口区分、严格 400 在站点发送前自校验、`answered_at` 不同 → 409、qref 小写上线并按发送串匹配回显、选题 `n` ≤10。 |
| 2 | **练习站令牌（`RUYI_BRAIN_GRADE_TOKEN`）的范围** | 大脑 PM / 家长 | 必须是**单独**的令牌（站点启动时拒绝它与 `RUYI_BRAIN_TOKEN` 相同）。按最终契约它是「`bank:read` + 只写 ruyi 一个脑」的练习站令牌；**scope 名由大脑定，本站不写死**——以后若换成更窄的 `bank:grade`，只需换令牌值，代码不用改。站点只拿它调 `POST /v1/bank/grade`（读题/选题仍用 `RUYI_BRAIN_TOKEN`），令牌只在服务端：不下发浏览器、不入归档、不进日志/响应/ntfy（有测试 grep 令牌标记串）。不能 `include_answer`、不能写题库/答案/解析。 |
| 3 | **`client_attempt_id` 契约** | 大脑 PM | 站点生成 UUID（页面里 `crypto.randomUUID`；契约允许 1–160 位 `[A-Za-z0-9._:-]`），**同一份答案重试用同一个 id（`answered_at` 也一起固定），用户改了答案换新 id**。大脑幂等键是 `ps:<client_attempt_id>:<qref>`：同 id 同答案 → 200 `duplicate:true` + 同一个 `attempt_id`；同 id 不同答案/结果 → 409 `idempotency_conflict`（站点先自查，页面收到后换新 id 自动重发一次）。站点自己也按 id 去重（内存 + 重启后从归档恢复），双保险。「已记入大脑」只看逐题 `recorded:true`；`unsupported`（`recorded:false`）、`pending_key`/`needs_review`（只记答案，页面「待核对」）都不会被说成判过分。 |
| 4 | **选题能力**（`mode=due_first\|wrong_only`） | 大脑 PM | 走大脑 `GET /v1/brain/pick-practice`（题文用 lookup）。**接口没部署时**返回 **501**「大脑接口未就绪」（`wrong_only_unavailable` / `due_first_unavailable`），不自己编"错题"逻辑；不带 `mode` 的随机套照旧。带 `deferred:*` 的题默认保留并标「参考答案待核，不自动判」，`RUYI_PICK_SKIP_DEFERRED=1` 则跳过。`/api/practice/new` 仍**默认关**（`RUYI_AUTO_SOURCES` 为空）。 |
| 5 | **ntfy**：`RUYI_NTFY_TOPIC` 是否沿用旧 topic | 家长 | 旧页里的 topic 是公开的；env 样例已要求用**新** topic。这个 topic 现在**同时**用于：①登录后 `/api/config` 下发给动态页（旧功能）；②服务端提交后"打招呼"推送（本期）。ECS 到 `ntfy.sh` 的出网需要通（不通也不影响判分，见下）。 |
| 6 | `RUYI_AUTO_SOURCES` 开不开 | 家长 | 默认空 = 关闭 `GET /api/practice/new`。要开就填允许自动出题的 source key（逗号分隔，真实 key 只写在 ECS 的 env 里）。 |
| 7 | 每套每小时提交上限 `RUYI_GRADE_MAX_PER_HOUR` | 家长 | 默认 5，按"会话 + 来源 IP"各限一次，**同一套**；重复提交同一个 `client_attempt_id`（重试）不占额度、校验不过的请求也不占。 |

## 1. 新增 / 变化的环境变量（全部可选，不配则行为 = P0）

| 变量 | 默认 | 说明 |
|---|---|---|
| `RUYI_BRAIN_GRADE_TOKEN` | 空 | 练习站令牌（见上 §0-2）。**空 = 不判分**（`mode:"archive_only"`），同时**不能用 `mode=due_first|wrong_only` 选题**（选题也用这枚令牌）。必须 ≠ `RUYI_BRAIN_TOKEN`（启动校验）。写进 `/etc/ruyi-site/ruyi-site.env`，同样 600 root:root。日志里的 `[redacted]` 过滤器已包含它 |
| `RUYI_BRAIN_GRADE_PATH` | `/v1/bank/grade` | 判分接口路径。必须以 `/` 开头 |
| `RUYI_BRAIN_LOOKUP_PATH` | `/v1/bank/questions/lookup` | 按 qref 取题文（读令牌） |
| `RUYI_BRAIN_PICK_PATH` | `/v1/brain/pick-practice` | 大脑选题。**用判分令牌 `RUYI_BRAIN_GRADE_TOKEN`**（契约要目标练习脑的 write 权限；读令牌调它是 403）。没配判分令牌 → 选题 501「大脑接口未就绪（选题需要配置判分令牌）」，不会改用读令牌；大脑回 401/403 → 502 `pick_forbidden`「选题令牌无权限」 |
| `RUYI_PICK_SKIP_DEFERRED` | `0` | `1` = 跳过带 `deferred:*` 的选题结果；`0` = 保留并标「参考答案待核」 |
| `RUYI_ATTEMPTS_DIR` | `/var/lib/ruyi-site/attempts` | 作答归档目录，见下。绝对路径 |
| `RUYI_GRADE_MAX_PER_HOUR` | `5` | 1–1000 |
| `RUYI_NTFY_TOPIC` | 空 | 同旧变量；空则不推送（`notify:"off"`），其它照常 |
| `RUYI_NTFY_BASE_URL` | `https://ntfy.sh/` | 一般不用配；测试用 |
| `RUYI_NTFY_RETRY_DELAY` | `1m` | 推送失败后异步重试一次的等待时间；一般不用配 |
| `RUYI_AUTO_SOURCES` | 空 | 允许自动出题的 source，逗号分隔 |
| `RUYI_AUTO_TTL` | `6h` | 自动套有效期（内存里，重启即失效） |

启动时不会因为这些变量里"目录不可写"而退出（只在第一次提交时打一条 error 日志，且不带路径/内容）；变量格式不对则退出并只报变量名。

## 2. 作答归档目录（`RUYI_ATTEMPTS_DIR`）

- 站点以 `ruyi-site` 用户运行，**第一次提交时**才创建目录（0700）和 `attempts.jsonl`（0600）。建议**部署时预先建好**，这样权限和属主不靠运气：

  ```bash
  install -d -m 700 -o ruyi-site -g ruyi-site /var/lib/ruyi-site
  install -d -m 700 -o ruyi-site -g ruyi-site /var/lib/ruyi-site/attempts
  ls -ld /var/lib/ruyi-site/attempts     # 期望：drwx------ ruyi-site ruyi-site
  ```
- 文件是 JSON Lines，一次提交一行（v2）。顶层：`v`（=2）、`client_attempt_id`、`set_id`、`set_title`、`source`、`submitted_at`、`mode`（`graded` = 大脑答复了 / `archive_only` = 降级）、`brain_note`（降级原因：`not_configured` / `not_ready` / `unavailable`，未降级时没有）、`sig`（答案指纹，用于识别「同 id 换答案」）、`items`、`summary`（`total/graded/correct/pending/unsupported/unanswered/recorded`）、`client{ua,ip}`。每题 `items[]`：`qref`、`answer`、`status`、`correct`、`rev`（学生看到的题目版本，未知则无）、`recorded`（大脑确认落库才为 true）、`attempt_id`（大脑的整数 id，没落库则无）、`duplicate`（大脑认为是重放时才有）。旧的 v1 行（单个 `brain_attempt_id`）仍可读。**不含** cookie、令牌、家庭码、topic。
- 它同时是"重启后仍能识别重复提交"的依据（启动时重读，文件上限 64 MiB 以内）。**不要手工改它。** 备份/清理前先停服务。
- 目录写不了：答案照样判、照样推送，响应里 `archived:false`，日志只有一条 error。

## 3. systemd 单元要做的改动（CentOS 7 = systemd 219，**手工改 ECS 上的单元**，本分支未改仓库里的样例单元）

当前样例单元用了 `ProtectSystem=strict` + `ReadWritePaths=/var/log/ruyi-site`。strict 下整个文件系统只读，所以**必须**把归档目录加入可写路径：

```ini
ReadWritePaths=/var/log/ruyi-site /var/lib/ruyi-site
```

219 的注意点：
- **`StateDirectory=` 不能用**（systemd 235 才有），目录要像第 2 节那样自己建。
- `ReadWritePaths=` 里的路径**必须在服务启动前已存在**，否则 219 会以 `status=226/NAMESPACE` 起不来。所以先建目录，再改单元，再 `systemctl daemon-reload && systemctl restart ruyi-site`。
- `ProtectSystem=strict`（214+）、`PrivateTmp`、`NoNewPrivileges`、`ProtectHome` 在 219 都支持，不用动。`ProtectKernel*` 之类 219 不支持的指令**不要**加。
- 出网：单元没限制网络，站点到 ntfy 走 HTTPS（需 ECS 能访问 `ntfy.sh`，用的是系统 CA）。

## 4. 部署顺序

1. 取 `dist/ruyi-site-p1p3-linux-amd64` 与 `.sha256`，在 ECS 上 `sha256sum -c`。
2. 备份当前二进制：`cp -p /opt/ruyi-site/ruyi-site /opt/ruyi-site/ruyi-site.p0.bak`。
3. 建归档目录（第 2 节）；改单元的 `ReadWritePaths`（第 3 节）；`daemon-reload`。
4. 先**不配** `RUYI_BRAIN_GRADE_TOKEN`、不配 `RUYI_AUTO_SOURCES`：换二进制、`systemctl restart ruyi-site`，跑第 5 节的"降级冒烟"。
5. 大脑判分接口确认后（第 0 节 #1–#3）再加 `RUYI_BRAIN_GRADE_TOKEN`（以及必要时 `RUYI_BRAIN_GRADE_PATH`），重启，跑"判分冒烟"。
6. 需要自动出题再加 `RUYI_AUTO_SOURCES`，重启。
7. `RUYI_SETS_FILE` 的内容（真实 qref）只放在 ECS 上；格式样例见 `deploy/sets.example.json`（只有占位）。

## 5. 验证命令（把 `<host>`、`<家庭码>` 换成真实值，**不要**把它们贴进聊天/日志）

```bash
B=http://127.0.0.1:18082
curl -s -o /dev/null -w '%{http_code}\n' $B/healthz                         # 200
curl -s -c /tmp/j -H 'Content-Type: application/json' -d '{"code":"<家庭码>"}' $B/api/login -o /dev/null
curl -s -b /tmp/j $B/api/practice/<套号> | jq '.items|length'               # 题数
# 提交（client_attempt_id 用 uuidgen；答案按题型：选择题 "B"，填空 ["…","…"]，多选 ["A","C"]，自由作答 "…"）
CID=$(uuidgen)
curl -s -b /tmp/j -H 'Content-Type: application/json' \
  -d "{\"client_attempt_id\":\"$CID\",\"answers\":[{\"qref\":\"<套内的 qref>\",\"answer\":\"B\"}]}" \
  $B/api/practice/<套号>/grade | jq '{mode,brain_recorded,brain_note,recorded_count,summary,archived,notify,replayed}'
#   降级冒烟：mode=archive_only, brain_recorded=false, recorded_count=0；brain_note=not_ready（接口未部署）/ not_configured / unavailable
#   判分冒烟：mode=graded,       brain_recorded=true,  recorded_count=逐题 recorded:true 的数量（unsupported 的题不算）
#   同一个 CID 改了答案再交：409 idempotency_conflict（页面会自动换新 id）；同一题一天交第 4 次：429
# 同一个 CID 再来一次：replayed=true，且归档文件不新增行、大脑不重复记账
curl -s -b /tmp/j -H 'Content-Type: application/json' -d "<同上>" $B/api/practice/<套号>/grade | jq .replayed
# 归档
stat -c '%a %U' /var/lib/ruyi-site/attempts /var/lib/ruyi-site/attempts/attempts.jsonl   # 700 / 600，都是 ruyi-site
tail -n1 /var/lib/ruyi-site/attempts/attempts.jsonl | jq 'keys'
# 日志里不应出现任何秘密
journalctl -u ruyi-site --since '10 min ago' | grep -c -F -e '<家庭码>' -e '<令牌>'    # 0
# 旧页/白名单未变
curl -s -o /dev/null -w '%{http_code}\n' $B/server.log                       # 404
```

手机端：打开 `/m/practice/?set=<套号>`，答 1–2 题 → 点「提交」。降级时页面显示「已提交（暂未判分）」（接口未部署时还会写「大脑接口还没准备好」），且**不会**出现「已记入大脑 / 已提交到大脑」。判断是逐题的，只有 `recorded:true` 的题算已进大脑：全部 `recorded:false` 才显示「已提交（仅留档，未记入大脑）」；部分记录显示「已提交到大脑（N / M 题）」并标出未记录的题（该题「不自动判分」标签 + 附注）。

选题冒烟（**需大脑 #35 已部署**，否则应得 501）：`curl -s -b /tmp/j "$B/api/practice/new?source=<source>&n=3&mode=wrong_only" | jq '{mode,count,deferred,url}'`；未部署时 `{error:"wrong_only_unavailable",message:"…大脑接口未就绪…"}`，HTTP 501（不是 500）。

## 6. 回滚

- 只回滚二进制：`systemctl stop ruyi-site; cp -p /opt/ruyi-site/ruyi-site.p0.bak /opt/ruyi-site/ruyi-site; systemctl start ruyi-site`。P0 版没有提交功能，页面的提交按钮会 404（可接受，回到"只作答存草稿"）。
- 只关判分：删掉 `RUYI_BRAIN_GRADE_TOKEN` 重启 → 全部走归档 + 提醒。
- 只关自动出题：清空 `RUYI_AUTO_SOURCES` 重启。
- 归档目录与 `ReadWritePaths` 的改动留着无害；`attempts.jsonl` 里已有的记录是家长核对用的原始凭据，**回滚不要删**。
- 装回旧二进制前注意：旧二进制遇到 `sets.json` 中的 smoke 条目会启动失败，须先删除该条目（见 §8.3）。

## 7. 已知限制（上线前心里有数）

- 自动套存在内存里，**重启即失效**（页面会提示"临时练习已过期，请重新出一套"）；已提交的作答不受影响。
- 站点内的重复提交去重依赖内存 + 归档文件；归档目录写不了时，重启后同一个 id 会再次被当作新提交（大脑侧幂等键仍会兜底）。
- 限速的"同 IP"取 TCP 对端地址，不信任 `X-Forwarded-For`（同旧限制）。
- 判分结果完全以大脑为准：`pending_key`（没有标准答案）/`needs_review` 显示「待核对」、`unsupported` 显示「不自动判分」，都不显示对错；站点对真题没有任何自己判分的逻辑（唯一例外是 §8 的本地测试套 `smoke-test`，它是服务端自判、不进大脑）。
- 网络重试：大脑超时/5xx 时站点用同一个 `client_attempt_id` 和同一请求体重试一次（靠大脑幂等不重复记账）；仍失败 → 降级 `archive_only`，该 id 之后在站点内只会重放这条归档记录（要重新记入大脑需用户换答案/再做一次得到新 id）。
- 同一题每个上海日最多记 3 次（大脑 429，带 `Retry-After`）：整批拒绝，页面提示「今天这题提交次数到上限」，答案保留，不归档、不提醒。
- 大脑只收每个作答 ≤500 字；更长的自由作答（站点允许 ≤2000）不发给大脑，只留档、页面「不自动判分」。
- 选题 `n` 大脑上限 10，站点请求更多时降为 10 并在页面提示。
- 归档 v2：新行带逐题 `recorded`/`attempt_id`；v1 旧行仍能重放。回滚到旧二进制时，旧版读不懂 v2 行的新字段会忽略它们（只用于重放，不影响数据）。

## 8. 本地测试套（`smoke-test`）：让家长自己点一次「提交」，验证归档 + ntfy，不碰真题、不碰大脑

分支 `feat/local-smoke-set`（base = `feat/brain-contract-adapter`）。链接：`/m/practice/?set=smoke-test`。

**是什么**：一种"本地套"（`"local": true`）：题目直接写在 `sets.json` 里（假题，只有 1 道、题干标「测试题，随便填」），不读大脑、**提交时永不调大脑**（即使配了 `RUYI_BRAIN_GRADE_TOKEN`）。页面有「检查」按钮（服务端判对错，**答案不下发浏览器**）和「提交」；提交后固定 `mode: archive_only`、`brain_note: test_set`，页面显示「已提交（仅留档，未记入大脑）」，不会出现「已提交到大脑」。

### 8.1 怎么部署

1. 部署带本功能的二进制（`ruyi-site-smoke-linux-amd64`，含 #11+#12+#13；**不含 #14 ntfy 修复**，与 #14 合并后需一起部署，见 PR 描述）。
2. 把下面这个条目**追加**到 `/opt/ruyi-site/sets.json` 顶层 `"sets"` 数组末尾（该文件在 ECS 上，不进仓库；条目本身只有假值，同 `deploy/sets.smoke-entry.json`）：

```json
{
  "id": "smoke-test",
  "local": true,
  "title": "测试套（随便填，只测提交和推送）",
  "questions": [
    {
      "qref": "smoke:q1",
      "qtype": "single_choice",
      "stem": "【测试题，随便填】这道题只用来测试「提交」和「推送」，选哪个都行。想看「检查」的对错，就选 B。\n\n1 + 1 = ?",
      "options": [
        { "key": "A", "text": "1" },
        { "key": "B", "text": "2" },
        { "key": "C", "text": "3" }
      ],
      "answer": ["B"]
    }
  ]
}
```

   用 jq 追加（先备份，写到临时文件、校验后再替换）：

   ```bash
   cp -p /opt/ruyi-site/sets.json /opt/ruyi-site/sets.json.bak
   jq '.sets += [input]' /opt/ruyi-site/sets.json smoke-entry.json > /opt/ruyi-site/sets.json.new   # smoke-entry.json = 上面的条目
   jq -e '.sets|map(.id)|index("smoke-test")' /opt/ruyi-site/sets.json.new && mv /opt/ruyi-site/sets.json.new /opt/ruyi-site/sets.json
   ```

   完整文件示例见 `deploy/sets.smoke.example.json`（一个占位真实套 + `smoke-test`）。
3. `systemctl restart ruyi-site`（套文件只在启动时读取；有错会**启动即退出**，错误只写位置不写内容：`journalctl -u ruyi-site -n 20`）。
4. 家长手机打开 `https://<站点>/m/practice/?set=smoke-test`，输入家庭码 → 选一个选项（想看对错可点「检查」；选 B 是对的）→ 点「提交」。每套每小时最多 5 次提交（按会话 + 来源 IP，沿用 `RUYI_GRADE_MAX_PER_HOUR`）。草稿存在手机本地 `ruyi-practice-smoke-test-v1`。

不需要新增任何环境变量。**不要**把 `local.smoke`（保留 source）写进 `RUYI_AUTO_SOURCES` / `RUYI_ALLOWED_SOURCES`——会启动即报错。

### 8.2 怎么识别测试提交

- **归档**（`/var/lib/ruyi-site/attempts/attempts.jsonl`）：该行有 `"test":true`，`mode:"archive_only"`、`brain_note:"test_set"`、`source:"local.smoke"`、`set_id:"smoke-test"`、`items[].recorded:false`。真题提交的行**没有** `test` 字段。
  `jq -c 'select(.test==true)' /var/lib/ruyi-site/attempts/attempts.jsonl`
- **ntfy**：标题以「【测试】」开头（HTTP 头按 RFC 2047 编码，ntfy 会解码；真题标题仍是原来的 `Ruyi practice`），正文第一行以「【测试】如意练习：…」开头、含「未记入大脑」，正文 JSON 带 `"test":true`。
- **大脑侧**：这次提交**没有**任何请求到大脑（判分/取题/选题全是 0）。
- 注意：sets 文件非空时，每次重启有 1 次只读 lookup 探测（启动后异步发一个明显不存在的 qref，只写日志、不写任何数据、不影响启动）；这不是测试提交产生的请求。

### 8.3 怎么回滚

- 删掉 `sets.json` 里的 `smoke-test` 条目（或 `cp -p /opt/ruyi-site/sets.json.bak /opt/ruyi-site/sets.json`），`systemctl restart ruyi-site`；`?set=smoke-test` 随即 404「没有这套题」。
- 或者装回旧二进制：`cp -p /opt/ruyi-site/ruyi-site.<旧备份> /opt/ruyi-site/ruyi-site && systemctl restart ruyi-site`。**注意**：旧二进制不认识 `local` / `questions` 字段，`sets.json` 里还留着 `smoke-test` 条目时会**启动即退出**（严格解析）——装回旧二进制前**必须先删掉该条目**。
- 已经写进 `attempts.jsonl` 的测试行是普通行（带 `test:true`），可留可滤，不要手改文件。

### 8.4 约束（安全）

- **保留命名空间**：本地套 id 必须以 `smoke-` 或 `test-` 开头，qref 必须以 `smoke:` 开头，source 固定为 `local.smoke`（可不写）；真实套**不得**使用这些 id 前缀 / qref 前缀 / source，也不能带 `questions`。违反任一条：启动即退出。
- 本地套不能写 `qrefs`、必须有 `questions`；不会被 `/api/practice/new`（自动选题）选中，`local.smoke` 不是大脑 source（`/api/sets/…`、`/api/media/…` 对它一律 404）。
- 答案字段（`questions[].answer`）只在服务端，用于「检查」和提交后的对错判断，不出现在任何响应、归档题干、ntfy 里（有测试 grep）。
