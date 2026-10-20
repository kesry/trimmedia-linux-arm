package main

// 一起听（房间同步播放）—— 网关侧房间内核
//
// 设计（见 fnmusic-app/docs/06-阶段-一起听.md）：
//   - 服务端为唯一事实源：房间维护单调 version，任一成员的控制动作 POST 给服务端，
//     服务端应用 → version++ → SSE 广播新状态；所有客户端以广播状态收敛本地播放。
//   - 传输：下行 SSE（纯标准库 http.Flusher），上行普通 POST 命令；不引入 WebSocket。
//   - 状态存内存：单实例临时会话，rooms sync.Map + 每房间互斥锁；重启丢房间可接受。
//   - 成员身份由 token 解析（/user/me），客户端不能伪造 userId 抢控制权。
//
// 路径挂在 /mobile/api/v1/room/*，与其余移动 API 共用 withCORS(withToken(mux)) 中间件，
// 因此 SSE 用 ?t=<token>、命令用 X-Music-Token 头均可自动鉴权。

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	roomTTL     = 30 * time.Minute // 房间空/无人活动超时回收
	roomGCFreq  = time.Minute      // 后台巡检周期
	sseBuffer   = 16               // 每成员 SSE 缓冲帧数，满则判定为慢客户端并断开
	ssePingFreq = 15 * time.Second // SSE 心跳注释帧，保活并探测断线
)

// Member 房间成员。evt 是其 SSE 扇出通道（承载已编码好的 SSE 帧字符串）。
type Member struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar,omitempty"`
	Role     string `json:"role"` // host | member
	JoinedAt int64  `json:"joinedAt"`

	evt chan string // 内部：SSE 帧队列
}

// TrackRef 队列条目的轻量元数据：只存 token-free 字段（guid/标题/艺人/coverId/时长），
// 各端用自身 token 现构播放流/封面 URL，避免共享发起者凭据、也不依赖未知上游元数据接口。
type TrackRef struct {
	Guid       string `json:"guid"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	CoverId    string `json:"coverId,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// Room 房间及其权威播放状态。所有字段由 mu 保护。
type Room struct {
	Code   string
	HostID string

	mu      sync.Mutex
	Members map[string]*Member

	// 共享播放状态（服务端权威）
	Queue       []TrackRef // 共享队列：轻量曲目元数据
	Index       int        // 当前曲下标
	Playing     bool
	PositionMs  int64 // 在 UpdatedAtMs 时刻的播放位置
	UpdatedAtMs int64 // 服务器打点（Unix 毫秒）
	Version     uint64
	Closed      bool
}

// rooms 全局房间表：code -> *Room。
var rooms sync.Map

var gcOnce sync.Once

// ---------- 房间/成员基础 ----------

func nowMs() int64 { return time.Now().UnixMilli() }

// genRoomCode 生成 6 位大写字母数字房间码，避重。
func genRoomCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去掉易混字符 I/O/0/1
	for attempt := 0; attempt < 50; attempt++ {
		b := make([]byte, 6)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				b[i] = alphabet[i%len(alphabet)]
				continue
			}
			b[i] = alphabet[n.Int64()]
		}
		code := string(b)
		if _, exists := rooms.Load(code); !exists {
			return code
		}
	}
	return ""
}

// getRoom 取房间（未关闭的）。
func getRoom(code string) (*Room, bool) {
	v, ok := rooms.Load(code)
	if !ok {
		return nil, false
	}
	r := v.(*Room)
	r.mu.Lock()
	closed := r.Closed
	r.mu.Unlock()
	if closed {
		return nil, false
	}
	return r, true
}

// currentPosMs 按锚点推算当前权威播放位置（须持锁调用）。
// now 为“以哪个时刻为准”：房主/成员命令把锚点钉到“请求到达时刻”arrivedMs（见 applyCommand），
// 纯查询/广播则用真实 nowMs()。
func (r *Room) currentPosMs(now int64) int64 {
	if !r.Playing {
		return r.PositionMs
	}
	p := r.PositionMs + (now - r.UpdatedAtMs)
	if p < 0 {
		return 0
	}
	return p
}

// freezeLocked 把派生位置固化为权威值并重置锚点（须持锁调用）。
func (r *Room) freezeLocked(now int64) {
	r.PositionMs = r.currentPosMs(now)
	r.UpdatedAtMs = now
}

