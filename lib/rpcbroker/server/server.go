package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
)

const (
	appCenterAddr  = "/run/com.trim.app.center.sock"
	brokerAddr     = "/run/trim_app_cgi/rpcbroker"
	defaultToken   = "reserved"
	magicNumber    = "CPRT" // magic number for rpc protocol
	headerSize     = 80
	payloadLenPos  = 18
	payloadLenSize = 2
)

type (
	Service struct {
		Id    string `json:"id"`
		Name  string `json:"name"`
		IP    string `json:"ip"`
		Uds   string `json:"uds"`
		Type  int    `json:"type"`
		Token string `json:"token"`
	}

	Request struct {
		Header []byte `json:"-"`
		Data   struct {
			Uid      uint32   `json:"uid"`
			Pid      uint32   `json:"pid"`
			Req      string   `json:"req"`
			ReqId    string   `json:"reqid"`
			AppName  string   `json:"appName"`
			UserName string   `json:"user"`
			Services []string `json:"services,omitempty"`
		} `json:"data"`
	}

	BaseResp struct {
		Data   any    `json:"data,omitempty"`
		ReqId  string `json:"reqid"`
		Result string `json:"result"`
		Rev    string `json:"rev"`
		Req    string `json:"req,omitempty"`
	}

	Response struct {
		Data BaseResp `json:"data"`
	}

	AppAuthorizedDir struct {
		Type     int    `json:"storageType"`
		Path     string `json:"path"`
		UserName string `json:"uname"`
	}

	AuthPath struct {
		Editable bool   `json:"isEditable"`
		Perm     int    `json:"perm"`
		Status   int    `json:"status"`
		Path     string `json:"path"`
	}
)

var (
	// logPath           string
	folders           string
	authPathResp      []byte
	appAuthorizedDirs []AppAuthorizedDir
	marshaledUserId   = json.RawMessage(`{"uid": 1000}`)
	marshaledVolsInfo = json.RawMessage(`{"vols":[{"index":1,"state":0,"sysname":"dm-0","uuid":"trim_00000000_1111_2222_3333_444444444444-0","size":107374182400,"used":0,"voltype":61267}],"count":1}`)
	services          = []Service{
		{Id: "com.trim.main", Name: "TRIM Service", Uds: brokerAddr, Token: defaultToken, Type: 1},
		{Id: "com.trim.sysinfo", Name: "System Info Provider Service", Uds: brokerAddr, Token: defaultToken},
		{Id: "com.trim.filestor", Name: "File Storage Service", Uds: brokerAddr, Token: defaultToken},
		{Id: "com.trim.usersrv", Name: "User Service", Uds: brokerAddr, Token: defaultToken},
	}
)

func (rpc *RpcbrokerServer) InitData(mediaDir string) {
	folders = mediaDir
	splited := strings.Split(folders, ":")
	appAuthorizedDirs = make([]AppAuthorizedDir, 0, len(splited))
	authPaths := make([]AuthPath, 0, len(splited))
	for _, v := range splited {
		if strings.TrimSpace(v) == "" {
			continue
		}
		appAuthorizedDirs = append(appAuthorizedDirs, AppAuthorizedDir{Path: v, Type: 3, UserName: "admin"})
		authPaths = append(authPaths, AuthPath{Path: v, Perm: 6, Editable: true})
	}
	authPathResp, _ = json.Marshal(map[string]any{"code": 0, "msg": "", "data": map[string][]AuthPath{"list": authPaths}})
}

func NewResp(req *Request, data any) BaseResp {
	return BaseResp{ReqId: req.Data.ReqId, Req: req.Data.Req, Data: data, Result: "succ", Rev: "0.1"}
}

func NewErrorResp(req *Request) BaseResp {
	return BaseResp{ReqId: req.Data.ReqId, Req: req.Data.Req, Result: "fail", Rev: "0.1"}
}

