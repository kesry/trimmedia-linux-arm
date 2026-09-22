package main

// 移动端高级 API 门面（/mobile/api/v1）
//
// 背景：trim-music 原生接口是给自家 Web 前端用的，移动端直接适配背负了太多历史包袱：
//   - 鉴权只认 Cookie: music-token（浏览器跨域被禁、Capacitor 原生 GET 丢 Cookie 头，
//     被迫用 document.cookie / CapacitorHttp 补丁绕行）；
//   - 无任何 CORS，dev 阶段只能靠 vite 自定义代理注入 x-trim-target/_target；
//   - 参数名不统一（trackGUID / coverId / q / page+size）、媒体 URL 无法直接给 <img> 用。
//
// 本文件在 socket 反代之上包一层移动端专用 API：
//   - token 走请求头 X-Music-Token 或查询参数 t，由本层翻译成上游要求的 Cookie；
//   - 统一 CORS，浏览器（dev 模式）可直接跨域访问本端口，不再需要代理注入 target；
//   - 统一响应 {code,msg,data}，列表统一 data={list,total,page,size}；
//   - 登录改为提交明文密码（本层做 sha256），并聚合 temp-token 一次性返回；
//   - 封面/流媒体 URL 支持 ?t=<token> 直连，<img>/<audio> 可原生使用。
//
// 原 /music/* 的 1:1 透传仍然保留（见 proxy.go），老客户端零影响。
// 接口清单与契约见同目录 MOBILE-API.md。

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// mobileAPIPrefix 挂载前缀；完整基础路径为 /mobile/api/v1。
const mobileAPIPrefix = "/mobile"

const (
	mobileTokenHeader = "X-Music-Token" // 客户端传 token 的请求头
	mobileTokenQuery  = "t"             // 客户端传 token 的查询参数（<img>/<audio> 用）
)

// upstreamResp trim-music 的统一响应封装 {code,msg,data}。
type upstreamResp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// listEnvelope 列表接口的统一分页信封：data = {list,total,page,size}。
type listEnvelope struct {
	List  json.RawMessage `json:"list"`
	Total int64           `json:"total"`
	Page  int64           `json:"page"`
	Size  int64           `json:"size"`
}

// route 声明式路由：把移动端简化参数映射为 trim-music 的真实 query/body。
//
//	QueryMap / BodyMap 形如 {"mobile名": "upstream名"}；
//	值为 ""（空串）表示该映射被注释掉时可忽略；未列出的移动端参数原样透传。
type route struct {
	path     string            // 上游路径，如 /music/api/v1/album/list
	method   string            // 上游方法
	methods  []string          // 可选：允许多种入站方法（如 GET+PUT）；非空时以 req.Method 透传上游，忽略 method
	queryMap map[string]string // 移动端 query 名 -> 上游 query 名
	bodyMap  map[string]string // 移动端 body 键 -> 上游 body 键
	list     bool              // true：把上游 data 重包装成 {list,total,page,size}
}