// touchLocked 保活并固化派生位置（须持锁调用）。
// 关键：必须先把 PositionMs 推进到当下再重置 UpdatedAtMs 锚点，否则仅前移锚点会让
// currentPosMs = PositionMs + (now - UpdatedAtMs) 丢掉 (now-旧UpdatedAtMs) 这段已播时长，
// 表现为只调 touch 的事件（join/reconnect 的 addMember、leave/kick、queueAdd 等）把全房
// 权威进度“倒带”回上次锚点——房主实际已播更远 → 后进成员与房主对不上（不同步）、
// 播放中踢人整曲重播。与 freezeLocked 同义，故直接复用。
func (r *Room) touchLocked(now int64) { r.freezeLocked(now) }

// snapshotLocked 生成 state 快照（含 serverTimeMs，供各端算漂移）。须持锁调用。
// 传入的 now 同时用于推算 positionMs 与打 serverTimeMs，保证“位置↔服务器时钟”成对一致。
func (r *Room) snapshotLocked(now int64) map[string]any {
	q := r.Queue
	if q == nil {
		q = []TrackRef{}
	}
	return map[string]any{
		"code":         r.Code,
		"hostId":       r.HostID,
		"version":      r.Version,
		"queue":        q,
		"index":        r.Index,
		"playing":      r.Playing,
		"positionMs":   r.currentPosMs(now),
		"serverTimeMs": now,
		"closed":       r.Closed,
	}
}

func (r *Room) membersLocked() []*Member {
	out := make([]*Member, 0, len(r.Members))
	for _, m := range r.Members {
		out = append(out, &Member{ID: m.ID, Name: m.Name, Avatar: m.Avatar, Role: m.Role, JoinedAt: m.JoinedAt})
	}
	return out
}

// ---------- SSE 广播 ----------

// sseFrame 编码一帧 SSE。
func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// broadcastLocked 向房间所有成员广播一帧；缓冲满（慢客户端）则摘除该成员。须持锁调用。
func (r *Room) broadcastLocked(event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := sseFrame(event, string(b))
	for id, m := range r.Members {
		select {
		case m.evt <- frame:
		default:
			// 慢/死客户端：关闭其通道，SSE handler 收到 closed chan 后自行退出并再摘除
			close(m.evt)
			delete(r.Members, id)
		}
	}
}

// broadcastState / broadcastMembers 便捷封装（各自加锁由调用方保证已持锁或此处不持锁）。
func (r *Room) broadcastStateLocked() {
	r.Version++
	r.broadcastLocked("state", r.snapshotLocked(nowMs()))
}
func (r *Room) broadcastMembersLocked() {
	r.broadcastLocked("members", r.membersLocked())
}

// ---------- 成员进出 ----------

func (r *Room) addMember(id, name, avatar, role string) *Member {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.Members[id]; ok {
		// 同 id 重连：先关旧通道让旧 SSE 退出，再替换
		select {
		case <-old.evt:
		default:
		}
		close(old.evt)
	}
	m := &Member{ID: id, Name: name, Avatar: avatar, Role: role, JoinedAt: nowMs(), evt: make(chan string, sseBuffer)}
	r.Members[id] = m
	r.touchLocked(nowMs())
	r.broadcastMembersLocked()
	return m
}

func (r *Room) removeMember(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.Members[id]; ok {
		close(m.evt)
		delete(r.Members, id)
	}
	r.touchLocked(nowMs())
	r.broadcastMembersLocked()
}

// ---------- HTTP 处理 ----------

// registerRoomRoutes 把房间接口挂到 mux（前缀 base，如 /mobile/api/v1/room）。
func registerRoomRoutes(mux *http.ServeMux, base string, tr *http.Transport) {
	gcOnce.Do(func() { go roomJanitor() })
	mux.HandleFunc(base+"/create", handleRoomCreate(tr))
	mux.HandleFunc(base+"/join", handleRoomJoin(tr))
	mux.HandleFunc(base+"/leave", handleRoomLeave(tr))
	mux.HandleFunc(base+"/command", handleRoomCommand(tr))
	mux.HandleFunc(base+"/state", handleRoomState(tr))
	mux.HandleFunc(base+"/events", handleRoomEvents(tr))
}

