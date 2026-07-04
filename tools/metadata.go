package tools

import (
	"encoding/json"
	"fmt"

	"github.com/deepteams/gage"
)

func (t *readFileTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Filesystem: true, Tags: []string{"filesystem", "read"}}
}

func (t *readFileTool) DescribeCall(input json.RawMessage) string {
	return pathSummary("read_file", input)
}

func (t *writeFileTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{Filesystem: true, Destructive: true, RequiresApproval: true, Tags: []string{"filesystem", "write"}}
}

func (t *writeFileTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal(input, &args) == nil && args.Path != "" {
		return fmt.Sprintf("write_file %q (%d bytes)", args.Path, len(args.Content))
	}
	return ""
}

func (t *editTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{Filesystem: true, Destructive: true, RequiresApproval: true, Tags: []string{"filesystem", "edit"}}
}

func (t *editTool) DescribeCall(input json.RawMessage) string {
	return pathSummary("edit", input)
}

func (t *listDirTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Filesystem: true, Tags: []string{"filesystem", "read"}}
}

func (t *listDirTool) DescribeCall(input json.RawMessage) string {
	return pathSummary("list_dir", input)
}

func (t *grepTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Filesystem: true, Tags: []string{"filesystem", "search"}}
}

func (t *grepTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(input, &args) == nil {
		if args.Path == "" {
			args.Path = "."
		}
		return fmt.Sprintf("grep %q in %q", args.Pattern, args.Path)
	}
	return ""
}

func (t *globTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Filesystem: true, Tags: []string{"filesystem", "search"}}
}

func (t *globTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(input, &args) == nil {
		if args.Path == "" {
			args.Path = "."
		}
		return fmt.Sprintf("glob %q in %q", args.Pattern, args.Path)
	}
	return ""
}

func (t *bashTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{Shell: true, Destructive: true, LongRunning: true, RequiresApproval: true, Tags: []string{"shell"}}
}

func (t *bashTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &args) == nil && args.Command != "" {
		return fmt.Sprintf("bash %q", args.Command)
	}
	return ""
}

func (t *webFetchTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Network: true, Tags: []string{"network", "fetch"}}
}

func (t *webFetchTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(input, &args) == nil && args.URL != "" {
		return fmt.Sprintf("web_fetch %q", args.URL)
	}
	return ""
}

func (t *webSearchTool) Metadata() gage.ToolMetadata {
	return gage.ToolMetadata{ReadOnly: true, Network: true, Tags: []string{"network", "search"}}
}

func (t *webSearchTool) DescribeCall(input json.RawMessage) string {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if json.Unmarshal(input, &args) == nil && args.Query != "" {
		if args.Limit > 0 {
			return fmt.Sprintf("web_search %q (limit %d)", args.Query, args.Limit)
		}
		return fmt.Sprintf("web_search %q", args.Query)
	}
	return ""
}

func pathSummary(name string, input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &args) == nil && args.Path != "" {
		return fmt.Sprintf("%s %q", name, args.Path)
	}
	return ""
}
