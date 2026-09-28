// Package rolefix rewrites message roles for models whose chat templates only
// accept a single leading system message and no developer role (for example
// Qwen3.x served by SGLang). Clients such as Codex send `developer` items and
// mid-conversation system items that those templates reject with a 400.
//
// For every matching request:
//   - Responses: developer/system input items before the first user item are
//     folded, in order, into `instructions` and removed from input. Later
//     developer/system items become user items prefixed with "[system] ".
//   - Chat: developer becomes system, all system messages before the first user
//     message are merged into one leading system message, and later ones become
//     user messages prefixed with "[system] ".
//
// Requests for models that match none of the configured globs are untouched.
package rolefix

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const PluginName = "rolefix"

// lateSystemPrefix marks a system/developer message that had to be demoted to
// the user role because it appeared after the conversation started.
const lateSystemPrefix = "[system] "

// Config defines the configuration for the rolefix plugin.
type Config struct {
	// Models is a list of globs matched against "provider/model" and against
	// the bare model name, case-insensitively. `*` matches any run of
	// characters (including `/`) and `?` a single character, anywhere in the
	// pattern. Examples: "redpill/qwen/*", "*qwen*", "redpill/*/qwen3.?-27b".
	Models []string `json:"models"`
}

// RoleFixPlugin rewrites message roles in PreLLMHook. It runs per attempt so
// each fallback is matched against its own provider/model.
type RoleFixPlugin struct {
	patterns []*regexp.Regexp
	logger   schemas.Logger
}

// Init compiles the configured model globs.
func Init(config Config, logger schemas.Logger) (*RoleFixPlugin, error) {
	p := &RoleFixPlugin{logger: logger}
	for _, glob := range config.Models {
		glob = strings.TrimSpace(glob)
		if glob == "" {
			continue
		}
		re, err := compileGlob(glob)
		if err != nil {
			return nil, fmt.Errorf("rolefix: invalid model glob %q: %w", glob, err)
		}
		p.patterns = append(p.patterns, re)
	}
	return p, nil
}

