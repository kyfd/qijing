package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fileecosystem/internal/llm"
	"fileecosystem/internal/model"
)

func TestCloudPayloadPrivacyAndRotatingIDs(t *testing.T) {
	scan := model.Scan{ID: `C:\Users\alice\private`, EndedAt: time.Unix(2_000_000, 0), Errors: []string{`C:\secret\name.txt denied`}, Entries: []model.Entry{{ID: "stable-hash-id", RootID: `C:\Users\alice`, Path: `C:\Users\alice\tax.pdf`, Name: "tax.pdf", Extension: ".pdf", Kind: model.KindFile, Size: 1_234_567, ModTime: time.Unix(1_000_000, 0), SHA256: "deadbeef", Error: "raw private failure", Classes: []model.Class{model.ClassDormant}}}}
	first, err := BuildCloudPayload(scan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCloudPayload(scan)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestID == second.RequestID || first.SnapshotID == second.SnapshotID || first.Regions[0].ID == second.Regions[0].ID {
		t.Fatal("run identifiers are linkable")
	}
	preview, err := PreviewPayload(first)
	if err != nil {
		t.Fatal(err)
	}
	body := string(preview.Body)
	for _, forbidden := range []string{"alice", "tax.pdf", "stable-hash-id", "deadbeef", "1234567", "raw private failure", "2000000"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("payload leaked %q: %s", forbidden, body)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(preview.Body, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		if key != "schema_version" && key != "request_id" && key != "snapshot_id" && key != "overview" && key != "regions" {
			t.Errorf("unexpected top-level field %q", key)
		}
	}
}

type fakeChat struct {
	calls    int
	response llm.ChatResponse
}

func (f *fakeChat) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	f.calls++
	return f.response, nil
}

func TestOrchestratorRequiresConsent(t *testing.T) {
	payload := CloudPayload{SchemaVersion: 1, RequestID: "run", SnapshotID: "snap"}
	preview, _ := PreviewPayload(payload)
	fake := &fakeChat{response: llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}}}}
	o := Orchestrator{Client: fake}
	_, err := o.Run(context.Background(), RunOptions{Payload: payload, Preview: preview})
	if !errors.Is(err, ErrNetworkDisabled) {
		t.Fatalf("error = %v", err)
	}
	_, err = o.Run(context.Background(), RunOptions{NetworkEnabled: true, Payload: payload, Preview: preview, ConfirmedHash: "wrong"})
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("error = %v", err)
	}
	if fake.calls != 0 {
		t.Fatal("network called before consent")
	}
}

func TestOrchestratorRejectsUnknownToolAndLimitsSteps(t *testing.T) {
	payload := CloudPayload{SchemaVersion: 1, RequestID: "run", SnapshotID: "snap"}
	preview, _ := PreviewPayload(payload)
	fake := &fakeChat{response: llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "x", Function: llm.FunctionToolCall{Name: "delete_file", Arguments: `{}`}}}}}}}}
	_, err := (Orchestrator{Client: fake}).Run(context.Background(), RunOptions{NetworkEnabled: true, ConfirmedHash: preview.Hash, Preview: preview, Payload: payload})
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("error = %v", err)
	}
	fake = &fakeChat{response: llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "x", Function: llm.FunctionToolCall{Name: ToolOverview, Arguments: `{}`}}}}}}}}
	_, err = (Orchestrator{Client: fake}).Run(context.Background(), RunOptions{NetworkEnabled: true, ConfirmedHash: preview.Hash, Preview: preview, Payload: payload})
	if !errors.Is(err, ErrStepLimit) || fake.calls != MaxSteps {
		t.Fatalf("error=%v calls=%d", err, fake.calls)
	}
}

func TestCompareUnavailable(t *testing.T) {
	body, err := executeTool(CloudPayload{}, nil, llm.ToolCall{Function: llm.FunctionToolCall{Name: ToolCompareSnapshots, Arguments: `{}`}})
	if err != nil || !strings.Contains(string(body), "unavailable") {
		t.Fatalf("body=%s err=%v", body, err)
	}
}