// mobileRoutes 移动端接口总表（整理自 fnmusic-app/src/api/index.js 的全部现存调用）。
var mobileRoutes = map[string]route{
	// —— 账号 ——
	"/logout": {path: "/music/api/v1/user/logout", method: "POST"},
	// 改密上游契约为 body { password: sha256hex(新密码) }，无需原密码；本层原样透传（哈希由客户端完成）
	"/password-change": {path: "/music/api/v1/user/passwd-change", method: "POST"},

	// —— 音乐库 ——
	"/libraries":        {path: "/music/api/v1/shared-library/list", method: "GET"},
	"/library":          {path: "/music/api/v1/shared-library/detail", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/library/create":   {path: "/music/api/v1/shared-library/create", method: "POST"},
	"/library/edit":     {path: "/music/api/v1/shared-library/edit", method: "POST"},
	"/library/delete":   {path: "/music/api/v1/shared-library/delete", method: "POST"},
	"/library/scan":     {path: "/music/api/v1/shared-library/scan", method: "POST"},
	"/library/scan-all": {path: "/music/api/v1/shared-library/scan-all", method: "POST"},
	"/dirs":             {path: "/music/api/v1/app-center/authed-dir/list", method: "GET"},
	"/dirs/sub":         {path: "/music/api/v1/app-center/authed-dir/sub/list", method: "GET", queryMap: map[string]string{"parent": "parent"}},

	// —— 曲目 ——
	"/tracks": {
		path: "/music/api/v1/track/list", method: "GET",
		queryMap: map[string]string{"library": "sharedLibraryGuid", "limit": "size"},
		list:     true,
	},
	"/tracks/search": {
		path: "/music/api/v1/search/track", method: "GET",
		queryMap: map[string]string{"limit": "size"},
		list:     true,
	},
	"/tracks/by-album": {
		path: "/music/api/v1/track/album-detail/list", method: "GET", list: true,
	},
	"/tracks/by-artist": {
		path: "/music/api/v1/track/artist-detail/list", method: "GET", list: true,
	},
	"/tracks/by-genre": {
		path: "/music/api/v1/track/genre-detail/list", method: "GET", list: true,
	},
	"/tracks/by-playlist": {
		path: "/music/api/v1/track/playlist-detail/list", method: "GET", list: true,
	},
	"/track/info":  {path: "/music/api/v1/track/audio-info", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/track/lyric": {path: "/music/api/v1/track/lyrics", method: "GET", queryMap: map[string]string{"guid": "guid"}},

	// —— 专辑 / 艺人 / 风格 ——
	"/albums":         {path: "/music/api/v1/album/list", method: "GET", queryMap: map[string]string{"library": "sharedLibraryGuid"}, list: true},
	"/album":          {path: "/music/api/v1/album/detail", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/artists":        {path: "/music/api/v1/artist/list", method: "GET", queryMap: map[string]string{"library": "sharedLibraryGuid"}, list: true},
	"/artist":         {path: "/music/api/v1/artist/detail", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/genres":         {path: "/music/api/v1/genre/list", method: "GET", queryMap: map[string]string{"library": "sharedLibraryGuid"}, list: true},
	"/genre":          {path: "/music/api/v1/genre/detail", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/artists/search": {path: "/music/api/v1/search/artist", method: "GET", queryMap: map[string]string{"limit": "size"}, list: true},
	"/albums/search":  {path: "/music/api/v1/search/album", method: "GET", queryMap: map[string]string{"limit": "size"}, list: true},

	// —— 歌单 ——
	"/playlists": {
		path: "/music/api/v1/playlist/list", method: "GET",
		queryMap: map[string]string{"limit": "size"}, list: true,
	},
	"/playlist":               {path: "/music/api/v1/playlist/detail", method: "GET", queryMap: map[string]string{"guid": "guid"}},
	"/playlist/create":        {path: "/music/api/v1/playlist/create", method: "POST"},
	"/playlist/edit":          {path: "/music/api/v1/playlist/edit", method: "POST"},
	"/playlist/delete":        {path: "/music/api/v1/playlist/delete", method: "POST"},
	"/playlist/add-tracks":    {path: "/music/api/v1/playlist/add-track", method: "POST"},
	"/playlist/remove-tracks": {path: "/music/api/v1/playlist/remove-track", method: "POST"},
	"/playlists/search":       {path: "/music/api/v1/search/playlist", method: "GET", queryMap: map[string]string{"limit": "size"}, list: true},

	// —— 收藏 / 历史 ——
	"/favorites": {
		path: "/music/api/v1/favorite-track/list", method: "GET",
		queryMap: map[string]string{"limit": "size"}, list: true,
	},
	"/favorite/add":    {path: "/music/api/v1/favorite-track/create", method: "POST"},
	"/favorite/remove": {path: "/music/api/v1/favorite-track/delete", method: "POST"},
	"/history": {
		path: "/music/api/v1/play-history/list", method: "GET",
		queryMap: map[string]string{"limit": "size"}, list: true,
	},
	"/history/delete": {path: "/music/api/v1/play-history/delete", method: "POST"},

	// —— 歌词（备用入口，trackGUID 大小写统一收掉）——
	"/lyrics": {path: "/music/api/v1/lyric/list", method: "GET", queryMap: map[string]string{"guid": "trackGUID"}},

	// —— 事件上报 ——
	"/events": {path: "/music/api/v1/event/report", method: "POST"},

	// —— 用户管理（admin）——
	"/users":         {path: "/music/api/v1/user/list", method: "GET", queryMap: map[string]string{"limit": "size"}, list: true},
	"/user/exists":   {path: "/music/api/v1/user/exists", method: "GET", queryMap: map[string]string{"name": "name"}},
	"/user/create":   {path: "/music/api/v1/user/create", method: "POST"},
	"/user/edit":     {path: "/music/api/v1/user/edit", method: "POST"},
	"/user/delete":   {path: "/music/api/v1/user/delete", method: "POST"},
	"/user/unbanned": {path: "/music/api/v1/user/unbanned", method: "POST"},

	// —— 任务中心 ——
	"/tasks":       {path: "/music/api/v1/task/list", method: "GET", list: true},
	"/task/retry":  {path: "/music/api/v1/task/retry", method: "POST"},
	"/task/cancel": {path: "/music/api/v1/task/cancel", method: "POST"},
	"/task/delete": {path: "/music/api/v1/task/delete", method: "POST"},

	// —— 系统信息 ——
	"/sys/info": {path: "/music/api/v1/sys/info", method: "GET"},

	// —— 设置（读 + 写：GET 取、PUT 存，同一移动端点按入站方法透传）——
	"/settings/user":   {path: "/music/api/v1/settings/user", methods: []string{"GET", "PUT"}},
	"/settings/server": {path: "/music/api/v1/settings/server", methods: []string{"GET", "PUT"}},
}

// mobileAPIHandler 组装中间件（CORS + token 注入）与路由。
func mobileAPIHandler(tr *http.Transport) http.Handler {
	mux := http.NewServeMux()
	base := mobileAPIPrefix + "/api/v1"

	mux.HandleFunc(base+"/login", func(w http.ResponseWriter, r *http.Request) {
		handleMobileLogin(w, r, tr)
	})
	mux.HandleFunc(base+"/boot", func(w http.ResponseWriter, r *http.Request) {
		handleMobileBoot(w, r, tr)
	})

	// 媒体：封面 / 播放流 / 歌单封面，token 可用 ?t= 直连
	mux.HandleFunc(base+"/media/cover", mediaProxy(tr, "/music/api/v1/static/cover", "coverId"))
	mux.HandleFunc(base+"/media/cover/track", mediaProxy(tr, "/music/api/v1/static/cover/track", "guid"))
	mux.HandleFunc(base+"/media/cover/playlist", mediaProxy(tr, "/music/api/v1/static/cover/playlist", "guid"))
	mux.HandleFunc(base+"/media/stream", mediaProxy(tr, "/music/api/v1/track/stream", "guid"))
	// 歌单封面上传：multipart 仅字段 file，返回 { coverId }（再随 /playlist/create|edit 提交才生效）
	mux.HandleFunc(base+"/media/cover/playlist/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 POST", nil)
			return
		}
		proxyRaw(w, r, tr, "/music/api/v1/static/cover/playlist", url.Values{})
	})
	// 歌单预置封面：/media/preset-cover/playlist/{n}.png
	mux.Handle(base+"/media/preset-cover/playlist/", presetCoverHandler(tr))

	// 声明式透传路由
	for mobilePath, r := range mobileRoutes {
		route := r // 闭包捕获副本
		mux.HandleFunc(base+mobilePath, func(w http.ResponseWriter, req *http.Request) {
			eff := route.method
			if len(route.methods) > 0 {
				// 多方法路由：命中允许集合则以入站方法透传上游
				allowed := false
				for _, m := range route.methods {
					if req.Method == m {
						allowed = true
						break
					}
				}
				if !allowed {
					writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 "+strings.Join(route.methods, "/"), nil)
					return
				}
				eff = req.Method
			} else if req.Method != http.MethodOptions && req.Method != route.method {
				writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 "+route.method, nil)
				return
			}
			rr := route
			rr.method = eff
			passThrough(w, req, tr, rr)
		})
	}

	return withCORS(withToken(mux))
}

// withCORS 统一跨域：trim-music 没有任何 CORS 支持，本层放开后
// 浏览器 dev / 任意 H5 宿主可以直接跨域调用，不再依赖构建侧代理注入目标地址。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions { // 预放行，简化客户端可以直接带 X-Music-Token
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, "+mobileTokenHeader)
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withToken 把 X-Music-Token 头 / ?t= 查询参数翻译成上游要求的 Cookie: music-token=。
func withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get(mobileTokenHeader))
		if token == "" {
			token = r.URL.Query().Get(mobileTokenQuery)
		}
		if token != "" {
			r.Header.Set("Cookie", "music-token="+url.QueryEscape(token))
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 聚合接口 ----------

// handleMobileLogin POST /login {username, password(明文), deviceId?}
// 本层完成 sha256（客户端不再需要 crypto-js），并顺手调 temp-token 校验，
// 一次性返回 {token, user, tempToken}，客户端拿 token 走后续所有接口。
func handleMobileLogin(w http.ResponseWriter, r *http.Request, tr *http.Transport) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		DeviceID string `json:"deviceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeMobile(w, http.StatusBadRequest, 400, "请求体不是合法 JSON", nil)
		return
	}
	if body.Username == "" || body.Password == "" {
		writeMobile(w, http.StatusBadRequest, 100002, "username/password 必填", nil)
		return
	}
	if body.DeviceID == "" {
		body.DeviceID = newDeviceID()
	}
	sum := sha256.Sum256([]byte(body.Password))

	// 1) 密码登录（密码需 sha256 hex，与 trim-music 契约一致）
	code, msg, data, err := doUpstream(r, tr, "POST", "/music/api/v1/user/password-login",
		url.Values{}, jsonBody(map[string]any{
			"username": body.Username,
			"password": hex.EncodeToString(sum[:]),
			"deviceId": body.DeviceID,
		}))
	if err != nil {
		writeMobile(w, http.StatusBadGateway, 502, "无法连接音乐服务: "+err.Error(), nil)
		return
	}
	if code != 0 {
		writeMobile(w, http.StatusOK, code, msg, nil)
		return
	}
	var payload struct {
		UserToken string          `json:"userToken"`
		User      json.RawMessage `json:"user"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.UserToken == "" {
		writeMobile(w, http.StatusBadGateway, 502, "上游登录响应异常", nil)
		return
	}
	// 2) 顺手取 temp-token（原生媒体直连 <img> 时的兜底凭据；失败不阻塞登录）
	tempToken := ""
	if c, _, td, terr := doUpstream(r, tr, "POST", "/music/api/v1/user/temp-token", url.Values{}, nil); terr == nil && c == 0 {
		var tp struct {
			TempToken string `json:"tempToken"`
		}
		if json.Unmarshal(td, &tp) == nil {
			tempToken = tp.TempToken
		}
	}
	writeMobile(w, http.StatusOK, 0, "", map[string]any{
		"token":     payload.UserToken,
		"user":      payload.User,
		"deviceId":  body.DeviceID,
		"tempToken": tempToken,
	})
}

// handleMobileBoot GET /boot：串行聚合初始化所需的全部读取接口，
// 移动端冷启动一次请求拿齐（代替 me + init/state + sys/config 三连）。
func handleMobileBoot(w http.ResponseWriter, r *http.Request, tr *http.Transport) {
	out := map[string]any{}
	fetch := func(key, method, path string) {
		code, msg, data, err := doUpstream(r, tr, method, path, url.Values{}, nil)
		if err != nil || code != 0 {
			if msg == "" && err != nil {
				msg = err.Error()
			}
			out[key+"_error"] = msg
			return
		}
		out[key] = json.RawMessage(data)
	}
	fetch("user", "GET", "/music/api/v1/user/me")
	fetch("initState", "GET", "/music/api/v1/initialization/state")
	fetch("sysConfig", "GET", "/music/api/v1/sys/config")
	writeMobile(w, http.StatusOK, 0, "", out)
}

// mediaProxy 生成媒体代理 handler：透传到上游静态/流接口。
// 鉴权已由 withToken 统一处理（头或 ?t= 均可），<img>/<audio> 可直接用本层 URL。
// param 非空时做参数改名（移动端统一 guid，上游个别接口叫 coverId）。
func mediaProxy(tr *http.Transport, upstream, param string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if param != "" {
			if v := q.Get("guid"); v != "" && param != "guid" {
				q.Set(param, v)
				q.Del("guid")
			}
		}
		proxyRaw(w, r, tr, upstream, q)
	}
}

