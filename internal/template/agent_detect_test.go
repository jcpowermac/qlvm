package template

import (
	"context"
	"testing"
)

func TestDetectTemplateType_Agent(t *testing.T) {
	pc := &fakePodman{
		inspectFunc: func(_ context.Context, _ string) (map[string]string, error) {
			return map[string]string{"io.qlvm.type": "pvh-initramfs"}, nil
		},
	}
	got, labels, err := detectTemplateType(context.Background(), pc, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "agent" {
		t.Errorf("expected agent, got %s", got)
	}
	if labels["io.qlvm.type"] != "pvh-initramfs" {
		t.Error("expected labels to be returned")
	}
}

func TestDetectTemplateType_Ostree(t *testing.T) {
	pc := &fakePodman{
		inspectFunc: func(_ context.Context, _ string) (map[string]string, error) {
			return map[string]string{}, nil
		},
	}
	got, _, err := detectTemplateType(context.Background(), pc, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ostree" {
		t.Errorf("expected ostree, got %s", got)
	}
}
