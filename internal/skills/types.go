package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"asteria/internal/ops"
)

// TypeGroups are shorthands usable in inputTypes. They expand at load time.
var TypeGroups = map[string][]string{
	"@image":   ops.ImageInputExts,
	"@video":   ops.VideoExts,
	"@audio":   ops.AudioExts,
	"@media":   append(append([]string{}, ops.VideoExts...), ops.AudioExts...),
	"@pdf":     {".pdf"},
	"@archive": ops.ArchiveExts,
	"@data":    ops.DataExts,
}

// FileType returns the type token used to match a file against inputTypes:
// its lower-case extension (".tar.gz" counts as one), or "folder".
func FileType(name string, isDir bool) string {
	if isDir {
		return OutputFolder
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".tar.gz") {
		return ".tar.gz"
	}
	return strings.ToLower(filepath.Ext(name))
}

// Accepts reports whether the skill can take a file of the given type.
func (s Skill) Accepts(fileType string) bool {
	for _, t := range s.InputTypes {
		if t == "*" || strings.EqualFold(t, fileType) {
			return true
		}
	}
	return false
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

func expandTypes(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, raw := range in {
		t := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case t == "":
			continue
		case t == "*" || t == OutputFolder:
			add(t)
		case strings.HasPrefix(t, "@"):
			group, ok := TypeGroups[t]
			if !ok {
				return nil, fmt.Errorf("unknown input group %q", raw)
			}
			for _, g := range group {
				add(g)
			}
		default:
			if !strings.HasPrefix(t, ".") {
				t = "." + t
			}
			add(t)
		}
	}
	return out, nil
}

func normalizeOutputType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" || t == "none" || t == OutputFolder {
		return t
	}
	if !strings.HasPrefix(t, ".") {
		t = "." + t
	}
	return t
}
