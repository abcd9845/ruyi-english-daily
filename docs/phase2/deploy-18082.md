# 阶段2：18082 部署说明（给 ECS运维）

关联：#1

> 二进制由开发在接口约定后交付，当前文档只用于提前准备环境。

## 1. 目标与原则

- 新进程跑在 18082 端口。
- 不动 18081 现有 python 进程，不动旧文件。
- 回滚 = 停掉新 systemd 单元即可。
- 不用 root 跑新服务。
- 本仓库是公开的：任何密钥、家庭码、令牌、服务器地址都不要贴到 Issue、PR、聊天里。

## 2. 已知现状（来自 ECS运维 核对）

- 18081 是 root 启动的 `python3 -m http.server` 裸进程。
- 不是 systemd 管理，绑定 `0.0.0.0`。
- 目录里除了仓库的 17 个文件，还有 `server.log`、`server.pid`、`start-server.sh`、`stop-server.sh`。
- 该目录不是 git 仓库。
- 上述额外文件目前会被对外提供。
- 建议：新服务端只托管白名单，即仓库里已有的文件，不提供这些额外文件。

## 3. 前置检查清单

新服务用 Go 静态二进制，不需要在 ECS 上装 go、nginx、node。以下只是确认现状。

```bash
# 是否安装（没有也没关系）
command -v go nginx node || true

# 系统与 systemd 版本（CentOS 7 预期为 219）
cat /etc/redhat-release
systemctl --version | head -1
uname -r
```

需要确认的事项：

- [ ] 安全组（和系统防火墙）放行 TCP 18082。
- [ ] ECS 能出站访问大脑基址。基址待大脑开发接口约定，下面用占位符。

```bash
# <BASE> 为大脑基址，待接口约定后由大脑开发提供
curl -sS -o /dev/null -w '%{http_code}\n' <BASE>/healthz
```

- 返回 200：可达。
- 超时或连接失败：先排查出站网络，再继续。

## 4. 创建用户与目录

不要用 root 跑服务。创建专用低权限用户 `ruyi`。

```bash
# 专用用户，不可登录，无家目录
useradd --system --shell /sbin/nologin --no-create-home ruyi

# 二进制目录（属主 root，ruyi 只读执行）
mkdir -p /opt/ruyi-site
chown root:root /opt/ruyi-site
chmod 755 /opt/ruyi-site

# 配置目录
mkdir -p /etc/ruyi
chown root:root /etc/ruyi
chmod 750 /etc/ruyi

# 日志目录
mkdir -p /var/log/ruyi-site
chown ruyi:ruyi /var/log/ruyi-site
chmod 750 /var/log/ruyi-site
```

静态目录 `RUYI_STATIC_DIR`（默认 `/opt/ruyi-english-daily`）：

- 属主是 root 也可以，但 `ruyi` 用户必须可读：目录 755，文件 644。
- 服务只读该目录，不会写入。

```bash
# 检查 ruyi 用户能否读取
sudo -u ruyi ls /opt/ruyi-english-daily >/dev/null && echo readable
```

## 5. 环境文件 /etc/ruyi/ruyi.env

权限 600。属主 root 或 ruyi 均可（systemd 以 root 读取 `EnvironmentFile`，root 属主最稳妥）。

```bash
touch /etc/ruyi/ruyi.env
chown root:root /etc/ruyi/ruyi.env
chmod 600 /etc/ruyi/ruyi.env
```

模板如下。尖括号内是占位符，请替换为真实值，且不要把真实值贴到任何聊天或 Issue 里。

```ini
RUYI_LISTEN=:18082
RUYI_STATIC_DIR=/opt/ruyi-english-daily
RUYI_FAMILY_CODE=<由家长提供，勿贴聊天>
RUYI_SESSION_SECRET=<随机32字节hex>
RUYI_SESSION_TTL=720h
RUYI_COOKIE_SECURE=0
RUYI_BRAIN_BASE_URL=<待大脑开发接口约定>
RUYI_BRAIN_TOKEN=<bank:read 令牌，由家长/大脑侧发放，勿贴聊天>
RUYI_LOG_LEVEL=info
```

生成会话密钥：

```bash
openssl rand -hex 32
```

注意：

- `RUYI_FAMILY_CODE` 由家长决定，是新码，不沿用旧页里的任何码。
- `RUYI_COOKIE_SECURE=0` 仅因当前是 http。上 HTTPS 后改为 1。
- **令牌不得放命令行参数**（`ps` 可见）。只通过 env 文件注入。
- **日志不得记录** Authorization、Cookie、ticket。部署后要抽查。
- 改家庭码或会话密钥后，重启服务即可让旧 cookie 全部失效。

## 6. systemd 单元

`/etc/systemd/system/ruyi-site.service`

CentOS 7 的 systemd 是 219。下面只用 219 支持的指令。

```ini
[Unit]
Description=Ruyi English Daily site (phase 2)
After=network.target

[Service]
Type=simple
User=ruyi
Group=ruyi
EnvironmentFile=/etc/ruyi/ruyi.env
ExecStart=/opt/ruyi-site/ruyi-site
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=/opt/ruyi-english-daily
ReadWritePaths=/var/log/ruyi-site
LimitNOFILE=4096

[Install]
WantedBy=multi-user.target
```

说明：

- 不写 `User=`/`Group=` 以外的提权相关指令。
- 日志默认进 journal；`/var/log/ruyi-site` 备用。
- 若 `ProtectSystem=strict` 在该机器上起不来，先改成 `ProtectSystem=full` 再试，并告知开发。

加载与启停：

```bash
systemctl daemon-reload
systemctl enable ruyi-site
systemctl start ruyi-site
systemctl status ruyi-site
systemctl restart ruyi-site
systemctl stop ruyi-site
```

查看日志：

```bash
journalctl -u ruyi-site -n 100 --no-pager
journalctl -u ruyi-site -f
```

## 7. 试跑冒烟（部署者自测）

只做冒烟。登录、cookie、取图等逐条验收由大脑测试负责。

```bash
# 首页应为 200
curl -i http://127.0.0.1:18082/

# 额外文件不应被提供，应为 404
curl -i http://127.0.0.1:18082/server.log
curl -i http://127.0.0.1:18082/start-server.sh

# 旧 18081 不受影响，应仍为 200
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18081/
```

再确认：

- `ss -ltnp | grep -E '18081|18082'` 能看到两个监听，且 18082 的进程是 `ruyi` 用户。
- `journalctl -u ruyi-site` 里没有令牌、Cookie、家庭码。

## 8. 回滚

新服务出任何问题，直接停掉。旧 18081 不受影响。

```bash
systemctl stop ruyi-site
systemctl disable ruyi-site
```

## 9. 切换到 18081（概要）

> 仅在「验收通过且家长同意后」才做。具体步骤届时另文说明，这里只列概要。

切换前：

- 先把旧 python 进程的启动命令记录下来，即 `start-server.sh` 的内容。
- 不要删除 `start-server.sh`、`stop-server.sh` 等旧文件。

切换时：

1. 停旧 python：用它自带的 `stop-server.sh`，或 kill 对应 pid。
2. 把 env 文件里的 `RUYI_LISTEN` 改成 `:18081`。
3. 重启新单元，并做冒烟检查。

```bash
# 示例：查旧进程
cat /path/to/old-dir/server.pid
```

切换失败的回滚：

1. `systemctl stop ruyi-site`
2. 按 `start-server.sh` 的内容把旧进程拉起来。

---

二进制由开发在接口约定后交付，当前文档只用于提前准备环境。
