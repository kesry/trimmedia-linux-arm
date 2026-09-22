# trim-music 移动端 API（`/mobile/api/v1`）

> 本文件是 **fnmusic** 为移动端（Capacitor / H5）专门封装的高级接口契约。
> 实现见同目录 [`mobile_api.go`](./mobile_api.go)，挂载点见 [`proxy.go`](./proxy.go)。
>
> 设计动机：`trim-music` 原生接口是给自家 React Web 用的，移动端直接适配历史包袱重：
> - 鉴权只认 `Cookie: music-token`，浏览器禁止 JS 手动设 `Cookie` 头、Capacitor 原生 GET 又丢 `Cookie` 头，被迫 `document.cookie` + `CapacitorHttp` 双通道绕行；
> - **无任何 CORS**，dev 只能靠 vite 自定义代理注入 `x-trim-target`；
> - 参数名不统一（`trackGUID` / `coverId` / `q` / `page`+`size`），列表/分页信封不一致；
> - 媒体 `<img>/<audio>` 无法携带鉴权头。
>
> 本层把上述全部收敛：**token 走头/查询参数、统一 CORS、统一 `{code,msg,data}`、列表统一分页信封、媒体 URL 可 `?t=` 直连、登录提交明文密码由服务端做 sha256**。

---

## 0. 通用约定

| 项 | 说明 |
|---|---|
| Base URL | `http://<NAS_IP>:<WEB_PORT>/mobile/api/v1`（`WEB_PORT` 默认 `8007`，见 `fnmusic.go`） |
| 上游 | 本层全部转发到本机 unix socket `/var/run/trim_music.socket` 上的 `trim-music` |
| 鉴权 | 任选其一：请求头 **`X-Music-Token: <token>`**，或查询参数 **`?t=<token>`**（媒体 `<img>/<audio>` 用）。本层翻译成上游要求的 `Cookie: music-token=<token>` |
| 响应封装 | 统一 `{ code, msg, data }`；**`code === 0` 成功**。业务失败也回 `HTTP 200` 且带非 0 `code`；仅上游不可达时回 `HTTP 502 code=502` |
| CORS | 全开：回显 `Origin`、`Allow-Credentials: true`、放行 `Content-Type`/`X-Music-Token`，`OPTIONS` 预检直接 `204`。浏览器可跨域直连，**不再需要构建侧代理注入 target** |
| 列表信封 | 标「列表」的接口 `data = { list, total, page, size }`；分页参数统一 `page`（1 基）/`size`，缺省 `page=1`、`size=50` |

**业务错误码（透传上游）**：`0` 成功、`99999`/`120001` 鉴权失效、`100002` 参数错误、`100003` 需管理员。

---

## 1. 账号

### `POST /login` — 密码登录（聚合）
替代原生 `password-login` + `temp-token` 两连。客户端**提交明文密码**，本层做 `sha256hex` 再转上游，客户端无需再引 `crypto-js`。

请求体：
```json
{ "username": "admin", "password": "明文密码", "deviceId": "可选，缺省服务端生成32位hex" }
```
`data`：
```json
{ "token": "<userToken>", "user": { }, "deviceId": "<回显或生成>", "tempToken": "<可能为空>" }
```
> 后续所有接口用返回的 `token` 作为 `X-Music-Token` / `?t=`。

### `GET /boot` — 冷启动聚合
一次请求拿齐初始化数据，代替 `user/me` + `initialization/state` + `sys/config` 三连。
`data`：`{ "user": {}, "initState": {}, "sysConfig": {} }`；某项失败时以 `"<key>_error": "原因"` 降级，不整体报错。

### `POST /logout` → 上游 `user/logout`
### `POST /password-change` → 上游 `user/passwd-change`
body `{ password: sha256hex(新密码) }`（**无需原密码**，已实测确认；本层原样透传，哈希由客户端完成）。

---

## 2. 音乐库

