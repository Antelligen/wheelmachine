package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"wheelmachine/frontend/cli/internal/fakeapi"
)

// The file tools.json holds what this server tells a client in its own words: initialize's instructions, and the
// tools as tools/list gives them - each one's name, title, description, input schema and annotations. Wheelmachine's
// hosted MCP server serves a copy of it, so that the two offer the same tools in the same words; a tool changed here
// without the file fails this test.
const toolsFile = "tools.json"

// Set to write tools.json anew from the tools as they are now: WM_MCP_TOOLS_WRITE=1 go test ./cmd/wm-mcp
const toolsWriteEnv = "WM_MCP_TOOLS_WRITE"

// toolsJSON is the file as the tools are now: indented, its characters as they are, as the server writes them.
func toolsJSON(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Instructions       string `json:"instructions"`
		DeployInstructions string `json:"deploy_instructions"`
		Tools              []tool `json:"tools"`
	}{instructions, deployInstructions, tools()}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestToolsJSONIsWhatTheServerOffers(t *testing.T) {
	want := toolsJSON(t)
	if os.Getenv(toolsWriteEnv) == "1" {
		if err := os.WriteFile(toolsFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(toolsFile)
	if err != nil {
		t.Fatalf("%v: write it with %s=1 go test ./cmd/wm-mcp", err, toolsWriteEnv)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not what the server offers: write it anew with %s=1 go test ./cmd/wm-mcp, and give the hosted server the same copy",
			toolsFile, toolsWriteEnv)
	}
	// and it is what initialize and tools/list answer
	var file struct {
		Instructions string `json:"instructions"`
		Tools        []any  `json:"tools"`
	}
	if err := json.Unmarshal(got, &file); err != nil {
		t.Fatal(err)
	}
	s := start(t, connectTo(fakeapi.New(t)))
	if said := s.initialize("2025-11-25")["instructions"]; said != file.Instructions {
		t.Fatalf("initialize says %q", said)
	}
	// (those of Deploy's it holds are for the hosted server until Deploy opens: deploy_test.go)
	if listed := ok(t, s.request(2, "tools/list", nil))["tools"]; !reflect.DeepEqual(listed, file.Tools[:len(offered())]) {
		t.Fatalf("tools/list answers other tools than %s", toolsFile)
	}
}
