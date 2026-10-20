package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startRoomFakeUpstream 在临时 unix socket 起假 trim-music：/user/me 依据 Cookie 里的 music-token
// 返回不同用户（hostTk→房主 admin，memTk→普通 member），用于房间接口的身份解析。
func startRoomFakeUpstream(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "room-fake.socket")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/music/api/v1/user/me", func(w http.ResponseWriter, r *http.Request) {
		tok := ""
		for _, c := range strings.Split(r.Header.Get("Cookie"), ";") {
			c = strings.TrimSpace(c)
			if strings.HasPrefix(c, "music-token=") {
				tok = strings.TrimPrefix(c, "music-token=")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch tok {
		case "hostTk":
			_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"guid":"u_host","name":"房主","role":"admin"}}`)
		case "memTk":
			_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"guid":"u_mem","name":"好友","role":"user"}}`)
		default:
			_, _ = io.WriteString(w, `{"code":401,"msg":"unauthorized","data":null}`)
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return sock, func() { _ = srv.Shutdown(context.Background()); _ = ln.Close() }
}

// roomResp 解析统一信封。
func roomResp(t *testing.T, body []byte) (int, string, map[string]any) {
	t.Helper()
	var resp struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("非法 JSON: %v / %s", err, string(body))
	}
	return resp.Code, resp.Msg, resp.Data
}

