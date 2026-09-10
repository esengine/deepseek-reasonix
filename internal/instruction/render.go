package instruction

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Block renders resolved standing instructions in broad-to-specific order.
// Diagnostics stay host-side; malformed imports are already annotated at the
// exact source line and must not become additional model instructions.
func Block(documents []Document) string {
	if len(documents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Instructions\n\n")
	b.WriteString("Standing guidance resolved for this workspace and target path. Later entries are more specific and take precedence when rules conflict; the current user request still has highest priority. " +
		"Source labels such as user/AGENTS.md are stable provider-relative provenance labels, not host filesystem paths. When asked for an exact path, resolve and call the read-only instruction_sources capability; do not infer a path from another agent application.\n")
	workspaceRoot := providerWorkspaceRoot(documents)
	for _, doc := range documents {
		fmt.Fprintf(&b, "\n## %s (%s", providerDocumentPath(doc, workspaceRoot), doc.Scope)
		if doc.Scope != ScopeUser && strings.TrimSpace(doc.Directory) != "" {
			fmt.Fprintf(&b, ", applies to %s", providerDirectoryPath(doc.Directory, workspaceRoot))
		}
		b.WriteString(")\n\n")
		b.WriteString(strings.TrimSpace(doc.Body))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func providerWorkspaceRoot(documents []Document) string {
	for _, doc := range documents {
		if doc.Scope == ScopeUser || doc.Depth < 0 || strings.TrimSpace(doc.Directory) == "" {
			continue
		}
		root := absolutePath(doc.Directory)
		for range doc.Depth {
			root = filepath.Dir(root)
		}
		return root
	}
	return ""
}

func providerDocumentPath(doc Document, workspaceRoot string) string {
	if doc.Scope == ScopeUser {
		return filepath.ToSlash(filepath.Join("user", filepath.Base(doc.Path)))
	}
	if workspaceRoot != "" {
		if rel, err := filepath.Rel(workspaceRoot, absolutePath(doc.Path)); err == nil && filepath.IsLocal(rel) {
			return filepath.ToSlash(filepath.Join("workspace", rel))
		}
	}
	label := string(doc.Scope)
	if label == "" {
		label = "workspace"
	}
	return filepath.ToSlash(filepath.Join(label, filepath.Base(doc.Path)))
}

func providerDirectoryPath(dir, workspaceRoot string) string {
	if workspaceRoot != "" {
		if rel, err := filepath.Rel(workspaceRoot, absolutePath(dir)); err == nil && filepath.IsLocal(rel) {
			if rel == "." {
				return "workspace"
			}
			return filepath.ToSlash(filepath.Join("workspace", rel))
		}
	}
	return "workspace"
}

func Compose(base string, documents []Document) string {
	block := Block(documents)
	if block == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return block
	}
	return strings.TrimRight(base, "\n") + "\n\n" + block
}