// identity 由 token 解析当前用户（复用 withToken 注入的 Cookie → 上游 /user/me）。
func identity(r *http.Request, tr *http.Transport) (id, name, avatar, role string, err error) {
	code, msg, data, e := doUpstream(r, tr, "GET", "/music/api/v1/user/me", nil, nil)
	if e != nil {
		return "", "", "", "", e
	}
	if code != 0 {
		return "", "", "", "", fmt.Errorf("upstream user/me: %s", msg)
	}
	var u map[string]any
	if json.Unmarshal(data, &u) != nil {
		return "", "", "", "", fmt.Errorf("user/me 响应非法")
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := u[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
	id = pick("guid", "id", "userId")
	name = pick("name", "username", "nickName")
	avatar = pick("avatar", "avatarUrl", "icon")
	role = pick("role")
	if id == "" {
		return "", "", "", "", fmt.Errorf("user/me 缺少用户标识")
	}
	if role == "admin" {
		role = "host" // 房间语义里只有 host/member；admin 视作可当房主
	}
	return id, name, avatar, role, nil
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// handleRoomCreate POST /room/create {name?} -> {code, state, members}
func handleRoomCreate(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 POST", nil)
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = decodeBody(r, &body) // name 可选
		id, name, avatar, _, err := identity(r, tr)
		if err != nil {
			writeMobile(w, http.StatusUnauthorized, 401, "无法识别用户: "+err.Error(), nil)
			return
		}
		code := genRoomCode()
		if code == "" {
			writeMobile(w, http.StatusInternalServerError, 500, "房间号生成失败", nil)
			return
		}
		room := &Room{Code: code, HostID: id, Members: map[string]*Member{}, Version: 0, UpdatedAtMs: nowMs()}
		rooms.Store(code, room)
		room.addMember(id, name, avatar, "host")
		room.mu.Lock()
		state := room.snapshotLocked(nowMs())
		members := room.membersLocked()
		room.mu.Unlock()
		writeMobile(w, http.StatusOK, 0, "", map[string]any{"code": code, "state": state, "members": members})
	}
}

// handleRoomJoin POST /room/join {code} -> {state, members}
func handleRoomJoin(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 POST", nil)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if decodeBody(r, &body) != nil || strings.TrimSpace(body.Code) == "" {
			writeMobile(w, http.StatusBadRequest, 400, "code 必填", nil)
			return
		}
		id, name, avatar, role, err := identity(r, tr)
		if err != nil {
			writeMobile(w, http.StatusUnauthorized, 401, "无法识别用户: "+err.Error(), nil)
			return
		}
		room, ok := getRoom(strings.ToUpper(strings.TrimSpace(body.Code)))
		if !ok {
			writeMobile(w, http.StatusOK, 404, "房间不存在或已关闭", nil)
			return
		}
		mrole := "member"
		if id == room.HostID {
			mrole = "host"
		} else if role == "host" {
			mrole = "member"
		}
		room.addMember(id, name, avatar, mrole)
		room.mu.Lock()
		state := room.snapshotLocked(nowMs())
		members := room.membersLocked()
		room.mu.Unlock()
		writeMobile(w, http.StatusOK, 0, "", map[string]any{"state": state, "members": members})
	}
}

// handleRoomLeave POST /room/leave {code} -> {}
func handleRoomLeave(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 POST", nil)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		_ = decodeBody(r, &body)
		id, _, _, _, err := identity(r, tr)
		if err == nil {
			if room, ok := getRoom(strings.ToUpper(strings.TrimSpace(body.Code))); ok {
				room.removeMember(id)
			}
		}
		writeMobile(w, http.StatusOK, 0, "", map[string]any{})
	}
}

// handleRoomState GET /room/state?code= -> {state, members}（重连补拉）
func handleRoomState(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		room, ok := getRoom(strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code"))))
		if !ok {
			writeMobile(w, http.StatusOK, 404, "房间不存在或已关闭", nil)
			return
		}
		room.mu.Lock()
		state := room.snapshotLocked(nowMs())
		members := room.membersLocked()
		room.mu.Unlock()
		writeMobile(w, http.StatusOK, 0, "", map[string]any{"state": state, "members": members})
	}
}

// cmdPayload 命令统一入参（按 cmd 取用相应字段）。
type cmdPayload struct {
	Code       string     `json:"code"`
	Cmd        string     `json:"cmd"`
	PositionMs int64      `json:"positionMs"`
	Queue      []TrackRef `json:"queue"`
	Items      []TrackRef `json:"items"`
	Index      int        `json:"index"`
	UserID     string     `json:"userId"`
}

