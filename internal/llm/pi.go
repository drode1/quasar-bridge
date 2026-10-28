package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

const piMaxBytes = 4 << 20

type piSessionKey struct{}

// WithPiSession associates Pi requests with a stable session identifier.
func WithPiSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, piSessionKey{}, id)
}

type piOutput struct {
	bytes.Buffer
}

func (w *piOutput) Write(p []byte) (int, error) {
	if len(p) > piMaxBytes-w.Len() {
		return 0, errors.New("Pi response too large")
	}
	return w.Buffer.Write(p)
}

func (c *Client) pi(ctx context.Context, messages []Message, structured bool, tools []Tool) (Message, error) {
	provider, model, ok := strings.Cut(c.Model, "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" || strings.TrimSpace(c.PiScript) == "" {
		return Message{}, errors.New("invalid Pi configuration")
	}
	sessionID, _ := ctx.Value(piSessionKey{}).(string)
	if sessionID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Message{}, errors.New("cannot generate Pi session ID")
		}
		sessionID = hex.EncodeToString(id[:])
	}
	request := struct {
		Model           string    `json:"model"`
		SessionID       string    `json:"session_id"`
		Messages        []Message `json:"messages"`
		Tools           []Tool    `json:"tools"`
		Structured      bool      `json:"structured"`
		BaseURL         string    `json:"base_url,omitempty"`
		ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	}{c.Model, sessionID, messages, tools, structured, c.BaseURL, c.ReasoningEffort}
	if request.Messages == nil {
		request.Messages = []Message{}
	}
	if request.Tools == nil {
		request.Tools = []Tool{}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return Message{}, errors.New("invalid Pi request")
	}
	if len(data) > piMaxBytes {
		return Message{}, errors.New("Pi request too large")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", c.PiScript)
	cmd.Stdin = bytes.NewReader(data)
	var output piOutput
	cmd.Stdout = &output
	// Do not pass unrelated credentials to the subprocess or expose its stderr.
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "NODE_USE_ENV_PROXY"} {
		if value, exists := os.LookupEnv(key); exists {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "QUASAR_LLM_API_KEY="+c.APIKey)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, errors.New("Pi subprocess failed")
	}
	var response struct {
		Message *Message `json:"message"`
		Error   *string  `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || (response.Message == nil) == (response.Error == nil) {
		return Message{}, errors.New("invalid Pi response")
	}
	if response.Error != nil {
		return Message{}, piBackendError(*response.Error)
	}
	message := *response.Message
	if message.Role != "assistant" || (strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0) || message.ToolCallID != "" {
		return Message{}, errors.New("invalid Pi response message")
	}
	if structured && (len(message.ToolCalls) != 0 || !piJSONObject(message.Content)) {
		return Message{}, errors.New("invalid Pi structured response")
	}
	seen := make(map[string]bool, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		allowed := false
		for _, tool := range tools {
			if tool.Type == "function" && tool.Function.Name == call.Function.Name {
				allowed = true
				break
			}
		}
		if strings.TrimSpace(call.ID) == "" || seen[call.ID] || call.Type != "function" || !allowed || strings.TrimSpace(call.Function.Name) == "" || !piJSONObject(call.Function.Arguments) {
			return Message{}, errors.New("invalid Pi function call")
		}
		seen[call.ID] = true
	}
	return message, nil
}

func piBackendError(code string) error {
	switch code {
	case "unknown model":
		return errors.New("unknown model")
	case "missing API key":
		return errors.New("missing API key")
	case "structured output unsupported":
		return errors.New("structured output unsupported")
	case "LLM answer exceeded token budget":
		return errors.New("LLM answer exceeded token budget")
	case "LLM request failed":
		return errors.New("LLM request failed")
	default:
		return errors.New("Pi request failed")
	}
}

func piJSONObject(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "{") && jsontext.Value(trimmed).IsValid()
}
