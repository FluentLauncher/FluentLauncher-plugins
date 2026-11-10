package pluginsdk

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type handler struct {
	executed *ExecuteRequest
	queried  *Query
}

func (h *handler) Query(ctx context.Context, q Query) ([]Result, error) {
	h.queried = &q
	return []Result{{ID: "echo", Title: q.Text, Subtitle: "s",
		Actions: []Action{{ID: "copy", Title: "Copy", Kind: KindCopyText, Value: q.Text}}}}, nil
}

func (h *handler) Execute(_ context.Context, req ExecuteRequest) error {
	h.executed = &req
	return nil
}

func TestServeQuery(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeIO("official.echo", &handler{}, inputReader, outputWriter) }()

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	var initialized InitializeResult
	readResult(t, outputReader, &initialized)
	if initialized.ProtocolVersion != ProtocolVersion || initialized.PluginID != "official.echo" {
		t.Fatalf("initialize = %+v", initialized)
	}

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "query",
		"params": map[string]any{"text": "hi", "maxResults": 5, "queryVersion": 1, "keyword": "echo"}})
	var result QueryResult
	readResult(t, outputReader, &result)
	if len(result.Results) != 1 || result.Results[0].Title != "hi" {
		t.Fatalf("query = %+v", result)
	}
	inputWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The action reached the plugin as one request value: two bare strings made
// the result id and the action id easy to swap, with no compile error to say so.
func TestServeExecute(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &handler{}
	done := make(chan error, 1)
	go func() { done <- ServeIO("official.echo", h, inputReader, outputWriter) }()

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	var initialized InitializeResult
	readResult(t, outputReader, &initialized)

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "execute",
		"params": map[string]any{"resultId": "r1", "actionId": "a1"}})
	var executed map[string]any
	readResult(t, outputReader, &executed)
	if h.executed == nil || h.executed.ResultID != "r1" || h.executed.ActionID != "a1" {
		t.Fatalf("executed = %+v", h.executed)
	}
	inputWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// An unknown method is reported, not ignored: a plugin author who mistypes the
// method name sees why the host got no results.
func TestServeUnknownMethod(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeIO("official.echo", &handler{}, inputReader, outputWriter) }()

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	var initialized InitializeResult
	readResult(t, outputReader, &initialized)

	write(t, inputWriter, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "nope"})
	message := readError(t, outputReader)
	if !strings.Contains(message, "不支持的方法") || !strings.Contains(message, "nope") {
		t.Fatalf("error = %q", message)
	}
	inputWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestManifestID(t *testing.T) {
	dir := t.TempDir()
	if _, err := readManifestID(dir); err == nil || !strings.Contains(err.Error(), "缺少 plugin.json") {
		t.Fatalf("missing manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifestID(dir); err == nil || !strings.Contains(err.Error(), "缺少 id") {
		t.Fatalf("manifest without id: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"vendor.tool"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := readManifestID(dir)
	if err != nil || id != "vendor.tool" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func write(t *testing.T, w io.Writer, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func readTestFrame(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func readResult(t *testing.T, r io.Reader, target any) {
	t.Helper()
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(readTestFrame(t, r), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("error = %v", response.Error)
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		t.Fatal(err)
	}
}

func readError(t *testing.T, r io.Reader) string {
	t.Helper()
	var response struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(readTestFrame(t, r), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		t.Fatal("no error in the response")
	}
	return response.Error.Message
}