| 移动端 | 方法 | 上游 | 备注 |
|---|---|---|---|
| `/libraries` | GET | `shared-library/list` | 库列表 |
| `/library?guid=` | GET | `shared-library/detail` | 库详情 |
| `/library/create` | POST | `shared-library/create` | `{ path, metadataPreference, autoDownloadLyric }` |
| `/library/edit` | POST | `shared-library/edit` | `{ guid, path, ... }`（端点是 edit 非 update） |
| `/library/delete` | POST | `shared-library/delete` | `{ guid }` |
| `/library/scan` | POST | `shared-library/scan` | 单库扫描 `{ guid }` |
| `/library/scan-all` | POST | `shared-library/scan-all` | 全库扫描 |
| `/dirs` | GET | `app-center/authed-dir/list` | 已授权顶层目录 |
| `/dirs/sub?parent=` | GET | `app-center/authed-dir/sub/list` | 懒加载子目录 `{ list:[{path,name}] }` |

---

## 3. 曲目

| 移动端 | 方法 | 上游 | 参数 |
|---|---|---|---|
| `/tracks` | GET | `track/list` | 列表信封；`library`→`sharedLibraryGuid`；`page/size`（`limit`→`size`） |
| `/tracks/search` | GET | `search/track` | 列表信封；`q=`（搜索参数名是 q） |
| `/tracks/by-album` | GET | `track/album-detail/list` | 列表信封；按专辑专属 GUID + `page/size` |
| `/tracks/by-artist` | GET | `track/artist-detail/list` | 同上 |
| `/tracks/by-genre` | GET | `track/genre-detail/list` | 同上 |
| `/tracks/by-playlist` | GET | `track/playlist-detail/list` | 同上 |
| `/track/info?guid=` | GET | `track/audio-info` | 音频技术参数 |
| `/track/lyric?guid=` | GET | `track/lyrics` | 歌词备用入口 |

---

## 4. 专辑 / 艺人 / 风格

| 移动端 | 方法 | 上游 | 参数 |
|---|---|---|---|
| `/albums` | GET | `album/list` | 列表信封；`library`→`sharedLibraryGuid` |
| `/album?guid=` | GET | `album/detail` | |
| `/artists` | GET | `artist/list` | 列表信封 |
| `/artist?guid=` | GET | `artist/detail` | |
| `/genres` | GET | `genre/list` | 列表信封 |
| `/genre?guid=` | GET | `genre/detail` | |
| `/albums/search` `/artists/search` | GET | `search/album` `search/artist` | 列表信封；`q=` |

---

## 5. 歌单

| 移动端 | 方法 | 上游 | body / 备注 |
|---|---|---|---|
| `/playlists` | GET | `playlist/list` | 列表信封 |
| `/playlist?guid=` | GET | `playlist/detail` | |
| `/playlist/create` | POST | `playlist/create` | `{ name, coverId? }` |
| `/playlist/edit` | POST | `playlist/edit` | `{ guid, name, coverId? }`（端点 edit） |
| `/playlist/delete` | POST | `playlist/delete` | `{ guid }` |
| `/playlist/add-tracks` | POST | `playlist/add-track` | `{ guid, trackGUIDs:[...] }` |
| `/playlist/remove-tracks` | POST | `playlist/remove-track` | `{ guid, trackGUIDs:[...] }` |
| `/playlists/search` | GET | `search/playlist` | 列表信封；`q=` |

**封面持久化契约**（与旧版一致）：先 `POST /media/cover/playlist/upload` 见 §7，拿 `coverId` 再放进 `create/edit` 的 body 提交。预置随机封面用 §6 的 `/media/preset-cover/playlist/{n}.png`。

---

## 6. 收藏 / 历史 / 歌词 / 事件

| 移动端 | 方法 | 上游 | 参数 |
|---|---|---|---|
| `/favorites` | GET | `favorite-track/list` | 列表信封 |
| `/favorite/add` | POST | `favorite-track/create` | `{ trackGUID }`（以实际为准） |
| `/favorite/remove` | POST | `favorite-track/delete` | 同上 |
| `/history` | GET | `play-history/list` | 列表信封 |
| `/history/delete` | POST | `play-history/delete` | |
| `/lyrics?guid=` | GET | `lyric/list` | `guid`→`trackGUID`；返回 `{ list, preferred }` |
| `/events` | POST | `event/report` | `{ events:[{ eventType, occurredAt, payload }] }`，播放记 `track_play` |

