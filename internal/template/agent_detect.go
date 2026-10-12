package template

import (
	"context"
	"fmt"
)

const agentLabel = "io.qlvm.type"
const agentLabelValue = "pvh-initramfs"

// detectTemplateType inspects the container image and returns "agent" if
// it has the io.qlvm.type=pvh-initramfs label, "ostree" otherwise.
func detectTemplateType(ctx context.Context, pc Podman, ref string) (string, error) {
	labels, err := pc.InspectImage(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if labels != nil && labels[agentLabel] == agentLabelValue {
		return "agent", nil
	}
	return "ostree", nil
}
