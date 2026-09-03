package builtin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"reasonix/internal/tool"
)

// ManagedConfigPaths is the set of Reasonix-owned configuration FILES a file
// tool may write outside the workspace roots, each write gated by a fresh
// human approval (see tool.ConfigWriteApprover). The zero value matches
// nothing, preserving plain workspace confinement. Entries are individual
// files, never directories: the Reasonix home also holds credentials (.env),
// global hooks (settings.json), skills, and session stores, which must not
// become writable through this escape hatch.
type ManagedConfigPaths struct {
	paths []string
}

// NewManagedConfigPaths resolves each candidate file to an absolute,
// symlink-free path (mirroring realRoots), dropping empty or unresolvable
// entries.
func NewManagedConfigPaths(paths []string) ManagedConfigPaths {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if real, err := realPath(p); err == nil {
			out = append(out, real)
		}
	}
	return ManagedConfigPaths{paths: out}
}

// Match reports whether target resolves to exactly one of the managed config
// files. Exact file equality with no case folding: this is an allow-side rule,
// and folding an allow rule on a case-sensitive filesystem would wave a
// genuinely different file through (see withinFold).
func (m ManagedConfigPaths) Match(target string) bool {
	if len(m.paths) == 0 {
		return false
	}
	abs, err := realPath(target)
	if err != nil {
		return false
	}
	return slices.Contains(m.paths, abs)
}

// approve asks the user whether this managed-config write may proceed, via the
// approver carried on ctx. No approver — a headless run, or a sub-agent whose
// parent has no interactive frontend — fails closed. The error text is written
// for the model: it names the boundary and the durable ways forward.
func (m ManagedConfigPaths) approve(ctx context.Context, target string) error {
	approver, ok := tool.ConfigWriteApproverFrom(ctx)
	if !ok {
		return fmt.Errorf("path %q is a Reasonix-managed config file; writing it requires interactive user approval, which this session cannot provide. "+
			"Ask the user to retry in an interactive session; widening [sandbox] allow_write does not authorize Reasonix config edits", target)
	}
	req := tool.ConfigWriteRequest{Path: target}
	if checker, ok := approver.(tool.ConfigWriteSessionChecker); ok && checker.ManagedConfigWriteSessionAllowed(ctx, req) {
		return nil
	}
	allow, reason, err := approver.ApproveManagedConfigWrite(ctx, req)
	if err != nil {
		return err
	}
	if !allow {
		if strings.TrimSpace(reason) == "" {
			reason = "the user declined this Reasonix config write — do not retry it; ask how they would like to proceed."
		}
		return errors.New(reason)
	}
	return nil
}
