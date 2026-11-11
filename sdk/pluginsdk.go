// Package pluginsdk is the stable API for Go launcher plugins. A plugin is
// an executable that serves JSON-RPC over stdin/stdout with 4-byte
// big-endian length frames.
//
// A plugin lives in a directory with a plugin.json next to its executable:
//
//	{
//	  "id": "vendor.tool", "name": "示例", "version": "1.0.0",
//	  "protocolVersion": 1, "executable": "tool.exe",
//	  "keywords": ["tool"], "permissions": [], "description": "一句话说明"
//	}
//
// main then serves a Handler, ideally with ServeFromManifest so the id cannot
// drift from the manifest:
//
//	func main() {
//		if err := pluginsdk.ServeFromManifest(handler{}); err != nil {
//			log.Fatal(err)
//		}
//	}
package pluginsdk

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const MaxMessageBytes = 4 << 20

// ProtocolVersion is the wire version this SDK speaks; it must match the
// manifest's protocolVersion and the launcher's own constant.
const ProtocolVersion = 1

// maxInflight is how many handler calls one plugin runs at once. The launcher
// sends one query per plugin per search, so this only fills up if the host is
// waiting on replies it never reads; the reply says so instead of the plugin
// piling up goroutines.
const maxInflight = 8

// Action kinds the launcher understands. They are strings on the wire; these
// constants keep a typo from silently dropping the action, and the host's
// plugin diagnostics report the kind it refused.
const (
	// KindCopyText copies Value to the clipboard.
	KindCopyText = "copyText"
	// KindInvoke asks the plugin to handle the action through execute.
	KindInvoke = "invoke"
	// KindOpenURL opens an http or https URL from Value.
	KindOpenURL = "openUrl"
	// KindOpenPath opens an absolute path from Value.
	KindOpenPath = "openPath"
)

// Query is one search. Text is what the user typed after the keyword, or the
// whole input when the plugin answers every search (a global plugin); Keyword
// is the trigger that matched, and is empty for a global query. MaxResults is
// an upper bound, not a target: return fewer, or none, when nothing matches.
// QueryVersion counts the searches of this session and can be used to drop
// work that the next search has already made pointless.
type Query struct {
	Text         string `json:"text"`
	MaxResults   int    `json:"maxResults"`
	QueryVersion int64  `json:"queryVersion"`
	Keyword      string `json:"keyword,omitempty"`
}

// Action is one thing the user can do with a result. Kind must be one of the
// Kind* constants, and Value means what the kind needs: the text to copy, the
// URL to open, the absolute path to open, or, for KindInvoke, whatever the
// plugin wants to find again when Execute gets the action id.
type Action struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Result is one row the launcher can show. ID identifies it inside the plugin
// (the host prefixes it), Title is required, and a result the launcher cannot
// read is dropped and reported in the plugin's diagnostics rather than shown
// empty.
type Result struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle"`
	Actions     []Action `json:"actions"`
	PreviewText string   `json:"previewText,omitempty"`
}

type QueryResult struct {
	Results []Result `json:"results"`
}

// ExecuteRequest is the action the user picked, with the result it belonged
// to; a struct rather than two bare strings, which were easy to swap.
type ExecuteRequest struct {
	ResultID string `json:"resultId"`
	ActionID string `json:"actionId"`
}

