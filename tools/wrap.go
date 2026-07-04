package tools

import (
	"encoding/json"

	"github.com/deepteams/gage"
)

// forwarding embeds a wrapped gage.Tool and forwards the optional capability
// interfaces (gage.ToolMetadataProvider, gage.ToolCallDescriber) to it. Every
// decorator in this package embeds forwarding so a new wrapper cannot silently
// drop the optional interfaces; wrappers override Execute (and anything else
// they change) on top.
type forwarding struct {
	gage.Tool
}

func (f forwarding) Metadata() gage.ToolMetadata {
	return gage.MetadataOf(f.Tool)
}

func (f forwarding) DescribeCall(input json.RawMessage) string {
	return gage.CallSummaryOf(f.Tool, input)
}
