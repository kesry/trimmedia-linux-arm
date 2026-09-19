# trimmedia-linux

将飞牛 OS（fnOS）的影视应用 `trim.media` 及其闭源后端 `mediasrv` 移植到通用 Linux（arm64）环境运行的项目。通过一个 `rpcbroker` 中间层，模拟飞牛系统的 RPC / 应用中心服务，使原本依赖飞牛平台的影视服务可以在独立的 Debian / Ubuntu 等系统上启动并提供 Web 影视库能力。

## 功能特性

- 媒体库刮削：扫描媒体目录并提取元数据（TMDB / IMDb）。
- 在线播放：服务端提供直链 URL，客户端负责解码播放（默认关闭服务端转码，片源优先使用 H.264 + AAC 以保证兼容性）。
- 通用 Linux 部署：不依赖飞牛 OS，仅需一行脚本即可完成依赖安装。

## 项目结构

```
trimmedia-linux/
├── install.sh              # 依赖自动检测与安装脚本
├── package.sh              # 打包脚本（构建 arm64 发布包）
├── init.sql                # SQLite 数据库初始化脚本
└── lib/rpcbroker/          # rpcbroker 中间层（Go）
    ├── main.go             # 程序入口：启动 mediasrv、rpcbroker、trim-media
    ├── server/server.go    # 模拟飞牛 RPC broker / 应用中心服务
    └── build.sh            # rpcbroker 编译脚本
```

## 组件说明

| 组件 | 说明 |
| --- | --- |
| `rpcbroker`（`fntv.arm64`） | 中间层，负责启动并协调整个服务，模拟飞牛的 RPC 与应用中心接口 |
| `mediasrv` | 飞牛 OS arm64 提取的闭源后端，自带 FFmpeg 7 运行库（位于 `lib/mediasrv`），不依赖系统 apt 安装的 ffmpeg |
| `trim-media` | 影视应用主程序，提供 Web 服务与前端页面 |

## 环境要求

- **架构**：arm64（aarch64）
- **系统**：Debian 13 推荐；其他 Debian / Ubuntu 版本可尝试（会自动回退使用扩展依赖包）
- **权限**：root 或可用 sudo

## 安装说明

### 1. 安装系统依赖

在项目根目录执行安装脚本，脚本会自动检测发行版并安装所需的运行库、`sqlite3` 等依赖，同时下载 `trim-media` 运行库与主程序包：

```bash
sudo bash install.sh
```
> 可以通过设置环境变量`PROXY_PREFIX`来加速下载

> 说明：Debian 13 会直接安装系统自带的 FFmpeg / 编解码相关库；其余版本若缺少对应包，脚本会额外下载 `lib.extends.zip` 作为补充依赖。

### 2. 目录结构

安装/解压后，可执行文件所在目录（`BINPATH`）需包含以下结构，`rpcbroker` 会据此自动定位资源：

```
BINPATH/
├── fntv.arm64              # rpcbroker 入口程序
├── init.sql                # 数据库初始化脚本
├── bin/mediasrv            # 闭源后端
├── lib/                    # mediasrv 及其自带运行库
├── extends/                # 扩展依赖库（非 Debian 13 时）
├── trim.media/trim-media   # 影视主程序与前端静态资源
└── data/                   # 运行时生成的数据库与媒体数据
```

### 3. 配置媒体目录

媒体目录**无需手动配置**，由程序自动管理，默认指向：

```
BINPATH/data/media
```

（`BINPATH` 即可执行文件 `fntv.arm64` 所在目录，程序启动时会自动解析。）

使用时只需在媒体目录下**创建子目录并把影视文件放进去**，例如：

```
BINPATH/data/media/
├── movies/      # 电影
├── tv/          # 剧集
└── anime/       # 动漫
```

> 媒体目录下的每个一级子目录都会被自动识别为一个可授权路径，飞牛影视即可对其刮削与播放。首次启动时若目录为空，程序会自动创建一个 `default` 子目录。

### 4. 启动服务

```bash
./fntv.arm64
```

启动后 `rpcbroker` 会依次拉起 `mediasrv`、模拟 RPC 服务，并在首次运行时自动初始化 SQLite 数据库（`data/database/trimmedia.db`），随后启动 `trim-media` Web 服务。

### 5. 访问与登录

浏览器打开：

```
http://<服务器IP>:8005
```

**默认登录账号：**

| 用户名 | 密码 |
| --- | --- |
| `admin` | `123456` |

> 建议登录后及时修改默认密码。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WEB_PORT` | `8005` | Web 服务监听端口 |
| `LOG_LEVEL` | `info` | 日志级别 |
| `LD_LIBRARY_PATH` | 自动拼接 | 运行库搜索路径，一般无需手动设置 |

> 媒体目录（`MEDIA_DIR`）由程序自动管理，默认指向 `BINPATH/data/media`，**无需用户设置**，只需将影视文件按子目录放入该路径即可（详见[配置媒体目录](#3-配置媒体目录)）。

示例（仅需指定端口）：

```bash
WEB_PORT=9000 ./fntv.arm64
```

## 从源码构建

编译 `rpcbroker` 中间层并生成发布包（默认 target 为 `linux/arm64`）：

```bash
# 编译 rpcbroker
sh lib/rpcbroker/build.sh

# 打包发布（输出到 out/fntv.linux.arm64.tar.gz）
sh package.sh
```

> 构建依赖 Go 工具链；若安装了 `upx`，编译脚本会自动对产物进行压缩。

## 常见问题

- **mediasrv 启动即退出**：确认 `/usr/trim/etc` 目录存在（入口程序会自动创建），并核对设备树 / SoC 是否匹配，非瑞芯微平台可能无法硬解，需要依赖软解回退。
- **缺少 `libavformat.so.61` 等库**：Debian 13 使用系统自带库；其他版本请确保已下载扩展依赖 `lib.extends.zip` 并放置到 `extends/` 目录。
- **无法登录或数据异常**：可删除 `data/database/trimmedia.db` 后重启，服务会根据 `init.sql` 重新初始化。