type InitializeResult struct {
	ProtocolVersion int      `json:"protocolVersion"`
	PluginID        string   `json:"pluginId"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

// ExecuteCommandRequest identifies a stable command declared in plugin.json.
type ExecuteCommandRequest struct {
	CommandID string `json:"commandId"`
}

// CommandHandler is optional. Implement it for result-independent invoke
// commands. Query-opening commands need no additional handler. Long-running
// interactive work should return after starting and complete asynchronously.
type CommandHandler interface {
	ExecuteCommand(context.Context, ExecuteCommandRequest) error
}

// ActivityHandler keeps interactive work alive across short execute RPCs.
// Return true while a capture, dialog, or image window (even hidden) exists.
// The host polls only before idle recycling; explicit stop still terminates it.
type ActivityHandler interface {
	HasActivity(context.Context) (bool, error)
}

type ActivityResult struct {
	Active bool `json:"active"`
}

// Handler is what a plugin implements.
type Handler interface {
	// Query returns the results for one search. It runs on its own goroutine
	// and must stop when ctx is done: the host cancels the queries of a search
	// that the next keystroke replaced.
	Query(context.Context, Query) ([]Result, error)
	// Execute runs one action of one earlier result, for a KindInvoke action.
	Execute(context.Context, ExecuteRequest) error
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the plugin on stdin/stdout until the host closes them. pluginID
// must equal the id in plugin.json: the launcher kills a plugin whose
// initialize reply names another id, and the plugin cannot tell why, so prefer
// ServeFromManifest.
func Serve(pluginID string, handler Handler) error {
	return ServeIO(pluginID, handler, os.Stdin, os.Stdout)
}

// ServeFromManifest is Serve with the id read from the plugin.json next to the
// running executable, so the two cannot drift apart.
func ServeFromManifest(handler Handler) error {
	id, err := ManifestID()
	if err != nil {
		return err
	}
	return Serve(id, handler)
}

// ManifestID reads the "id" from the plugin.json next to the executable.
func ManifestID() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位插件程序：%w", err)
	}
	return readManifestID(filepath.Dir(executable))
}

func readManifestID(dir string) (string, error) {
	path := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("插件程序同目录缺少 plugin.json（%s）", path)
		}
		return "", fmt.Errorf("无法读取 %s：%w", path, err)
	}
	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("plugin.json 格式错误：%w", err)
	}
	if manifest.ID == "" {
		return "", errors.New("plugin.json 缺少 id")
	}
	return manifest.ID, nil
}

func ServeIO(pluginID string, handler Handler, input io.Reader, output io.Writer) error {
	server := &server{pluginID: pluginID, handler: handler, output: output, active: map[string]context.CancelFunc{}}
	return server.run(input)
}

type server struct {
	pluginID string
	handler  Handler
	output   io.Writer
	writeMu  sync.Mutex
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
	inflight int
	wg       sync.WaitGroup
}

func (s *server) run(input io.Reader) error {
	for {
		payload, err := readFrame(input)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(payload, &req); err != nil {
			s.writeError(nil, -32700, "无法解析的 JSON")
			continue
		}
		if req.Method == "$/cancelRequest" {
			var cancel struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(req.Params, &cancel) == nil {
				s.cancel(string(cancel.ID))
			}
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			s.writeError(req.ID, -32600, "无效的请求：需要 JSON-RPC 2.0 和方法名")
			continue
		}
		key := string(req.ID)
		ctx, cancel := context.WithCancel(context.Background())
		if !s.begin(key, cancel) {
			cancel()
			s.writeError(req.ID, -32000, "插件同时处理的请求过多，请稍后重试")
			continue
		}
		s.wg.Add(1)
		go func(req request, ctx context.Context, cancel context.CancelFunc, key string) {
			defer s.wg.Done()
			defer cancel()
			defer s.finish(key)
			result, err := s.handle(ctx, req)
			if err != nil {
				s.writeError(req.ID, -32603, err.Error())
				return
			}
			s.writeResult(req.ID, result)
		}(req, ctx, cancel, key)
	}
	s.activeMu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.activeMu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *server) cancel(key string) {
	s.activeMu.Lock()
	cancel := s.active[key]
	s.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// begin registers one request and reports whether the plugin can take it. The
// count and the registration happen under one lock: reading the map without it
// raced with the goroutine that deletes its own entry.
func (s *server) begin(key string, cancel context.CancelFunc) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.inflight >= maxInflight {
		return false
	}
	s.inflight++
	if key != "" {
		s.active[key] = cancel
	}
	return true
}

func (s *server) finish(key string) {
	s.activeMu.Lock()
	s.inflight--
	if key != "" {
		delete(s.active, key)
	}
	s.activeMu.Unlock()
}

func (s *server) handle(ctx context.Context, req request) (any, error) {
	switch req.Method {
	case "initialize":
		result := InitializeResult{ProtocolVersion: ProtocolVersion, PluginID: s.pluginID}
		if _, ok := s.handler.(CommandHandler); ok {
			result.Capabilities = append(result.Capabilities, "executeCommand")
		}
		if _, ok := s.handler.(ActivityHandler); ok {
			result.Capabilities = append(result.Capabilities, "activity")
		}
		if _, ok := s.handler.(ThemeHandler); ok {
			result.Capabilities = append(result.Capabilities, "theme")
		}
		return result, nil
	case "setTheme":
		handler, ok := s.handler.(ThemeHandler)
		if !ok {
			return nil, errors.New("插件未实现 theme")
		}
		var theme Theme
		if err := json.Unmarshal(req.Params, &theme); err != nil {
			return nil, err
		}
		return map[string]any{}, handler.SetTheme(ctx, theme)
	case "activity":
		handler, ok := s.handler.(ActivityHandler)
		if !ok {
			return nil, errors.New("插件未实现 activity")
		}
		active, err := handler.HasActivity(ctx)
		return ActivityResult{Active: active}, err
	case "ping":
		return "pong", nil
	case "query":
		var query Query
		if err := json.Unmarshal(req.Params, &query); err != nil {
			return nil, fmt.Errorf("查询参数无效：%w", err)
		}
		results, err := s.handler.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		if results == nil {
			results = []Result{}
		}
		return QueryResult{Results: results}, nil
	case "executeCommand":
		handler, ok := s.handler.(CommandHandler)
		if !ok {
			return nil, errors.New("插件未实现 executeCommand")
		}
		var command ExecuteCommandRequest
		if err := json.Unmarshal(req.Params, &command); err != nil || command.CommandID == "" {
			return nil, errors.New("命令参数无效")
		}
		if err := handler.ExecuteCommand(ctx, command); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "execute":
		var execute ExecuteRequest
		if err := json.Unmarshal(req.Params, &execute); err != nil {
			return nil, fmt.Errorf("动作参数无效：%w", err)
		}
		if err := s.handler.Execute(ctx, execute); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("不支持的方法 %q", req.Method)
}

func (s *server) writeResult(id json.RawMessage, result any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeJSONFrame(s.output, response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, message string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeJSONFrame(s.output, response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length <= 0 || length > MaxMessageBytes {
		return nil, fmt.Errorf("invalid frame length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeJSONFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > MaxMessageBytes {
		return fmt.Errorf("message exceeds limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}
