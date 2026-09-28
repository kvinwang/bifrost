package rolefix

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func respMsg(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(role),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr(text)},
		}},
	}
}

func chatMsg(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
}

func newPlugin(t *testing.T, models ...string) *RoleFixPlugin {
	t.Helper()
	p, err := Init(Config{Models: models}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatches(t *testing.T) {
	p := newPlugin(t, "redpill/qwen/*", "redpill/phala/qwen*", "gpu19/*")
	cases := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{"redpill", "qwen/qwen3.8-27b", true},
		{"redpill", "phala/qwen3.8-27b-uncensored", true},
		{"redpill", "phala/gemma-4-26b-a4b-uncensored", false},
		{"redpill", "z-ai/glm-5.3", false},
		{"gpu19", "some-model", true},
		{"gpu19", "org/some-model", true},
		{"", "redpill/qwen/qwen3.5-27b", true}, // unresolved provider
		{"openai", "gpt-5", false},
	}
	for _, c := range cases {
		if got := p.matches(c.provider, c.model); got != c.want {
			t.Errorf("matches(%q, %q) = %v, want %v", c.provider, c.model, got, c.want)
		}
	}
}

func TestPreLLMHookResponsesFoldsLeadingAndDemotesLate(t *testing.T) {
	p := newPlugin(t, "redpill/qwen/*")
	input := []schemas.ResponsesMessage{
		respMsg(schemas.ResponsesInputMessageRoleDeveloper, "perms"),
		respMsg(schemas.ResponsesInputMessageRoleDeveloper, "env"),
		respMsg(schemas.ResponsesInputMessageRoleUser, "hi"),
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall)},
		respMsg(schemas.ResponsesInputMessageRoleSystem, "late"),
	}
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "redpill",
			Model:    "qwen/qwen3.8-27b",
			Input:    input,
			Params:   &schemas.ResponsesParameters{Instructions: schemas.Ptr("base")},
		},
	}

	out, sc, err := p.PreLLMHook(nil, req)
	if err != nil || sc != nil {
		t.Fatalf("unexpected err=%v sc=%v", err, sc)
	}
	rr := out.ResponsesRequest
	if got := *rr.Params.Instructions; got != "base\n\nperms\n\nenv" {
		t.Fatalf("instructions = %q", got)
	}
	if len(rr.Input) != 3 {
		t.Fatalf("input len = %d, want 3", len(rr.Input))
	}
	if *rr.Input[0].Role != schemas.ResponsesInputMessageRoleUser {
		t.Fatalf("first item role = %s", *rr.Input[0].Role)
	}
	late := rr.Input[2]
	if *late.Role != schemas.ResponsesInputMessageRoleUser || *late.Content.ContentStr != "[system] late" {
		t.Fatalf("late item = %s %v", *late.Role, late.Content.ContentStr)
	}

	// The original request must be untouched (fallbacks may reuse it).
	if req.ResponsesRequest.Input[0].Role == nil || *req.ResponsesRequest.Input[0].Role != schemas.ResponsesInputMessageRoleDeveloper || len(req.ResponsesRequest.Input) != 5 {
		t.Fatal("original input mutated")
	}
	if *req.ResponsesRequest.Params.Instructions != "base" {
		t.Fatal("original instructions mutated")
	}
}

func TestPreLLMHookResponsesNilParams(t *testing.T) {
	p := newPlugin(t, "gpu19/*")
	req := &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "gpu19",
			Model:    "m",
			Input: []schemas.ResponsesMessage{
				respMsg(schemas.ResponsesInputMessageRoleDeveloper, "dev"),
				respMsg(schemas.ResponsesInputMessageRoleUser, "hi"),
			},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	if got := *out.ResponsesRequest.Params.Instructions; got != "dev" {
		t.Fatalf("instructions = %q", got)
	}
	if req.ResponsesRequest.Params != nil {
		t.Fatal("original params mutated")
	}
}

func TestPreLLMHookNonMatchingUntouched(t *testing.T) {
	p := newPlugin(t, "redpill/qwen/*")
	req := &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "redpill",
			Model:    "z-ai/glm-5.3",
			Input: []schemas.ResponsesMessage{
				respMsg(schemas.ResponsesInputMessageRoleDeveloper, "dev"),
				respMsg(schemas.ResponsesInputMessageRoleUser, "hi"),
			},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	if out != req {
		t.Fatal("non-matching request was replaced")
	}
}

