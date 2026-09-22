# trimapps

> 以下内容由AI生成，自行辨别
> 登录用户`admin`，密码`123456`
将飞牛 OS（fnOS）的部分系统应用移植到普通 ARM64 Linux（Debian / Ubuntu）运行的实验项目。

包含 4 个可独立打包、独立运行的服务，均以 Go 编写的**启动器（wrapper）**形式存在：Go 二进制负责环境准备、数据库初始化、子进程管理，真正的业务核心是飞牛系统里提取出来的预编译二进制（`trim-media`、`trim-music`、`mediasrv`）。

> **关于 fntv**：fntv 的业务核心 `trim-media`、数据文件 `init.sql` 以及各服务的本体安装包，均来自第三方项目 [kesry/trimmedia-linux-arm](https://github.com/kesry/trimmedia-linux-arm)（由他人从飞牛系统提取并预编译），本仓库只是套了一层 Go 启动器和打包/安装脚本，**fntv 及其承载的服务并非原创**。

## 目录结构

```
apps/
├── rpcbroker/    # 飞牛系统服务（应用中心 / RPC）的本地伪造实现
├── mediasrv/     # 转码服务 mediasrv 的启动器 + nodri.so 打桩库
├── fntv/         # 飞牛影视（trim-media）启动器
└── fnmusic/      # 飞牛音乐（trim-music）启动器
nodri.c           # LD_PRELOAD 打桩源码，mediasrv 打包时编译为 nodri.so
Makefile          # 统一打包入口，产物汇总到 out/
out/              # 打包产物（<应用名>.tgz）
temp/             # 参考用的原始提取文件，不参与构建
```

## 架构与依赖关系

```
                        ┌───────────────┐
      HTTP  :8001       │   rpcbroker   │   伪造飞牛系统级服务
   （unix socket 上的   │  (Go 启动器)   │   com.trim.main / usersrv /
     应用中心 API）     └──────┬────────┘   sysinfo / filestor 等
        /run/com.trim.app.center.sock        /run/trim_app_cgi/rpcbroker
        /run/trim_app_cgi/rpcbroker              │ 提供 RPC（服务注册/用户/存储卷/授权目录）
                                                   │
          ┌────────────────────────┬───────────────┴──────────────┐
          ▼                        ▼                              ▼
   ┌────────────┐           ┌────────────┐                 ┌────────────┐
   │    fntv    │           │  fnmusic   │                 │  mediasrv  │
   │  :8005     │           │   :8007    │                 │（unix sock）│
   └─────┬──────┘           └─────┬──────┘                 └─────┬──────┘
   启动 trim-media           启动 trim-music                预编译二进制
   （Web 端口 8005）         （socket：trim_music.socket）      │
         │                        │                    /var/run/mediasrv.socket
         └──── RPC 走 rpcbroker ──┴──── 转码调用 mediasrv ──────┘
                        │
                   MEDIA_DIR（媒体库目录，默认 /vol1/1000）
```

依赖关系说明：

- **rpcbroker 是一切的前提**。`trim-media` / `trim-music` 启动后会通过 `/run/trim_app_cgi/rpcbroker` 这个 unix socket，以自定义 CPRT 二进制协议请求 `com.trim.rpcbroker.apply`（服务注册）、`com.trim.usersrv.getUserId`（取用户 uid）、`com.trim.filestor.getAppAuthorizedDir`（媒体目录授权）、`com.trim.sysinfo.getAllVolsInfo`（存储卷信息）等"系统服务"。rpcbroker 就是这些请求的伪造应答方，**必须最先启动**。它还监听 `/run/com.trim.app.center.sock` 提供应用中心 HTTP 接口（auth-path 查询）。
- **fntv、fnmusic 依赖 rpcbroker**，不依赖对方的进程，但共用 `MEDIA_DIR` 指向的媒体目录。
- **fntv（trim-media）依赖 mediasrv**：视频转码/媒体分析通过 unix socket `/var/run/mediasrv.socket` 调用 mediasrv。mediasrv 本身可独立启动，但没有被依赖启动的逻辑，建议其在 fntv 之前启动。
- **fnmusic 不依赖 mediasrv**：`trim-music` 的转码走包内自带的 wasm 版 ffmpeg，与 mediasrv 无关。
- **mediasrv 依赖 nodri.so**（`LD_PRELOAD` 打桩，见下文）。

## 环境要求

| 项目 | 要求 |
|---|---|
| CPU 架构 | arm64（预编译核心仅产出 arm64） |
| 操作系统 | Debian / Ubuntu（`install.sh` 按此检测，其他发行版需自行改造） |
| 权限 | root（需要创建 `/run`、`/var/apps`、`/vol1` 下的 socket / 软链接 / 目录） |
| 构建机 | Go 1.2x+；打包 mediasrv 需要 `gcc`（编译 `nodri.c`）；可选 `upx`（二进制压缩，缺失自动跳过） |
| 运行期系统包 | `sqlite3`、`wget`（fnmusic 还需 `libzmq5`，由各 install.sh 自动安装） |

## 环境变量

所有启动器共用同一套环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MEDIA_DIR` | `/vol1/1000` | 媒体库根目录。rpcbroker 会枚举其一级子目录作为授权目录（跳过 `mediasrv.transcode`）；若 `MEDIA_DIR != /vol1/1000`，rpcbroker 会重建 `/vol1` 并建立 `/vol1/1000 -> MEDIA_DIR` 软链接来适配核心的写死路径 |
| `WEB_PORT` | fntv `8005`，fnmusic `8007` | 对外 HTTP 端口（mediasrv / rpcbroker 不监听 TCP，仅 unix socket） |
| `LD_LIBRARY_PATH` | 各程序自动推导 | 手动设置可覆盖默认库搜索路径 |
| `LOG_LEVEL` | `info` | 预留，当前仅读取 |
| `PROXY_PREFIX` | 空 | `install.sh` 下载 GitHub 资源时的代理前缀（如 `https://ghproxy.net/`） |

## 构建

在仓库根目录使用 Makefile，自动发现 `apps/*/package.sh` 并逐个打包，产物汇总为 `out/<应用名>.tgz`：

```bash
make            # 打包全部项目 → out/{rpcbroker,mediasrv,fntv,fnmusic}.tgz
make list       # 列出可单独打包的项目
make fntv       # 只打包某一个（make <目录名>）
make clean      # 清理 out/ 及各项目临时产物
```

每个 `package.sh` 做同样的事：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64` 交叉编译 Go 启动器（可选 upx 压缩），与 `install.sh`、SQL、内置 `lib/` 一起打成 tgz。特殊点：

- **mediasrv**：额外用 `gcc -shared -fPIC -o nodri.so ../../nodri.c` 现场编译打桩库并随包携带。
- **fnmusic**：内置 `lib/libzmq.so.5`（`trim-music` 除 libc 外唯一的非系统动态库依赖），随包携带后 `LD_LIBRARY_PATH` 只需指向包内 `lib/`。
- **fntv**：内置 `lib/libwebp.so.7`（`trim-media` 的运行期依赖）。

## 部署与启动

通用流程：把 `out/<应用>.tgz` 传到目标机，解到独立目录，先跑 `install.sh`（若有），再直接运行 arm64 二进制。所有进程以前台方式运行，`Ctrl+C` / SIGTERM 会级联杀掉子进程（启动器用 `Setpgid + Pdeathsig` 管理），生产环境建议自行套 systemd / nohup。

### 1. rpcbroker（必须最先启动）

```bash
mkdir -p /opt/trim/rpcbroker && tar xzf rpcbroker.tgz -C /opt/trim/rpcbroker
MEDIA_DIR=/vol1/1000 /opt/trim/rpcbroker/rpcbroker.arm64
```

无 install.sh、无额外依赖。启动后创建两个 unix socket：
`/run/trim_app_cgi/rpcbroker`（CPRT RPC）和 `/run/com.trim.app.center.sock`（应用中心 HTTP）。

### 2. mediasrv（转码服务）

```bash
mkdir -p /opt/trim/mediasrv && tar xzf mediasrv.tgz -C /opt/trim/mediasrv
cd /opt/trim/mediasrv && ./install.sh     # 下载 trim-media-lib.zip / lib.extends.zip（核心二进制 + 全套 so）
./mediasrv.arm64                          # 启动器包装 bin/mediasrv
```

启动器做的事情：

- 以 `-a /var/run/mediasrv.socket` 参数拉起预编译的 `bin/mediasrv`；
- 设置 `LD_LIBRARY_PATH` 为 `lib:lib/mediasrv:lib/mediasrv/lib:extends`；
- 设置 `LD_PRELOAD=nodri.so`；
- 预建 `/usr/trim/etc/`（核心二进制要求该目录存在）。

**nodri.so 的作用**：mediasrv 启动时检测 `/dev/dri`，一旦存在就进入"用静态链接的 libpci 枚举 PCI 上的 GPU"分支；在无 PCI 枚举能力的平台（如 MT6833 手机 SoC，`/sys/bus/pci` 等全部 ENOENT）该分支会直接崩溃退出。`nodri.c` 通过拦截 `open/stat/access` 等调用，仅对本进程把 `/dev/dri` 伪造成不存在，使 mediasrv 跳过 GPU 分支、回退 CPU 模式。若目标机本身没有 `/dev/dri`（如 mt6771），不打桩也能跑。

### 3. fntv（飞牛影视）

```bash
mkdir -p /opt/trim/fntv && tar xzf fntv.tgz -C /opt/trim/fntv
cd /opt/trim/fntv && ./install.sh         # 装 sqlite3/wget/libc6，下载 trim.media.tar.gz（含 trim-media 核心）
MEDIA_DIR=/vol1/1000 WEB_PORT=8005 ./fntv.arm64
```

启动器流程：

1. 建库目录 `data/database/`，首次运行时用 `sed` 把 `init.sql` 里写死的 `/vol1/@appmeta/trim.media`、`/vol1` 路径替换为实际路径，再用 `sqlite3` 初始化 `trimmedia.db`；
2. 以 `--port/--static/--root/--meta` 等参数拉起 `trim.media/trim-media`，`LD_LIBRARY_PATH` 指向包内 `lib/`；
3. 子进程独立进程组运行，父进程退出时自动收组清理。

访问 `http://<host>:8005`。**前提：rpcbroker 已运行**，否则 trim-media 拿不到用户/目录信息。

### 4. fnmusic（飞牛音乐）

```bash
mkdir -p /opt/trim/fnmusic && tar xzf fnmusic.tgz -C /opt/trim/fnmusic
cd /opt/trim/fnmusic && ./install.sh      # 装 sqlite3/wget/libzmq5，下载 trim.music.tar.gz
MEDIA_DIR=/vol1/1000 WEB_PORT=8007 ./fnmusic.arm64
```

启动器流程：

1. `trim-music` 二进制不接受任何参数，路径全部写死在 `/var/apps/trim.music` 下 —— 启动器建软链接 `/var/apps/trim.music/target -> 包内 trim.music/` 来对接；
2. **绝不提前建库**：`trim-music` 首启会自己执行包内 `sqlite*.sql` 建 schema 并创建歌词分片库（`lyric-xx.db`）。若抢先 `sqlite3` 建 `music.db`，升级流程被跳过、分片库缺失，`trim-music` 会 panic。因此启动器只保证目录存在；
3. 后台轮询 `/var/apps/trim.music/var/db/music.db`，待 schema 就绪且 `app_state` 为空时，才导入 `init_data.sql` 播种免登录数据（admin/oauth 用户、`initialized` 状态），实现跳过官方初始化流程直接进主页；
4. `trim-music` 只监听 unix socket `/var/run/trim_music.socket`，启动器内置一个 HTTP 反向代理监听 `WEB_PORT`（默认 8007）转发到该 socket，并带上 `X-Real-IP` / `X-Forwarded-For`（等价于原移植方案里的 caddy 配置，省掉 caddy）。

访问 `http://<host>:8007`。**前提：rpcbroker 已运行**。

### 推荐启动顺序

```bash
export MEDIA_DIR=/vol1/1000          # 你的实际媒体目录

rpcbroker.arm64 &                    # 1. 系统服务伪造层
mediasrv.arm64 &                     # 2. 转码服务
fntv.arm64 &                         # 3. 影视（:8005）
fnmusic.arm64 &                      # 4. 音乐（:8007），与 3 无先后依赖
```

## 端口与 socket 一览

| 服务 | 监听地址 | 用途 |
|---|---|---|
| fntv (trim-media) | TCP `:8005`（`WEB_PORT`） | 影视 Web UI / API |
| fnmusic (启动器) | TCP `:8007`（`WEB_PORT`） | HTTP → trim_music.socket 反代 |
| fnmusic (trim-music) | unix `/var/run/trim_music.socket` | 音乐服务本体 |
| mediasrv | unix `/var/run/mediasrv.socket` | 转码服务，供 trim-media 调用 |
| rpcbroker | unix `/run/trim_app_cgi/rpcbroker` | CPRT RPC，伪造飞牛系统服务 |
| rpcbroker | unix `/run/com.trim.app.center.sock` | 应用中心 HTTP（auth-path） |

## 常见问题

- **trim-media / trim-music 启动后功能异常、拿不到目录**：确认 rpcbroker 已先启动，且 `MEDIA_DIR` 在其启动前就设置正确（它负责枚举授权目录）。
- **mediasrv 启动即退出（退出码 1）**：目标机有 `/dev/dri` 但无 PCI 枚举能力，确认 `LD_PRELOAD=nodri.so` 生效（走启动器 `mediasrv.arm64` 会自动设置）。
- **trim-music panic：`unable to open database file`（歌词分片库缺失）**：多为手工提前建了 `music.db` 导致升级流程被跳过；删除库文件让 trim-music 重新自建 schema，由启动器完成播种。
- **install.sh 下载超时**：设置 `PROXY_PREFIX=https://<镜像站>/` 后重试。
- **WebUI 首次进入要求初始化**：确认对应应用的播种 SQL 已执行成功（日志中有 `database init success`）。

## 许可与致谢

- 本项目中的启动器代码（`apps/*/` 下的 `*.go`、Makefile、`nodri.c`、打包/安装脚本）为本仓库编写。
- **fntv 及其他应用的运行时核心不属于本项目**：`trim-media`、`trim-music`、`mediasrv` 预编译二进制、`init.sql` / `trim.media.tar.gz` / `trim.music.tar.gz` / `trim-media-lib.zip` 等均来自网络，其原始权利归属于飞牛（fnOS）及该项目作者。本仓库仅做移植、包装与启动方式研究，不代表对核心代码的任何原创声明。
