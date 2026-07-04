// Package skills loads Claude Code-style SKILL.md skill folders and exposes
// them to an agent: their name+description are advertised in the system prompt,
// and a "skill" tool loads a skill's full body on demand.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

// Skill is a named, described capability whose instructions live in a SKILL.md
// body loaded lazily from disk.
type Skill struct {
	// Name identifies the skill (from frontmatter, falling back to the folder).
	Name string
	// Description is the one-line summary advertised to the model.
	Description string
	// Dir is the skill's folder (holds SKILL.md and any bundled files).
	Dir string
	// bodyPath is the SKILL.md path; body is loaded on demand.
	bodyPath string
}

// Body returns the markdown instructions of the skill (the SKILL.md content
// after the frontmatter), read from disk on each call.
func (s *Skill) Body() (string, error) {
	body, _, err := readSkillFile(s.bodyPath)
	if err != nil {
		return "", err
	}
	return body, nil
}

// Set is a collection of skills keyed by name.
type Set struct {
	skills map[string]*Skill
	order  []string
}

// NewSet builds a Set from the given skills.
func NewSet(skills ...*Skill) *Set {
	s := &Set{skills: map[string]*Skill{}}
	for _, sk := range skills {
		s.add(sk)
	}
	return s
}

func (s *Set) add(sk *Skill) {
	if _, ok := s.skills[sk.Name]; !ok {
		s.order = append(s.order, sk.Name)
	}
	s.skills[sk.Name] = sk
}

// Get returns a skill by name.
func (s *Set) Get(name string) (*Skill, bool) {
	sk, ok := s.skills[name]
	return sk, ok
}

// List returns the skills in load order.
func (s *Set) List() []*Skill {
	out := make([]*Skill, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.skills[name])
	}
	return out
}

// Len reports the number of skills.
func (s *Set) Len() int { return len(s.order) }

// SystemPrompt renders the advertised name+description list for injection into
// an agent's system prompt. It returns an empty string for an empty set.
func (s *Set) SystemPrompt() string {
	if s.Len() == 0 {
		return ""
	}
	out := "You have access to the following skills. Use the \"skill\" tool with a skill's name to load its full instructions before applying it.\n"
	for _, sk := range s.List() {
		out += fmt.Sprintf("- %s: %s\n", sk.Name, sk.Description)
	}
	return out
}

// skillFileName is the conventional skill definition file.
const skillFileName = "SKILL.md"

func skillPath(dir string) string { return filepath.Join(dir, skillFileName) }

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