func TestPreLLMHookResponsesNoSystemLikeUnchanged(t *testing.T) {
	p := newPlugin(t, "redpill/qwen/*")
	req := &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "redpill",
			Model:    "qwen/qwen3.8-27b",
			Input:    []schemas.ResponsesMessage{respMsg(schemas.ResponsesInputMessageRoleUser, "hi")},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	if out != req {
		t.Fatal("request without system/developer items was replaced")
	}
}

func TestPreLLMHookChat(t *testing.T) {
	p := newPlugin(t, "redpill/phala/qwen*")
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "redpill",
			Model:    "phala/qwen3.8-27b-uncensored",
			Input: []schemas.ChatMessage{
				chatMsg(schemas.ChatMessageRoleSystem, "sys"),
				chatMsg(schemas.ChatMessageRoleDeveloper, "dev"),
				chatMsg(schemas.ChatMessageRoleUser, "hi"),
				chatMsg(schemas.ChatMessageRoleAssistant, "hello"),
				chatMsg(schemas.ChatMessageRoleDeveloper, "late"),
			},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	msgs := out.ChatRequest.Input
	want := []struct {
		role schemas.ChatMessageRole
		text string
	}{
		{schemas.ChatMessageRoleSystem, "sys\n\ndev"},
		{schemas.ChatMessageRoleUser, "hi"},
		{schemas.ChatMessageRoleAssistant, "hello"},
		{schemas.ChatMessageRoleUser, "[system] late"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("len = %d, want %d", len(msgs), len(want))
	}
	for i, w := range want {
		if msgs[i].Role != w.role || *msgs[i].Content.ContentStr != w.text {
			t.Errorf("msg[%d] = %s %q, want %s %q", i, msgs[i].Role, *msgs[i].Content.ContentStr, w.role, w.text)
		}
	}
	if req.ChatRequest.Input[1].Role != schemas.ChatMessageRoleDeveloper || len(req.ChatRequest.Input) != 5 {
		t.Fatal("original chat input mutated")
	}
}

func TestPreLLMHookChatAlreadyValid(t *testing.T) {
	p := newPlugin(t, "gpu19/*")
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "gpu19",
			Model:    "m",
			Input: []schemas.ChatMessage{
				chatMsg(schemas.ChatMessageRoleSystem, "sys"),
				chatMsg(schemas.ChatMessageRoleUser, "hi"),
			},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	if out != req {
		t.Fatal("valid chat request was replaced")
	}
}

// The Responses->Chat fallback prepends instructions as a system message; after
// the rewrite that must be the only system message and it must lead.
func TestResponsesRewriteSurvivesChatConversion(t *testing.T) {
	p := newPlugin(t, "gpu19/*")
	req := &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "gpu19",
			Model:    "m",
			Input: []schemas.ResponsesMessage{
				respMsg(schemas.ResponsesInputMessageRoleDeveloper, "dev"),
				respMsg(schemas.ResponsesInputMessageRoleUser, "hi"),
				respMsg(schemas.ResponsesInputMessageRoleDeveloper, "late"),
			},
			Params: &schemas.ResponsesParameters{Instructions: schemas.Ptr("base")},
		},
	}
	out, _, _ := p.PreLLMHook(nil, req)
	chat := out.ResponsesRequest.ToChatRequest()
	systems := 0
	for i, m := range chat.Input {
		if m.Role == schemas.ChatMessageRoleSystem || m.Role == schemas.ChatMessageRoleDeveloper {
			systems++
			if i != 0 {
				t.Fatalf("system message at index %d", i)
			}
		}
	}
	if systems != 1 {
		t.Fatalf("system messages = %d, want 1", systems)
	}
}

func TestInitRejectsNothingAndSkipsBlank(t *testing.T) {
	p := newPlugin(t, "", "  ")
	if len(p.patterns) != 0 {
		t.Fatalf("patterns = %d", len(p.patterns))
	}
	req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Model: "x"}}
	if out, _, _ := p.PreLLMHook(nil, req); out != req {
		t.Fatal("plugin with no patterns changed request")
	}
}
