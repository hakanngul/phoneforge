// Package llm is a tiny, dependency-free client layer for tool-using LLMs.
// It defines a provider-neutral conversation model and two implementations
// (Anthropic and Ollama) selected from environment variables.
package llm

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Block types used in Message.Content.
const (
	BlockText       = "text"
	BlockImage      = "image"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Block is one piece of message content. Which fields are meaningful depends
// on Type.
type Block struct {
	Type string `json:"type"`
	// Text is the body of a text block.
	Text string `json:"text,omitempty"`
	// ID is the tool_use id (tool_use) or the id being answered (tool_result).
	ID string `json:"id,omitempty"`
	// Name is the tool name (tool_use, and tool_result for providers that need it).
	Name string `json:"name,omitempty"`
	// Input holds the tool arguments of a tool_use block.
	Input map[string]any `json:"input,omitempty"`
	// Content is the textual output of a tool_result block.
	Content string `json:"content,omitempty"`
	// IsError marks a tool_result as a failure.
	IsError bool `json:"isError,omitempty"`
	// Image holds raw PNG bytes of an image block.
	Image []byte `json:"-"`
	// Transient blocks (fresh observations) are dropped from older turns
	// to keep the transcript bounded.
	Transient bool `json:"-"`
}

// Message is one conversation turn. Role is "user" or "assistant".
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Tool describes a function the model may call. Schema is a JSON Schema
// object describing the arguments.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Reply is the model's answer to one Chat call.
type Reply struct {
	// Text is any free-form reasoning/text the model produced.
	Text string `json:"text"`
	// ToolCalls are the tools the model wants to run.
	ToolCalls []ToolCall `json:"toolCalls"`
	// StopReason is the provider's stop reason, informational only.
	StopReason string `json:"stopReason,omitempty"`
}

// Client talks to an LLM provider.
type Client interface {
	// Chat sends the conversation and returns the model's reply.
	Chat(ctx context.Context, system string, messages []Message, tools []Tool) (Reply, error)
}

// VisionCapable is optionally implemented by clients that accept image blocks.
type VisionCapable interface {
	// SupportsVision reports whether image blocks will be understood.
	SupportsVision() bool
}

// SupportsVision reports whether c accepts images.
func SupportsVision(c Client) bool {
	v, ok := c.(VisionCapable)
	return ok && v.SupportsVision()
}

// Provider names returned by Select.
const (
	ProviderAnthropic = "anthropic"
	ProviderOllama    = "ollama"
)

// DefaultOllamaHost is probed when OLLAMA_HOST is unset.
const DefaultOllamaHost = "http://127.0.0.1:11434"

// Select picks a provider from the environment (read through getenv):
// Anthropic if ANTHROPIC_API_KEY is set; otherwise Ollama if OLLAMA_HOST is
// set, or if probe reports a reachable default local Ollama. probe may be nil
// (meaning "not reachable"). It returns ("", false) with a nil Client when no
// provider is configured.
func Select(getenv func(string) string, probe func(host string) bool) (Client, string, bool) {
	if key := strings.TrimSpace(getenv("ANTHROPIC_API_KEY")); key != "" {
		return NewAnthropic(key, getenv("ANTHROPIC_MODEL")), ProviderAnthropic, true
	}
	host := strings.TrimSpace(getenv("OLLAMA_HOST"))
	if host == "" {
		if probe == nil || !probe(DefaultOllamaHost) {
			return nil, "", false
		}
		host = DefaultOllamaHost
	}
	vision := strings.EqualFold(getenv("OLLAMA_VISION"), "1") || strings.EqualFold(getenv("OLLAMA_VISION"), "true")
	return NewOllama(host, getenv("OLLAMA_MODEL"), vision), ProviderOllama, true
}

// FromEnv is Select using the process environment and a real TCP/HTTP probe
// of the default local Ollama address.
func FromEnv() (Client, string, bool) {
	return Select(os.Getenv, ProbeOllama)
}

// ProbeOllama reports whether an Ollama server answers at host within a
// short timeout.
func ProbeOllama(host string) bool {
	hc := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := hc.Get(normalizeHost(host) + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "http://" + h
	}
	return strings.TrimRight(h, "/")
}