// presetCoverHandler 歌单预置封面：GET /media/preset-cover/playlist/{n}.png
// 上游是静态资源 /music/static/assets/img/playlist-covers/{n}.png（n=1..4）。
func presetCoverHandler(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, mobileAPIPrefix+"/api/v1/media/preset-cover/playlist/")
		if !regexpPresetCover(name) {
			writeMobile(w, http.StatusBadRequest, 400, "封面名应为 1-4.png", nil)
			return
		}
		proxyRaw(w, r, tr, "/music/static/assets/img/playlist-covers/"+name, url.Values{})
	}
}

func regexpPresetCover(s string) bool {
	switch s {
	case "1.png", "2.png", "3.png", "4.png":
		return true
	}
	return false
}

// ---------- 声明式透传 ----------

// passThrough 按 route 表转发：改名参数、透传 body、解包并（可选）重塑列表信封。
func passThrough(w http.ResponseWriter, r *http.Request, tr *http.Transport, route route) {
	q := url.Values{}
	for mv, uv := range route.queryMap {
		if v := r.URL.Query().Get(mv); v != "" {
			q.Set(uv, v)
		} else {
			q.Del(uv)
		}
	}
	// 未在映射表里的参数原样透传（page/size/guid/keyword 等）。
	// 注意：mapped 只登记「移动端名」(mv)，这样客户端直接传上游名（如 size）时也能原样透传；
	// 若把上游名(uv)也登记进去，会把客户端发的 size 误判为已处理而丢弃、退化成默认分页。
	mapped := make(map[string]bool, len(route.queryMap))
	for mv := range route.queryMap {
		mapped[mv] = true
	}
	for k, vs := range r.URL.Query() {
		if !mapped[k] {
			q[k] = vs
		}
	}
	// 兼容 limit -> size 已在 map 里；再兜底默认分页
	if route.list {
		if q.Get("page") == "" {
			q.Set("page", "1")
		}
		if q.Get("size") == "" {
			q.Set("size", "50")
		}
	}

	var body []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			writeMobile(w, http.StatusBadRequest, 400, "读取请求体失败", nil)
			return
		}
		body = renameJSONKeys(raw, route.bodyMap)
	}

	code, msg, data, err := doUpstream(r, tr, route.method, route.path, q, body)
	if err != nil {
		writeMobile(w, http.StatusBadGateway, 502, "无法连接音乐服务: "+err.Error(), nil)
		return
	}
	if code != 0 {
		writeMobile(w, http.StatusOK, code, msg, nil)
		return
	}
	if route.list {
		data = reshapeList(data, q)
	}
	writeMobile(w, http.StatusOK, 0, "", json.RawMessage(data))
}

