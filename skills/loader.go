package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatter is the YAML header of a SKILL.md file.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// LoadDir loads every skill under root. A skill is any immediate subdirectory
// containing a SKILL.md, and root itself if it holds one. Skills that fail to
// parse are skipped with an aggregated error returned alongside the valid ones.
func LoadDir(root string) (*Set, error) {
	set := NewSet()
	var errs []string

	// root itself as a skill.
	if fileExists(skillPath(root)) {
		if sk, err := LoadSkill(root); err != nil {
			errs = append(errs, err.Error())
		} else {
			set.add(sk)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return set, fmt.Errorf("skills: read %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !fileExists(skillPath(dir)) {
			continue
		}
		sk, err := LoadSkill(dir)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		set.add(sk)
	}
	if len(errs) > 0 {
		return set, fmt.Errorf("skills: %s", strings.Join(errs, "; "))
	}
	return set, nil
}

// LoadSkill loads the skill in dir (which must contain a SKILL.md).
func LoadSkill(dir string) (*Skill, error) {
	path := skillPath(dir)
	_, fm, err := readSkillFile(path)
	if err != nil {
		return nil, err
	}
	name := fm.Name
	if name == "" {
		name = filepath.Base(dir)
	}
	return &Skill{
		Name:        name,
		Description: fm.Description,
		Dir:         dir,
		bodyPath:    path,
	}, nil
}

// readSkillFile parses a SKILL.md into its body and frontmatter. Frontmatter is
// an optional block delimited by lines of "---" at the top of the file.
func readSkillFile(path string) (body string, fm frontmatter, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", frontmatter{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
	content := string(data)
	fmText, body := splitFrontmatter(content)
	if fmText != "" {
		if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
			return "", frontmatter{}, fmt.Errorf("skills: parse frontmatter %s: %w", path, err)
		}
	}
	return body, fm, nil
}

// splitFrontmatter separates a leading --- ... --- YAML block from the body.
func splitFrontmatter(content string) (fm, body string) {
	trimmed := strings.TrimPrefix(content, "\ufeff") // strip BOM
	if !strings.HasPrefix(trimmed, "---") {
		return "", content
	}
	// Find the closing delimiter after the opening one.
	rest := trimmed[3:]
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", content
	}
	fm = rest[:idx]
	body = rest[idx+len("\n---"):]
	// Drop the remainder of the closing delimiter line.
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return fm, strings.TrimLeft(body, "\r\n")
}
