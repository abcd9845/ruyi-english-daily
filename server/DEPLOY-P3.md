# DEPLOY-P3：练习套提交 / 判分 / 归档 / 提醒 的上线清单

适用：`feat/practice-set-p1-p3`（依赖 P0 的 PR #11，须先合 #11）。本文只是**清单**，不含任何真实值；占位用 `<…>`。
本分支**不改** systemd 单元文件、不改 allowlist、不改旧页、不改家庭码逻辑、不改大脑写接口。下面的"单元改动"是**部署时在 ECS 上手工做的**。

## 0. 部署前要先确认的事（需要家长 / 大脑 PM 拍板）

| # | 事项 | 谁确认 | 说明 |
|---|---|---|---|
| 1 | **大脑判分接口的真实契约**（路径、请求/响应字段） | 大脑 PM | 站点按**草案**对接：`POST /v1/bank/grade`。契约全部集中在 `server/internal/brain/grade.go`（`encodeGradeRequest` / `decodeGradeResponse` / `DefaultGradePath`），路径还可用 `RUYI_BRAIN_GRADE_PATH` 改，不用动其它代码。拿到真契约前**不要**配 `RUYI_BRAIN_GRADE_TOKEN`，站点会走降级（只归档 + 提醒），页面不会声称"已提交到大脑"。 |
| 2 | **判分令牌（grade token）的范围** | 大脑 PM / 家长 | 必须是**单独**的令牌（站点拒绝它与 `RUYI_BRAIN_TOKEN` 相同），**只能**调用"判分并记录一次作答"这一个接口：不能读题库（不要给 `bank:read`）、不能写题库/答案/解析、不能改复习队列以外的任何东西、不能 `include_answer`。大脑侧对令牌应有单独的限速。站点绝不直接写大脑，也不持有别的写权限。 |
| 3 | **`client_attempt_id` 契约** | 大脑 PM | 站点生成 UUID（页面里 `crypto.randomUUID`），**同一份答案重试用同一个 id，改了答案换新 id**。它是**大脑侧幂等键**：同 id 再提交必须原样返回第一次的结果、不得重复记账、不得重复更新复习队列。站点自己也按这个 id 去重（内存 + 重启后从归档文件恢复），双保险。大脑返回必须回显 `client_attempt_id`、给出大脑自己的 `attempt_id` 且 `recorded:true`——三者缺一，站点就当"大脑没确认"而降级。 |
| 4 | **选题能力**（`wrong_only` / `due_first`） | 大脑 PM | 现在大脑没有选题接口：站点只能"整卷拉取 + 白名单 + 随机/顺序"。`wrong_only=1`、`due_first=1` 明确返回 **501**（`wrong_only_unavailable` / `due_first_unavailable`），绝不自己编"错题"逻辑。等大脑出接口后只改 `internal/brain/bank.go` 的 `SelectForPractice`。 |
| 5 | **ntfy**：`RUYI_NTFY_TOPIC` 是否沿用旧 topic | 家长 | 旧页里的 topic 是公开的；env 样例已要求用**新** topic。这个 topic 现在**同时**用于：①登录后 `/api/config` 下发给动态页（旧功能）；②服务端提交后"打招呼"推送（本期）。ECS 到 `ntfy.sh` 的出网需要通（不通也不影响判分，见下）。 |
| 6 | `RUYI_AUTO_SOURCES` 开不开 | 家长 | 默认空 = 关闭 `GET /api/practice/new`。要开就填允许自动出题的 source key（逗号分隔，真实 key 只写在 ECS 的 env 里）。 |
| 7 | 每套每小时提交上限 `RUYI_GRADE_MAX_PER_HOUR` | 家长 | 默认 5，按"会话 + 来源 IP"各限一次，**同一套**；重复提交同一个 `client_attempt_id`（重试）不占额度、校验不过的请求也不占。 |

## 1. 新增 / 变化的环境变量（全部可选，不配则行为 = P0）

| 变量 | 默认 | 说明 |
|---|---|---|
| `RUYI_BRAIN_GRADE_TOKEN` | 空 | 判分令牌。**空 = 不判分**（`mode:"archive_only"`）。写进 `/etc/ruyi-site/ruyi-site.env`，同样 600 root:root。日志里的 `[redacted]` 过滤器已包含它 |
| `RUYI_BRAIN_GRADE_PATH` | `/v1/bank/grade` | 判分接口路径（草案）。必须以 `/` 开头 |
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
- 文件是 JSON Lines，一次提交一行（`v,client_attempt_id,set_id,set_title,source,submitted_at,mode,brain_attempt_id,items[{qref,answer,status,correct}],summary,client{ua,ip}`）。**不含** cookie、令牌、家庭码、topic。
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
  $B/api/practice/<套号>/grade | jq '{mode,brain_recorded,summary,archived,notify,replayed}'
#   降级冒烟：mode=archive_only, brain_recorded=false, summary.pending=题数
#   判分冒烟：mode=graded,       brain_recorded=true
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

手机端：打开 `/m/practice/?set=<套号>`，答 1–2 题 → 点「提交」。降级时页面显示「已提交（暂未判分）」且**不会**出现「已记入大脑 / 已提交到大脑」；判分成功才显示「已提交到大脑」。

## 6. 回滚

- 只回滚二进制：`systemctl stop ruyi-site; cp -p /opt/ruyi-site/ruyi-site.p0.bak /opt/ruyi-site/ruyi-site; systemctl start ruyi-site`。P0 版没有提交功能，页面的提交按钮会 404（可接受，回到"只作答存草稿"）。
- 只关判分：删掉 `RUYI_BRAIN_GRADE_TOKEN` 重启 → 全部走归档 + 提醒。
- 只关自动出题：清空 `RUYI_AUTO_SOURCES` 重启。
- 归档目录与 `ReadWritePaths` 的改动留着无害；`attempts.jsonl` 里已有的记录是家长核对用的原始凭据，**回滚不要删**。

## 7. 已知限制（上线前心里有数）

- 自动套存在内存里，**重启即失效**（页面会提示"临时练习已过期，请重新出一套"）；已提交的作答不受影响。
- 站点内的重复提交去重依赖内存 + 归档文件；归档目录写不了时，重启后同一个 id 会再次被当作新提交（大脑侧幂等键仍会兜底）。
- 限速的"同 IP"取 TCP 对端地址，不信任 `X-Forwarded-For`（同旧限制）。
- 判分结果完全以大脑为准：`pending_key`（没有标准答案）/`unsupported`（题型不支持）一律显示「待核对」，不显示对错；站点没有任何自己判分的逻辑。