// reshapeList 把上游 {list,total[,sort]} 重塑为统一分页信封 {list,total,page,size}。
func reshapeList(data json.RawMessage, q url.Values) json.RawMessage {
	var payload struct {
		List  json.RawMessage `json:"list"`
		Total int64           `json:"total"`
		Sort  json.RawMessage `json:"sort,omitempty"`
	}
	out := listEnvelope{List: json.RawMessage("[]")}
	if len(data) == 0 || json.Unmarshal(data, &payload) != nil {
		// data 本身就是数组的情况（个别接口）
		var arr json.RawMessage
		if json.Unmarshal(data, &arr) == nil {
			out.List = arr
		}
	} else {
		out.List, out.Total = payload.List, payload.Total
		if len(out.List) == 0 {
			out.List = json.RawMessage("[]")
		}
	}
	out.Page, _ = strconv.ParseInt(q.Get("page"), 10, 64)
	out.Size, _ = strconv.ParseInt(q.Get("size"), 10, 64)
	b, _ := json.Marshal(out)
	return b
}

// renameJSONKeys 按 map 重命名 JSON 顶层键（无 map 或非法 JSON 时原样返回）。
func renameJSONKeys(raw []byte, map_ map[string]string) []byte {
	if len(map_) == 0 || len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	changed := false
	for mv, uv := range map_ {
		if v, ok := obj[mv]; ok && mv != uv {
			obj[uv] = v
			delete(obj, mv)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// ---------- 上游访问 ----------

// doUpstream 调 trim-music 并解析 {code,msg,data}。
func doUpstream(r *http.Request, tr *http.Transport, method, path string, q url.Values, body []byte) (code int, msg string, data json.RawMessage, err error) {
	req, err := http.NewRequestWithContext(r.Context(), method, "http://trim.music"+path, nil)
	if err != nil {
		return
	}
	req.URL.RawQuery = q.Encode()
	copyAuthHeaders(r, req)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return
	}
	var ur upstreamResp
	if err = json.Unmarshal(raw, &ur); err != nil {
		return 0, "", nil, err
	}
	return ur.Code, ur.Msg, ur.Data, nil
}

// proxyRaw 原样转发（媒体/流/二进制），保留 Range、Content-Type 等，支持 206。
func proxyRaw(w http.ResponseWriter, r *http.Request, tr *http.Transport, path string, q url.Values) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://trim.music"+path, r.Body)
	if err != nil {
		writeMobile(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	req.URL.RawQuery = q.Encode()
	copyAuthHeaders(r, req)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		writeMobile(w, http.StatusBadGateway, 502, "无法连接音乐服务", nil)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyAuthHeaders 构造发往上游的请求头：Cookie 已由 withToken 设进 r.Header，这里透传。
func copyAuthHeaders(r *http.Request, up *http.Request) {
	if c := r.Header.Get("Cookie"); c != "" {
		up.Header.Set("Cookie", c)
	}
	if t := r.Header.Get("X-Real-IP"); t != "" {
		up.Header.Set("X-Real-IP", t)
	}
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		up.Header.Set("X-Forwarded-For", f)
	}
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		up.Header.Set("Content-Type", ct) // 透传 multipart 边界等
	}
	if rm := r.Header.Get("Range"); rm != "" {
		up.Header.Set("Range", rm)
	}
	up.Header.Set("User-Agent", "fnmusic-mobile/1.0 "+r.Header.Get("User-Agent"))
}

// ---------- 工具 ----------

// writeMobile 统一响应封装：HTTP 状态与业务 code 分离，移动端只需判 code==0。
func writeMobile(w http.ResponseWriter, status, code int, msg string, data any) {
	if status < 200 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
}

// jsonBody 序列化任意结构为请求体。
func jsonBody(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("mobile api: marshal body failed: %v", err)
		return nil
	}
	return b
}

// newDeviceID 生成 32 位 hex 设备 id（客户端未提供时兜底）。
func newDeviceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