func postRoom(t *testing.T, h http.Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", mobileAPIPrefix+"/api/v1/room/"+path, strings.NewReader(body))
	req.Header.Set("X-Music-Token", token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func stateOf(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	s, _ := data["state"].(map[string]any)
	return s
}

func TestRoomFlow(t *testing.T) {
	sock, cleanup := startRoomFakeUpstream(t)
	defer cleanup()
	h := mobileAPIHandler(newUpstreamTransport(sock))

	// 1) 创建房间
	rec := postRoom(t, h, "create", "hostTk", `{}`)
	code, _, data := roomResp(t, rec.Body.Bytes())
	if code != 0 {
		t.Fatalf("create 失败: %d %s", code, rec.Body.String())
	}
	roomCode, _ := data["code"].(string)
	if roomCode == "" {
		t.Fatalf("create 未返回房间码: %s", rec.Body.String())
	}
	st := stateOf(data)
	if st == nil || st["hostId"] != "u_host" {
		t.Fatalf("create state 异常: %+v", data)
	}

	// 2) 好友加入
	rec = postRoom(t, h, "join", "memTk", `{"code":"`+roomCode+`"}`)
	code, _, data = roomResp(t, rec.Body.Bytes())
	if code != 0 {
		t.Fatalf("join 失败: %s", rec.Body.String())
	}
	if members, _ := data["members"].([]any); len(members) != 2 {
		t.Fatalf("join 后应 2 名成员，实际 %v", data["members"])
	}

	// 3) 房主设置共享队列 → version 递增、index 归位
	rec = postRoom(t, h, "command", "hostTk", `{"code":"`+roomCode+`","cmd":"setQueue","queue":[{"guid":"g1","title":"T1"},{"guid":"g2","title":"T2"},{"guid":"g3","title":"T3"}],"index":1}`)
	_, _, data = roomResp(t, rec.Body.Bytes())
	st = stateOf(data)
	if st == nil || st["index"].(float64) != 1 || len(st["queue"].([]any)) != 3 {
		t.Fatalf("setQueue 未生效: %+v", st)
	}
	v1 := st["version"].(float64)

	// 4) 播放：playing=true
	rec = postRoom(t, h, "command", "hostTk", `{"code":"`+roomCode+`","cmd":"play"}`)
	_, _, data = roomResp(t, rec.Body.Bytes())
	st = stateOf(data)
	if st["playing"] != true || st["version"].(float64) <= v1 {
		t.Fatalf("play 未生效或版本未增: %+v", st)
	}
	v2 := st["version"].(float64)

	// 5) 下一首（好友发命令，共享控制）：index 从 1 → 2
	rec = postRoom(t, h, "command", "memTk", `{"code":"`+roomCode+`","cmd":"next"}`)
	_, _, data = roomResp(t, rec.Body.Bytes())
	st = stateOf(data)
	if st["index"].(float64) != 2 {
		t.Fatalf("next 未推进 index: %+v", st)
	}

	// 6) 任意端 GET state 收敛到同一版本
	req := httptest.NewRequest("GET", mobileAPIPrefix+"/api/v1/room/state?code="+roomCode, nil)
	req.Header.Set("X-Music-Token", "memTk")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	_, _, data = roomResp(t, rec2.Body.Bytes())
	st = stateOf(data)
	if st["index"].(float64) != 2 || st["version"].(float64) <= v2 {
		t.Fatalf("state 补拉未收敛: %+v", st)
	}

	// 7) 普通成员无权踢人
	rec = postRoom(t, h, "command", "memTk", `{"code":"`+roomCode+`","cmd":"kick","userId":"u_host"}`)
	code, _, _ = roomResp(t, rec.Body.Bytes())
	if code == 0 {
		t.Fatalf("member 踢人应被拒，实际 code=%d", code)
	}
}

// TestRoomTouchPreservesPosition 回归：播放中只调 touch 的命令（如 kick）不得把权威进度“倒带”回锚点。
// 曾有 bug：touchLocked 仅前移 UpdatedAtMs 而不推进 PositionMs，导致 currentPosMs 丢掉已播时长，
// join/kick 等事件让全房进度跳回上次锚点（后进成员不同步 / 播放中踢人整曲重播）。
func TestRoomTouchPreservesPosition(t *testing.T) {
	sock, cleanup := startRoomFakeUpstream(t)
	defer cleanup()
	h := mobileAPIHandler(newUpstreamTransport(sock))

	rec := postRoom(t, h, "create", "hostTk", `{}`)
	_, _, data := roomResp(t, rec.Body.Bytes())
	roomCode, _ := data["code"].(string)
	if roomCode == "" {
		t.Fatalf("create 未返回房间码")
	}

	// 设定队列并从 5000ms 处起播
	postRoom(t, h, "command", "hostTk", `{"code":"`+roomCode+`","cmd":"setQueue","queue":[{"guid":"g1","title":"T1"}],"index":0,"positionMs":5000}`)
	postRoom(t, h, "command", "hostTk", `{"code":"`+roomCode+`","cmd":"play"}`)

	// 让权威位置按服务器时钟外推一段真实时间
	time.Sleep(1500 * time.Millisecond)

	// 发一条不改播放态、仅触发 touch 的命令（kick 一个不存在的成员，幂等）
	rec = postRoom(t, h, "command", "hostTk", `{"code":"`+roomCode+`","cmd":"kick","userId":"ghost"}`)
	_, _, data = roomResp(t, rec.Body.Bytes())
	st := stateOf(data)
	if st == nil {
		t.Fatalf("kick 未返回 state")
	}
	// 起播于 5000ms，等了 ~1500ms，进度应 ≈ 6500ms 而非回落到 5000ms
	if pos, _ := st["positionMs"].(float64); pos < 6000 {
		t.Fatalf("touch 命令把进度倒带了：positionMs=%v 期望 ≥6000（5000 起播 + ~1500ms 外推）", st["positionMs"])
	}
}

func TestRoomSSEFirstFrame(t *testing.T) {
	sock, cleanup := startRoomFakeUpstream(t)
	defer cleanup()
	h := mobileAPIHandler(newUpstreamTransport(sock))

	// 建一个房间
	rec := postRoom(t, h, "create", "hostTk", `{}`)
	_, _, data := roomResp(t, rec.Body.Bytes())
	roomCode, _ := data["code"].(string)

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+mobileAPIPrefix+"/api/v1/room/events?code="+roomCode+"&t=hostTk", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE 连接失败: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type 错误: %q", ct)
	}

	// 读前几帧，应能拿到一个 event: state
	br := bufio.NewReader(resp.Body)
	gotState := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "event: state") {
			gotState = true
			break
		}
	}
	if !gotState {
		t.Fatalf("未在 SSE 流中收到 state 帧")
	}
}
