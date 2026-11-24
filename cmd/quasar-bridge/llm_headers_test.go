package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skrashevich/quasar-bridge/internal/llm"
)

func TestLLMProviderHeadersFromEnv(t *testing.T) {
	t.Setenv("QUASAR_LLM_HEADERS", `{"x-opencode-session":"ses_fixture","user-agent":"quasar-bridge/1.0"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "quasar-bridge/1.0" || r.Header.Get("X-Opencode-Session") != "ses_fixture" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			http.Error(w, "incorrect provider headers", http.StatusBadRequest)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		case "POST /v1/responses":
			io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
		case "GET /v1/models":
			io.WriteString(w, `{"data":[{"id":"fixture-model"}]}`)
		case "POST /redirect/chat/completions":
			http.Redirect(w, r, "/v1/chat/completions", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("QUASAR_LLM_BASE_URL", server.URL+"/v1")
	message := []llm.Message{{Role: "user", Content: "hello"}}
	for _, api := range []string{"chat_completions", "responses"} {
		t.Run(api, func(t *testing.T) {
			t.Setenv("QUASAR_LLM_API", api)
			cfg, err := parseConfig([]byte(`{"llm_model":"fixture-model"}`), false)
			if err != nil {
				t.Fatal(err)
			}
			client := newLLMClient(cfg, "fixture-key")
			if models, err := client.Models(t.Context()); err != nil || len(models) != 1 || models[0].ID != "fixture-model" {
				t.Fatalf("models=%v, err=%v", models, err)
			}
			if reply, err := client.Complete(t.Context(), message, false); err != nil || reply != "ok" {
				t.Fatalf("reply=%q, err=%v", reply, err)
			}
			if api == "chat_completions" {
				client.BaseURL = server.URL + "/redirect"
				_, err := client.Complete(t.Context(), message, false)
				var status *llm.HTTPError
				if !errors.As(err, &status) || status.StatusCode != http.StatusTemporaryRedirect {
					t.Fatalf("redirect followed with custom headers: %v", err)
				}
			}
		})
	}
}

func TestLLMHeadersRejectUnsafeValues(t *testing.T) {
	for _, tc := range []struct{ name, api, value string }{
		{"not an object", "chat_completions", `null`},
		{"null value", "chat_completions", `{"X-Session":null}`},
		{"line break", "chat_completions", `{"X-Session":"fixture-value\r\ninjected"}`},
		{"invalid name", "chat_completions", `{"X Session":"fixture-value"}`},
		{"duplicate", "chat_completions", `{"X-Session":"fixture-value","x-session":"other"}`},
		{"authorization", "chat_completions", `{"authorization":"fixture-value"}`},
		{"chatgpt subscription", "chatgpt", `{"X-Session":"fixture-value"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QUASAR_LLM_HEADERS", tc.value)
			t.Setenv("QUASAR_LLM_API", tc.api)
			_, err := parseConfig([]byte(`{}`), false)
			if err == nil || !strings.Contains(err.Error(), "QUASAR_LLM_HEADERS") || strings.Contains(err.Error(), "fixture-value") {
				t.Fatalf("expected header rejection without disclosing the value: %v", err)
			}
		})
	}
}
