package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type commandSet struct {
	commands map[string]customCommand
}

type customCommand struct {
	Name        string
	Description string
	Template    string
	Mode        agentMode
	Source      string
}

type commandFrontmatter struct {
	Description string `yaml:"description"`
	Mode        string `yaml:"mode"`
}

func loadCommands(root string, inline map[string]inlineCommand) (*commandSet, error) {
	set := &commandSet{commands: map[string]customCommand{}}
	for name, cmd := range inline {
		mode, err := parseMode(cmd.Mode)
		if err != nil {
			return nil, fmt.Errorf("command %s: %w", name, err)
		}
		set.commands[name] = customCommand{
			Name:        name,
			Description: cmd.Description,
			Template:    cmd.Template,
			Mode:        mode,
			Source:      "config",
		}
	}

	files, err := filepath.Glob(filepath.Join(root, ".agents", "commands", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, file := range files {
		cmd, err := loadCommandFile(root, file)
		if err != nil {
			return nil, err
		}
		set.commands[cmd.Name] = cmd
	}
	return set, nil
}

func loadCommandFile(root, path string) (customCommand, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return customCommand{}, err
	}
	body := strings.TrimSpace(string(raw))
	var fm commandFrontmatter
	if strings.HasPrefix(body, "---\n") {
		rest := body[len("---\n"):]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			if err := yaml.Unmarshal([]byte(rest[:idx]), &fm); err != nil {
				return customCommand{}, fmt.Errorf("%s: %w", path, err)
			}
			body = strings.TrimSpace(rest[idx+len("\n---"):])
		}
	}
	mode, err := parseMode(fm.Mode)
	if err != nil {
		return customCommand{}, fmt.Errorf("%s: %w", path, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return customCommand{
		Name:        name,
		Description: fm.Description,
		Template:    body,
		Mode:        mode,
		Source:      relDisplay(root, path),
	}, nil
}

func (s *commandSet) Get(name string) (customCommand, bool) {
	if s == nil {
		return customCommand{}, false
	}
	cmd, ok := s.commands[name]
	return cmd, ok
}

func (s *commandSet) List() []customCommand {
	if s == nil {
		return nil
	}
	out := make([]customCommand, 0, len(s.commands))
	for _, cmd := range s.commands {
		out = append(out, cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func expandCommand(cmd customCommand, args string) string {
	fields := strings.Fields(args)
	out := strings.ReplaceAll(cmd.Template, "$ARGUMENTS", args)
	for i := 1; i <= 9; i++ {
		value := ""
		if i <= len(fields) {
			value = fields[i-1]
		}
		out = strings.ReplaceAll(out, fmt.Sprintf("$%d", i), value)
	}
	return strings.TrimSpace(out)
}