---

## 7. 媒体（图片 / 音频流）

这些 URL 可**直接**填进 `<img src>` / `<audio src>`，鉴权用 `?t=<token>` 即可（本层转成 Cookie 再转上游），无需 blob 绕行、无需 `_target`。

| 移动端 | 上游 | 参数改名 |
|---|---|---|
| `/media/cover?guid=<coverId>` | `static/cover` | `guid`→`coverId` |
| `/media/cover/track?guid=` | `static/cover/track` | |
| `/media/cover/playlist?guid=` | `static/cover/playlist` | |
| `/media/stream?guid=` | `track/stream` | 支持 `Range`/`206`，供 `<audio>` 直连播放 |
| `/media/preset-cover/playlist/{1..4}.png` | `music/static/.../playlist-covers/*.png` | 歌单预置封面 |

示例：
```
GET /mobile/api/v1/media/stream?guid=<trackGuid>&t=<token>
GET /mobile/api/v1/media/cover/track?guid=<trackGuid>&t=<token>
```

---

## 8. 设置 / 系统 / 管理端

| 移动端 | 方法 | 上游 | 备注 |
|---|---|---|---|
| `/settings/user` | GET / PUT | `settings/user` | 同一点位按入站方法透传（GET 读、PUT 存） |
| `/settings/server` | GET / PUT | `settings/server` | 同上 |
| `/sys/info` | GET | `sys/info` | 系统信息 |

### 用户管理（需 admin）

| 移动端 | 方法 | 上游 | body / 参数 |
|---|---|---|---|
| `/users` | GET | `user/list` | 列表信封；`page/size`（`limit`→`size`） |
| `/user/exists?name=` | GET | `user/exists` | 用户名是否存在 |
| `/user/create` | POST | `user/create` | `{ username, password, role, ... }`（password 仍由客户端 sha256hex，本层不改写） |
| `/user/edit` | POST | `user/edit` | `{ guid, ... }` |
| `/user/delete` | POST | `user/delete` | `{ guid }` |
| `/user/unbanned` | POST | `user/unbanned` | `{ guid }` |

### 任务中心（扫描等后台任务）

| 移动端 | 方法 | 上游 | body / 参数 |
|---|---|---|---|
| `/tasks` | GET | `task/list` | 列表信封 |
| `/task/retry` | POST | `task/retry` | `{ taskId }` |
| `/task/cancel` | POST | `task/cancel` | `{ taskId }` |
| `/task/delete` | POST | `task/delete` | `{ taskId }` |

> 登录（§1 `/login`）已在本层做 sha256；但**用户创建/编辑/改密码**（`/user/create|edit`、`/password-change`）本层不改写密码字段，上游仍要求 sha256hex，由客户端自行哈希。

---

## 9. 兼容与回退

- 原 `trim-music` 原生接口 **`/music/*` 仍 1:1 透传保留**（见 `proxy.go` 的 `legacy` 反代），老版 Web / 未迁移客户端零影响。
- 本层是**新增**的并行命名空间，不改动任何上游行为，仅在 fnmusic 进程内加一层转发。

## 10. 移动端接入状态

前端 `fnmusic-app` **已全量切到本层** `/mobile/api/v1`，删除了旧 `/music` + 代理注入的全部包袱：
1. `src/api/http.js`：已删除 `nativeGetAdapter`（Capacitor GET 绕行）与 `x-music-token`/`x-trim-target`/`_target`/`_token`；统一用 `X-Music-Token` 头 + 绝对 Base。
2. `src/api/index.js`：已删除 `withCoverTarget` 与 dev 拼参；媒体统一用 §7 的 `/media/*?t=<token>` 绝对地址，`<img>`/`<audio>` 可直连。
3. `vite.config.js`：已删除 `trimMusicProxy` 中间件（CORS 由本层放开，dev 可直连）。
4. `utils/media.js`：已删除原生 blob 抓取/全局观察者（网关为 HTTPS，无混合内容限制）。

> 网关地址：`https://fnmusic.orgic.dpdns.org:233`（登录页填入的 host 即此网关；`/mobile` 与 legacy `/music` 同端口提供）。