func readHeader(conn net.Conn) ([]byte, error) {
	header := make([]byte, headerSize)
	_, err := io.ReadFull(conn, header)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(header[:4], []byte(magicNumber)) {
		return nil, fmt.Errorf("invalid magic number: %x", header[:4])
	}

	return header, nil
}

func parseRequest(conn net.Conn) (*Request, error) {
	header, err := readHeader(conn)
	if err != nil {
		log.Printf("read header error from %s: %v\n", conn.RemoteAddr(), err)
		return nil, err
	}

	plLen := getPayloadLength(header)
	payload, err := readPayload(conn, plLen)
	if err != nil {
		log.Println("header:", string(header))
		log.Printf("read payload error from %s: %v\n", conn.RemoteAddr(), err)
		log.Println("payload:", string(payload))
		return nil, err
	}

	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Println(string(payload))
		return nil, fmt.Errorf("unmarshal payload failed: %w", err)
	}

	log.Println("request:", string(payload))
	req.Header = header
	return &req, nil
}

func getPayloadLength(header []byte) uint16 {
	return binary.LittleEndian.Uint16(header[payloadLenPos : payloadLenPos+payloadLenSize])
}

func readPayload(conn net.Conn, length uint16) ([]byte, error) {
	if length == 0 {
		return nil, fmt.Errorf("payload len is zero")
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(conn, payload)
	return payload, err
}

func writeResponse(conn net.Conn, header []byte, payload []byte) error {
	length := uint16(len(payload))
	binary.LittleEndian.PutUint16(header[payloadLenPos:payloadLenPos+payloadLenSize], length)
	_, err := conn.Write(append(header, payload...))
	return err
}

func processRequest(req *Request) BaseResp {
	switch req.Data.Req {
	case "com.trim.rpcbroker.apply":
		return NewResp(req, services)

	case "com.trim.usersrv.getUserId", "com.trim.sysinfo.getUserId":
		return NewResp(req, marshaledUserId)

	case "com.trim.filestor.getAppAuthorizedDir":
		return NewResp(req, appAuthorizedDirs)

	case "com.trim.sysinfo.getAllVolsInfo":
		return NewResp(req, marshaledVolsInfo)

	default:
		log.Println("unknown req:", req.Data.Req)
		return NewErrorResp(req)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr()

	log.Printf("client %s connected\n", addr)

	for {
		req, err := parseRequest(conn)
		if err != nil {
			log.Printf("read request error from %s: %v\n", addr, err)
			return
		}

		resp := processRequest(req)

		data, _ := json.Marshal(Response{Data: resp})
		log.Println("response:", string(data))
		if err := writeResponse(conn, req.Header, data); err != nil {
			log.Printf("write resp error: %v\n", err)
			return
		}
	}
}

type RpcbrokerServer struct {
	Envs []string
}

func  NewServer(envs []string) *RpcbrokerServer {
	
	return &RpcbrokerServer {
		Envs: envs,
	}
}

func (rpc *RpcbrokerServer) Start() {
	// start auth http server
	os.RemoveAll(appCenterAddr)
	al, err := net.Listen("unix", appCenterAddr)
	if err != nil {
		log.Fatalf("[rpcbroker] cannot listen http unix: %v", err)
	}
	defer al.Close()

	http.HandleFunc("/rpc/v1/sysconfig/app/auth-path", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(authPathResp)
	})

	go func() {
		log.Println("app center serve:", http.Serve(al, nil))
	}()

	// start rpc broker
	os.Remove(brokerAddr)
	os.MkdirAll(path.Dir(brokerAddr), 0755)

	listener, err := net.Listen("unix", brokerAddr)
	if err != nil {
		log.Fatalf("listen rpc broker %s failed: %v", brokerAddr, err)
	}
	defer listener.Close()

	log.Printf("rpc broker listening on %s\n", brokerAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v\n", err)
			continue
		}
		go handleConnection(conn)
	}
}
