package config

import "testing"

func TestDefaultPromptFunctions(t *testing.T) {
	if defaultSystemPrompt() == "" {
		t.Fatal("expected default system prompt")
	}
	if defaultApproverPrompt() == "" {
		t.Fatal("expected default approver prompt")
	}
}
