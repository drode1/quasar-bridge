package assistant

import (
	"context"
	"quasarbridge/internal/llm"
	"strings"
	"testing"
)

type urlTool struct{ asked []string }

func (t *urlTool) Definitions() []llm.Tool {
	return []llm.Tool{definition("read_webpage", "Читает страницу", map[string]any{"url": map[string]string{"type": "string"}}, []string{"url"})}
}

func (t *urlTool) Call(_ context.Context, _ string, args string) (any, error) {
	t.asked = append(t.asked, args)
	return map[string]string{"text": "ok"}, nil
}

func TestAgentRejectsUnrequestedPage(t *testing.T) {
	tools := &urlTool{}
	rounds := 0
	agent := Agent{Tools: tools, MaxRounds: 2, MaxCalls: 2, Client: chatFunc(func(_ context.Context, messages []llm.Message, _ []llm.Tool) (llm.Message, error) {
		rounds++
		if rounds == 1 {
			return llm.Message{ToolCalls: []llm.ToolCall{{ID: "a", Type: "function", Function: llm.FunctionCall{Name: "read_webpage", Arguments: `{"url":"https://invented.example/news"}`}}}}, nil
		}
		for _, message := range messages {
			if message.Role == "tool" && !strings.Contains(message.Content, "not in the user") {
				t.Errorf("tool error text leaked: %s", message.Content)
			}
		}
		return llm.Message{Content: "Ссылку не назвали."}, nil
	})}
	answer, err := agent.Complete(t.Context(), []llm.Message{{Role: "user", Content: "что нового в мире"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Ссылку не назвали." {
		t.Fatalf("unexpected answer %q", answer)
	}
	if len(tools.asked) != 0 {
		t.Fatalf("unrequested page fetched: %v", tools.asked)
	}
}
