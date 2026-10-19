package rhunext

import (
	"runtime"
	"strings"
)

// SnippetPathStyle selects lexical path semantics without filesystem access.
type SnippetPathStyle uint8

const (
	SnippetPathNative SnippetPathStyle = iota
	SnippetPathPOSIX
	SnippetPathWindows
	MaxSnippetPathBytes      = 128 << 10
	MaxSnippetPathComponents = 8192
)

type snippetPath struct {
	root  string
	parts []string
}

func snippetPathStyle(style SnippetPathStyle) (SnippetPathStyle, error) {
	if style == SnippetPathNative {
		style = SnippetPathPOSIX
		if runtime.GOOS == "windows" {
			style = SnippetPathWindows
		}
	}
	if style != SnippetPathPOSIX && style != SnippetPathWindows {
		return 0, expansionError(-1, "invalid path style")
	}
	return style, nil
}

func parseSnippetPath(s string, style SnippetPathStyle) (*snippetPath, error) {
	if s == "" {
		return nil, nil
	}
	if len(s) > MaxSnippetPathBytes || strings.ContainsRune(s, 0) {
		return nil, expansionError(-1, "invalid or oversized snippet path")
	}
	if style == SnippetPathWindows {
		s = strings.ReplaceAll(s, "\\", "/")
	}
	result := &snippetPath{}
	components := 0
	switch {
	case style == SnippetPathPOSIX && strings.HasPrefix(s, "/"):
		result.root = "/"
		s = s[1:]
	case style == SnippetPathWindows && len(s) >= 3 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') && s[1] == ':' && s[2] == '/':
		result.root = s[:3]
		s = s[3:]
	case style == SnippetPathWindows && strings.HasPrefix(s, "//"):
		s = s[2:]
		server, rest, ok := strings.Cut(s, "/")
		if !ok || server == "" || server == "." || server == ".." || server == "?" {
			return nil, expansionError(-1, "invalid UNC root")
		}
		share, rest, _ := strings.Cut(rest, "/")
		if share == "" || share == "." || share == ".." {
			return nil, expansionError(-1, "invalid UNC share")
		}
		result.root = "//" + server + "/" + share + "/"
		s = rest
		components = 2
	default:
		return nil, expansionError(-1, "snippet paths must be absolute")
	}
	for s != "" {
		part, rest, _ := strings.Cut(s, "/")
		s = rest
		if part == "" {
			continue
		}
		components++
		if components > MaxSnippetPathComponents {
			return nil, expansionError(-1, "too many snippet path components")
		}
		switch part {
		case ".":
		case "..":
			if len(result.parts) > 0 {
				result.parts = result.parts[:len(result.parts)-1]
			}
		default:
			result.parts = append(result.parts, part)
		}
	}
	return result, nil
}

func (p *snippetPath) name() string {
	if len(p.parts) == 0 {
		return ""
	}
	return p.parts[len(p.parts)-1]
}
func (p *snippetPath) directory() snippetPath {
	parent := *p
	if len(parent.parts) > 0 {
		parent.parts = parent.parts[:len(parent.parts)-1]
	}
	return parent
}
func (p *snippetPath) String() string { return p.root + strings.Join(p.parts, "/") }

func snippetRelativePath(file, workspace *snippetPath, style SnippetPathStyle, original string) string {
	if file == nil || workspace == nil {
		return original
	}
	equal := func(a, b string) bool { return a == b }
	if style == SnippetPathWindows {
		equal = strings.EqualFold
	}
	if !equal(file.root, workspace.root) || len(workspace.parts) > len(file.parts) {
		return original
	}
	for i, part := range workspace.parts {
		if !equal(part, file.parts[i]) {
			return original
		}
	}
	if len(file.parts) == len(workspace.parts) {
		return "."
	}
	return strings.Join(file.parts[len(workspace.parts):], "/")
}
