package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kyfd/qijing/internal/llm"
)

const MaxSteps = 5

const (
	ToolOverview         = "overview"
	ToolListRegions      = "list_regions"
	ToolRegionDetails    = "get_region_details"
	ToolCompareSnapshots = "compare_snapshots"
	ToolRuleExplanation  = "get_rule_explanation"
)

var (
	ErrNetworkDisabled = errors.New("model networking is disabled")
	ErrNotConfirmed    = errors.New("agent payload is not confirmed")
	ErrUnknownTool     = errors.New("unknown or forbidden tool")
	ErrStepLimit       = errors.New("agent step limit reached")
)

type ChatClient interface {
	Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error)
}

type RunOptions struct {
	NetworkEnabled bool
	ConfirmedHash  string
	Preview        PayloadPreview
	Payload        CloudPayload
	Previous       *CloudPayload
}

type RunResult struct {
	Final       llm.Message `json:"final"`
	Steps       int         `json:"steps"`
	ToolsCalled []string    `json:"tools_called"`
}

type Orchestrator struct{ Client ChatClient }

func (o Orchestrator) Run(ctx context.Context, opts RunOptions) (RunResult, error) {
	if !opts.NetworkEnabled {
		return RunResult{}, ErrNetworkDisabled
	}
	actual, err := PreviewPayload(opts.Payload)
	if err != nil {
		return RunResult{}, err
	}
	if opts.ConfirmedHash == "" || opts.ConfirmedHash != opts.Preview.Hash || opts.Preview.Hash != actual.Hash || string(opts.Preview.Body) != string(actual.Body) {
		return RunResult{}, ErrNotConfirmed
	}
	messages := []llm.Message{{Role: "system", Content: "你是「栖境」的只读文件生态观察员。只用已提供的聚合工具。禁止索要路径、文件名、正文、哈希、shell、SQL、URL 或任何文件操作。最多五步。最终报告必须用简体中文，像野外手记：先给一句结论，再写规模、风险与建议。不要把匿名 region_id 当标题，改用「休眠区」「巨物集中区」这类可读称呼。可用简单 Markdown（标题、粗体、列表）。不要建议删除文件，只建议用户自行在资源管理器中复核。"}, {Role: "user", Content: string(actual.Body)}}
	result := RunResult{}
	for step := 0; step < MaxSteps; step++ {
		response, err := o.Client.Chat(ctx, llm.ChatRequest{Messages: messages, Tools: toolDefinitions()})
		if err != nil {
			return result, err
		}
		if len(response.Choices) == 0 {
			return result, errors.New("model returned no choices")
		}
		message := response.Choices[0].Message
		result.Steps++
		messages = append(messages, message)
		if len(message.ToolCalls) == 0 {
			result.Final = message
			return result, nil
		}
		for _, call := range message.ToolCalls {
			value, err := executeTool(opts.Payload, opts.Previous, call)
			if err != nil {
				return result, err
			}
			result.ToolsCalled = append(result.ToolsCalled, call.Function.Name)
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: string(value)})
		}
	}
	return result, ErrStepLimit
}

func executeTool(payload CloudPayload, previous *CloudPayload, call llm.ToolCall) ([]byte, error) {
	var args map[string]json.RawMessage
	if call.Function.Arguments != "" && call.Function.Arguments != "{}" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("invalid tool arguments: %w", err)
		}
	}
	allowed := func(names ...string) bool {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		for key := range args {
			if !set[key] {
				return false
			}
		}
		return true
	}
	var value any
	switch call.Function.Name {
	case ToolOverview:
		if !allowed() {
			return nil, ErrUnknownTool
		}
		value = payload.Overview
	case ToolListRegions:
		if !allowed() {
			return nil, ErrUnknownTool
		}
		value = payload.Regions
	case ToolRegionDetails:
		if !allowed("region_id") {
			return nil, ErrUnknownTool
		}
		var id string
		if err := json.Unmarshal(args["region_id"], &id); err != nil || id == "" {
			return nil, errors.New("region_id is required")
		}
		value = map[string]any{"status": "unavailable"}
		for _, region := range payload.Regions {
			if region.ID == id {
				value = region
				break
			}
		}
	case ToolCompareSnapshots:
		if !allowed() {
			return nil, ErrUnknownTool
		}
		if previous == nil {
			value = map[string]string{"status": "unavailable", "reason": "previous snapshot not supplied"}
		} else {
			value = compare(*previous, payload)
		}
	case ToolRuleExplanation:
		if !allowed("rule") {
			return nil, ErrUnknownTool
		}
		var rule string
		_ = json.Unmarshal(args["rule"], &rule)
		explanations := map[string]string{"orphan": "An item lacks observed ecosystem relationships.", "dormant": "An item falls into a long-inactive age bucket.", "giant": "An item falls into a high size class.", "rotten": "Local rules identified multiple decay signals."}
		text, ok := explanations[rule]
		if !ok {
			value = map[string]string{"status": "unavailable"}
		} else {
			value = map[string]string{"rule": rule, "explanation": text}
		}
	default:
		return nil, ErrUnknownTool
	}
	return json.Marshal(value)
}

func compare(before, after CloudPayload) any {
	return map[string]any{"status": "available", "before": before.Overview, "after": after.Overview}
}

func toolDefinitions() []llm.Tool {
	schema := func(properties string) json.RawMessage { return json.RawMessage(properties) }
	return []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: ToolOverview, Description: "Get aggregate ecosystem overview", Parameters: schema(`{"type":"object","properties":{},"additionalProperties":false}`)}},
		{Type: "function", Function: llm.ToolFunction{Name: ToolListRegions, Description: "List anonymous aggregate regions", Parameters: schema(`{"type":"object","properties":{},"additionalProperties":false}`)}},
		{Type: "function", Function: llm.ToolFunction{Name: ToolRegionDetails, Description: "Get one anonymous region", Parameters: schema(`{"type":"object","properties":{"region_id":{"type":"string"}},"required":["region_id"],"additionalProperties":false}`)}},
		{Type: "function", Function: llm.ToolFunction{Name: ToolCompareSnapshots, Description: "Compare supplied aggregate snapshots", Parameters: schema(`{"type":"object","properties":{},"additionalProperties":false}`)}},
		{Type: "function", Function: llm.ToolFunction{Name: ToolRuleExplanation, Description: "Explain a local rule", Parameters: schema(`{"type":"object","properties":{"rule":{"type":"string"}},"required":["rule"],"additionalProperties":false}`)}},
	}
}