// handleRoomCommand POST /room/command {code, cmd, ...} -> {state}
func handleRoomCommand(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMobile(w, http.StatusMethodNotAllowed, 405, "方法应为 POST", nil)
			return
		}
		// 记录“请求到达时刻”：命令回源鉴权 identity()(同步调上游 /user/me) 可能耗时数百 ms，
		// 进度锚点必须以到达时刻为准打点，否则这段处理时延会被固化进房间时间轴 → 全房比房主慢。
		arrivedMs := nowMs()
		var p cmdPayload
		if decodeBody(r, &p) != nil || strings.TrimSpace(p.Code) == "" || p.Cmd == "" {
			writeMobile(w, http.StatusBadRequest, 400, "code/cmd 必填", nil)
			return
		}
		id, name, avatar, _, err := identity(r, tr)
		if err != nil {
			writeMobile(w, http.StatusUnauthorized, 401, "无法识别用户: "+err.Error(), nil)
			return
		}
		room, ok := getRoom(strings.ToUpper(strings.TrimSpace(p.Code)))
		if !ok {
			writeMobile(w, http.StatusOK, 404, "房间不存在或已关闭", nil)
			return
		}

		// 成员若不在房间（如断线期间被清），自动补登记，避免命令被拒
		room.mu.Lock()
		if _, present := room.Members[id]; !present {
			room.mu.Unlock()
			role := "member"
			if id == room.HostID {
				role = "host"
			}
			room.addMember(id, name, avatar, role)
			room.mu.Lock()
		}
		isHost := id == room.HostID
		room.mu.Unlock()

		state, kickID, closeRoom, err := room.applyCommand(p, isHost, arrivedMs)
		if err != nil {
			writeMobile(w, http.StatusOK, 400, err.Error(), nil)
			return
		}
		if kickID != "" {
			room.kickMember(kickID)
		}
		if closeRoom {
			room.closeRoom()
		}
		writeMobile(w, http.StatusOK, 0, "", map[string]any{"state": state})
	}
}

// applyCommand 串行应用一条命令并广播，返回新 state。返回 (state, kickUserId, closed, err)。
// arrivedMs = 该命令“请求到达服务端的时刻”：所有涉及进度锚点的写入(UpdatedAtMs)都以它为准，
// 而非 identity() 回源鉴权之后更晚的 nowMs()，避免把网关处理时延固化进房间时间轴。
func (r *Room) applyCommand(p cmdPayload, isHost bool, arrivedMs int64) (map[string]any, string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchLocked(arrivedMs)

	nextTrack := func(delta int) {
		if len(r.Queue) == 0 {
			return
		}
		r.Index = ((r.Index+delta)%len(r.Queue) + len(r.Queue)) % len(r.Queue)
		r.PositionMs = 0
		r.UpdatedAtMs = arrivedMs
	}

	switch p.Cmd {
	case "play":
		r.freezeLocked(arrivedMs)
		r.Playing = true
	case "pause":
		r.freezeLocked(arrivedMs)
		r.Playing = false
	case "toggle":
		r.freezeLocked(arrivedMs)
		r.Playing = !r.Playing
	case "seek":
		if p.PositionMs >= 0 {
			r.PositionMs = p.PositionMs
			r.UpdatedAtMs = arrivedMs
		}
	case "next":
		nextTrack(1)
	case "prev":
		nextTrack(-1)
	case "setQueue":
		r.Queue = append([]TrackRef{}, p.Queue...)
		r.Index = clampIdx(p.Index, len(r.Queue))
		r.PositionMs = 0
		if p.PositionMs > 0 {
			r.PositionMs = p.PositionMs
		}
		r.UpdatedAtMs = arrivedMs
	case "queueAdd":
		if len(p.Items) > 0 {
			r.Queue = append(r.Queue, p.Items...)
		}
	case "queueRemove":
		if len(r.Queue) > 0 && p.Index >= 0 && p.Index < len(r.Queue) {
			switch {
			case p.Index < r.Index:
				r.Index-- // 删除当前曲之前的条目：下标前移保持指向同曲
			case p.Index == r.Index:
				r.PositionMs = 0 // 删除当前曲：顺延到新的当前曲，位置归零
			}
			r.Queue = append(r.Queue[:p.Index], r.Queue[p.Index+1:]...)
			r.Index = clampIdx(r.Index, len(r.Queue))
			r.UpdatedAtMs = arrivedMs
		}
	case "setIndex":
		// 点共享队列的某曲播放：仅切当前下标、位置归零，不换队列
		if p.Index >= 0 && p.Index < len(r.Queue) {
			r.Index = p.Index
			r.PositionMs = 0
			r.UpdatedAtMs = arrivedMs
		}
	case "kick":
		if !isHost {
			return nil, "", false, fmt.Errorf("仅房主可踢人")
		}
		r.broadcastStateLocked()
		r.broadcastMembersLocked()
		return r.snapshotLocked(nowMs()), p.UserID, false, nil
	case "close":
		if !isHost {
			return nil, "", false, fmt.Errorf("仅房主可关闭房间")
		}
		r.Closed = true
		r.broadcastStateLocked()
		r.broadcastLocked("closed", map[string]any{})
		return r.snapshotLocked(nowMs()), "", true, nil
	default:
		return nil, "", false, fmt.Errorf("未知命令: %s", p.Cmd)
	}

	r.broadcastStateLocked()
	r.broadcastMembersLocked()
	return r.snapshotLocked(nowMs()), "", false, nil
}

