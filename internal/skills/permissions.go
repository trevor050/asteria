package skills

import "sort"

// Permission strings are part of the skill JSON format.
// We keep them as strings so skills can be authored without recompiling.
const (
	PermFilesRead     = "files.read"     // read input files
	PermFilesWrite    = "files.write"    // write output files
	PermFilesTemp     = "files.temp"     // create temp files
	PermFilesAnywhere = "files.anywhere" // access outside input/output/temp
	PermNetwork       = "network"        // network access
	PermToolsExec     = "tools.exec"     // run managed tools (ffmpeg, magick, ...)
	PermToolsExecAny  = "tools.exec.any" // run arbitrary executables
	PermSystem        = "system"         // system/environment access
)

var basePermissions = map[string]bool{
	PermFilesRead:  true,
	PermFilesWrite: true,
	PermFilesTemp:  true,
	PermToolsExec:  true,
}

var elevatedPermissions = map[string]bool{
	PermFilesAnywhere: true,
	PermNetwork:       true,
	PermToolsExecAny:  true,
	PermSystem:        true,
}

func NormalizePermissions(perms []string) []string {
	if len(perms) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(perms))
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func ElevatedPermissions(perms []string) []string {
	perms = NormalizePermissions(perms)
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if elevatedPermissions[p] {
			out = append(out, p)
		}
	}
	return out
}

func IsBasePermission(p string) bool {
	return basePermissions[p]
}

func IsElevatedPermission(p string) bool {
	return elevatedPermissions[p]
}

func (s Skill) RequiresTrust() bool {
	return len(ElevatedPermissions(s.Permissions)) > 0
}
