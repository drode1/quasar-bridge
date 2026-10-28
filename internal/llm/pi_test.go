package llm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func piTestClient(t *testing.T, source string) *Client {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	path := filepath.Join(t.TempDir(), "pi.js")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return &Client{API: "pi", Model: "provider/model", PiScript: path, APIKey: "secret", MaxOutputTokens: 128}
}

func TestPiRequestAndSession(t *testing.T) {
	client := piTestClient(t, `
let input = '';
process.stdin.on('data', chunk => input += chunk);
process.stdin.on('end', () => {
  const r = JSON.parse(input);
  if (r.model !== 'provider/model' || r.session_id !== 'session-1' || r.messages[0].content !== 'hello' || r.tools.length !== 0 || r.structured !== true || 'max_output_tokens' in r || process.env.QUASAR_LLM_API_KEY !== 'secret') process.exit(1);
  process.stdout.write(JSON.stringify({message: {role:'assistant', content:'{"ok":true}'}}));
});`)
	answer, err := client.Complete(WithPiSession(context.Background(), "session-1"), []Message{{Role: "user", Content: "hello"}}, true)
	if err != nil || answer != `{"ok":true}` {
		t.Fatalf("answer = %q, error = %v", answer, err)
	}
}

func TestPiPublicErrors(t *testing.T) {
	cases := []struct {
		output string
		want   string
	}{
		{`{"error":"unknown model"}`, "unknown model"},
		{`{"error":"missing API key"}`, "missing API key"},
		{`{"error":"structured output unsupported"}`, "structured output unsupported"},
		{`{"error":"LLM answer exceeded token budget"}`, "LLM answer exceeded token budget"},
		{`{"error":"LLM request failed"}`, "LLM request failed"},
		{`{"error":"secret provider failure"}`, "Pi request failed"},
	}
	for _, tc := range cases {
		client := piTestClient(t, "process.stdout.write("+strconv.Quote(tc.output)+");")
		_, err := client.chat(context.Background(), nil, false, nil)
		if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), "secret") {
			t.Fatalf("error = %v, want %q", err, tc.want)
		}
	}
}

func TestPiGeneratedSession(t *testing.T) {
	client := piTestClient(t, `
let input = '';
process.stdin.on('data', chunk => input += chunk);
process.stdin.on('end', () => process.stdout.write(JSON.stringify({message:{role:'assistant',content:JSON.parse(input).session_id}})));`)
	first, err := client.Complete(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Complete(context.Background(), nil, false)
	if err != nil || len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("unexpected generated IDs %q, %q, error %v", first, second, err)
	}
}

func TestPiResponses(t *testing.T) {
	tools := []Tool{{Type: "function", Function: Function{Name: "lookup"}}}
	cases := []struct {
		name       string
		output     string
		structured bool
		tools      []Tool
		ok         bool
	}{
		{"text", `{"message":{"role":"assistant","content":"answer"}}`, false, nil, true},
		{"call", `{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}`, false, tools, true},
		{"unknown call", `{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}`, false, nil, false},
		{"bad arguments", `{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"lookup","arguments":"[]"}}]}}`, false, tools, false},
		{"duplicate IDs", `{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}`, false, tools, false},
		{"invalid structured", `{"message":{"role":"assistant","content":"[]"}}`, true, nil, false},
		{"valid structured", `{"message":{"role":"assistant","content":"{\"ok\":true}"}}`, true, nil, true},
		{"empty", ``, false, nil, false},
		{"invalid", `{`, false, nil, false},
		{"missing answer", `{"message":{"role":"assistant","content":""}}`, false, nil, false},
		{"wrong role", `{"message":{"role":"user","content":"answer"}}`, false, nil, false},
		{"error", `{"error":"secret"}`, false, nil, false},
		{"both", `{"message":{"role":"assistant","content":"answer"},"error":"secret"}`, false, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := piTestClient(t, "process.stdout.write("+strconv.Quote(tc.output)+");")
			_, err := client.chat(context.Background(), nil, tc.structured, tc.tools)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("response leaked in error")
			}
		})
	}
}

func TestPiLimitsAndCancellation(t *testing.T) {
	t.Run("output limit", func(t *testing.T) {
		client := piTestClient(t, `process.stdout.write('x'.repeat(5 * 1024 * 1024));`)
		if _, err := client.Chat(context.Background(), nil, nil); err == nil {
			t.Fatal("oversized output accepted")
		}
	})
	t.Run("input limit", func(t *testing.T) {
		client := piTestClient(t, `process.exit(0);`)
		if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: strings.Repeat("x", piMaxBytes)}}, nil); err == nil {
			t.Fatal("oversized input accepted")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		client := piTestClient(t, `setInterval(() => {}, 1000);`)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := client.Chat(ctx, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
	})
	t.Run("stderr is hidden", func(t *testing.T) {
		client := piTestClient(t, `console.error('secret'); process.exit(1);`)
		_, err := client.Chat(context.Background(), nil, nil)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