func clampIdx(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// kickMember 把某成员踢出：先给它单发 closed(kicked) 帧再摘除。
func (r *Room) kickMember(userID string) {
	r.mu.Lock()
	if m, ok := r.Members[userID]; ok {
		select {
		case m.evt <- sseFrame("closed", `{"kicked":true}`):
		default:
		}
		close(m.evt)
		delete(r.Members, userID)
	}
	r.broadcastMembersLocked()
	r.mu.Unlock()
}

// closeRoom 关房：广播已在 applyCommand 完成，这里从全局表移除。
func (r *Room) closeRoom() {
	rooms.Delete(r.Code)
}

// handleRoomEvents GET /room/events?code=&t= —— SSE 事件流。
func handleRoomEvents(tr *http.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		room, ok := getRoom(strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code"))))
		if !ok {
			writeMobile(w, http.StatusOK, 404, "房间不存在或已关闭", nil)
			return
		}
		id, name, avatar, _, err := identity(r, tr)
		if err != nil {
			writeMobile(w, http.StatusUnauthorized, 401, "无法识别用户: "+err.Error(), nil)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeMobile(w, http.StatusInternalServerError, 500, "不支持流式响应", nil)
			return
		}

		mrole := "member"
		if id == room.HostID {
			mrole = "host"
		}
		m := room.addMember(id, name, avatar, mrole)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // 关掉可能的反代缓冲
		fmt.Fprint(w, "retry: 3000\n\n")

		// 首帧：立即下发当前 state + members（不等广播）
		room.mu.Lock()
		state := room.snapshotLocked(nowMs())
		members := room.membersLocked()
		room.mu.Unlock()
		if b, err := json.Marshal(state); err == nil {
			fmt.Fprint(w, sseFrame("state", string(b)))
		}
		if b, err := json.Marshal(members); err == nil {
			fmt.Fprint(w, sseFrame("members", string(b)))
		}
		flusher.Flush()

		ping := time.NewTicker(ssePingFreq)
		defer ping.Stop()
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				room.removeMember(id)
				return
			case frame, alive := <-m.evt:
				if !alive {
					return // 被踢/房间关闭：通道已关
				}
				if _, err := fmt.Fprint(w, frame); err != nil {
					room.removeMember(id)
					return
				}
				flusher.Flush()
			case <-ping.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					room.removeMember(id)
					return
				}
				flusher.Flush()
			}
		}
	}
}

// roomJanitor 周期回收：空房间或超 TTL 无活动的房间。
func roomJanitor() {
	t := time.NewTicker(roomGCFreq)
	defer t.Stop()
	for range t.C {
		deadline := nowMs() - roomTTL.Milliseconds()
		rooms.Range(func(k, v any) bool {
			r := v.(*Room)
			r.mu.Lock()
			empty := len(r.Members) == 0
			idle := r.UpdatedAtMs < deadline
			r.mu.Unlock()
			if empty || idle {
				rooms.Delete(k)
			}
			return true
		})
	}
}