// compileGlob turns a glob into an anchored, case-insensitive regexp where
// `*` spans `/`.
func compileGlob(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// GetName returns the plugin name.
func (p *RoleFixPlugin) GetName() string {
	return PluginName
}

// Cleanup is a no-op.
func (p *RoleFixPlugin) Cleanup() error {
	return nil
}

// PreRequestHook is a no-op: the rewrite depends on the attempt's model, so it
// happens in PreLLMHook, which runs again for every fallback.
func (p *RoleFixPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PostLLMHook passes responses through unchanged.
func (p *RoleFixPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// PreLLMHook rewrites roles for matching models. The incoming request is never
// mutated: a rewritten shallow copy is returned so fallbacks to non-matching
// models still see the original messages.
func (p *RoleFixPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req == nil || len(p.patterns) == 0 {
		return req, nil, nil
	}
	switch {
	case req.ResponsesRequest != nil:
		r := req.ResponsesRequest
		if !p.matches(r.Provider, r.Model) {
			return req, nil, nil
		}
		var instructions *string
		if r.Params != nil {
			instructions = r.Params.Instructions
		}
		newInstructions, newInput, changed := rewriteResponsesInput(instructions, r.Input)
		if !changed {
			return req, nil, nil
		}
		rr := *r
		rr.Input = newInput
		if r.Params != nil {
			params := *r.Params
			rr.Params = &params
		} else {
			rr.Params = &schemas.ResponsesParameters{}
		}
		rr.Params.Instructions = newInstructions
		out := *req
		out.ResponsesRequest = &rr
		p.log(ctx, r.Provider, r.Model)
		return &out, nil, nil
	case req.ChatRequest != nil:
		r := req.ChatRequest
		if !p.matches(r.Provider, r.Model) {
			return req, nil, nil
		}
		newInput, changed := rewriteChatMessages(r.Input)
		if !changed {
			return req, nil, nil
		}
		cr := *r
		cr.Input = newInput
		out := *req
		out.ChatRequest = &cr
		p.log(ctx, r.Provider, r.Model)
		return &out, nil, nil
	}
	return req, nil, nil
}

func (p *RoleFixPlugin) log(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) {
	if ctx != nil {
		ctx.Log(schemas.LogLevelDebug, fmt.Sprintf("rolefix: rewrote system/developer roles for %s/%s", provider, model))
	}
}

func (p *RoleFixPlugin) matches(provider schemas.ModelProvider, model string) bool {
	if model == "" {
		return false
	}
	full := model
	if provider != "" {
		full = string(provider) + "/" + model
	}
	for _, re := range p.patterns {
		if re.MatchString(full) || re.MatchString(model) {
			return true
		}
	}
	return false
}

// rewriteResponsesInput applies the role rules to a Responses input list. It
// returns the new instructions and input, and whether anything changed.
func rewriteResponsesInput(instructions *string, input []schemas.ResponsesMessage) (*string, []schemas.ResponsesMessage, bool) {
	firstUser := len(input)
	for i := range input {
		if isResponsesMessage(&input[i]) && *input[i].Role == schemas.ResponsesInputMessageRoleUser {
			firstUser = i
			break
		}
	}

	var folded []string
	changed := false
	out := make([]schemas.ResponsesMessage, 0, len(input))
	for i := range input {
		item := input[i]
		if !isResponsesSystemLike(&item) {
			out = append(out, item)
			continue
		}
		changed = true
		text := responsesText(item.Content)
		if i < firstUser {
			if text != "" {
				folded = append(folded, text)
			}
			continue
		}
		item.Role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		item.Content = &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(lateSystemPrefix + text)}
		out = append(out, item)
	}
	if !changed {
		return instructions, input, false
	}

	if len(folded) > 0 {
		if instructions != nil && *instructions != "" {
			folded = append([]string{*instructions}, folded...)
		}
		instructions = schemas.Ptr(strings.Join(folded, "\n\n"))
	}
	return instructions, out, true
}

// rewriteChatMessages applies the role rules to a chat message list.
func rewriteChatMessages(messages []schemas.ChatMessage) ([]schemas.ChatMessage, bool) {
	// Only a single system message at index 0 is acceptable as-is.
	firstUser := len(messages)
	needsRewrite := false
	for i := range messages {
		switch messages[i].Role {
		case schemas.ChatMessageRoleUser:
			if firstUser == len(messages) {
				firstUser = i
			}
		case schemas.ChatMessageRoleDeveloper:
			needsRewrite = true
		case schemas.ChatMessageRoleSystem:
			if i != 0 {
				needsRewrite = true
			}
		}
	}
	if !needsRewrite {
		return messages, false
	}

	var leading []string
	rest := make([]schemas.ChatMessage, 0, len(messages))
	for i := range messages {
		msg := messages[i]
		if msg.Role != schemas.ChatMessageRoleSystem && msg.Role != schemas.ChatMessageRoleDeveloper {
			rest = append(rest, msg)
			continue
		}
		text := chatText(msg.Content)
		if i < firstUser {
			if text != "" {
				leading = append(leading, text)
			}
			continue
		}
		msg.Role = schemas.ChatMessageRoleUser
		msg.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(lateSystemPrefix + text)}
		rest = append(rest, msg)
	}

	out := make([]schemas.ChatMessage, 0, len(rest)+1)
	if len(leading) > 0 {
		out = append(out, schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(strings.Join(leading, "\n\n"))},
		})
	}
	return append(out, rest...), true
}

// isResponsesMessage reports whether item is a role-bearing message item.
func isResponsesMessage(item *schemas.ResponsesMessage) bool {
	if item.Role == nil {
		return false
	}
	return item.Type == nil || *item.Type == schemas.ResponsesMessageTypeMessage
}

func isResponsesSystemLike(item *schemas.ResponsesMessage) bool {
	if !isResponsesMessage(item) {
		return false
	}
	return *item.Role == schemas.ResponsesInputMessageRoleSystem || *item.Role == schemas.ResponsesInputMessageRoleDeveloper
}

func responsesText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func chatText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
