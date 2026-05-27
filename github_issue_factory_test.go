package main

import (
	"encoding/json"
	"strings"
	"testing"
)

type issueFactoryEvent struct {
	Issue struct {
		AuthorAssociation string `json:"author_association"`
	} `json:"issue"`
}

func TestIssueFactoryAuthorAssociationGate(t *testing.T) {
	t.Parallel()

	workflow := readText(t, ".github/workflows/codex-issue-factory.yml")
	localWorkflow := readText(t, "examples/github-issue-factory/local-issue-factory.yml")
	meloniteWorkflow := readText(t, ".github/workflows/melonite.yml")
	allowed := "OWNER,MEMBER,COLLABORATOR"

	if !strings.Contains(workflow, "default: "+allowed) {
		t.Fatalf("reusable workflow should default the issue factory gate to %s", allowed)
	}
	if !strings.Contains(localWorkflow, "allowed-author-associations: "+allowed) {
		t.Fatalf("local act workflow should gate issue factory runs to %s", allowed)
	}
	if !strings.Contains(meloniteWorkflow, "allowed-author-associations: "+allowed) {
		t.Fatalf("melonite workflow should gate issue factory runs to %s", allowed)
	}

	for name, content := range map[string]string{
		"reusable workflow":  workflow,
		"local act workflow": localWorkflow,
		"melonite workflow":  meloniteWorkflow,
	} {
		if strings.Contains(content, "OWNER,MEMBER,COLLABORATOR,CONTRIBUTOR") {
			t.Fatalf("%s must not allow CONTRIBUTOR by default", name)
		}
	}

	tests := []struct {
		name    string
		fixture string
		want    bool
	}{
		{
			name:    "collaborator is trusted",
			fixture: "examples/github-issue-factory/issue-event-collaborator.json",
			want:    true,
		},
		{
			name:    "contributor is not trusted",
			fixture: "examples/github-issue-factory/issue-event-contributor.json",
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var event issueFactoryEvent
			if err := json.Unmarshal(readBytes(t, tt.fixture), &event); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}

			if got := authorAssociationAllowed(event.Issue.AuthorAssociation, allowed); got != tt.want {
				t.Fatalf("authorAssociationAllowed(%q) = %v, want %v", event.Issue.AuthorAssociation, got, tt.want)
			}
		})
	}
}

func authorAssociationAllowed(association, allowedAssociations string) bool {
	association = strings.ToUpper(strings.TrimSpace(association))
	for _, value := range strings.Split(allowedAssociations, ",") {
		candidate := strings.ToUpper(strings.TrimSpace(value))
		if association == candidate {
			return true
		}
	}

	return false
}
