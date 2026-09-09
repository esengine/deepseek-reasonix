package main

import (
	"path/filepath"
	"strings"
)

// windowsVisualStudioDevenvCandidates converts vswhere installationPath output
// into ordered devenv.exe paths. vswhere -latest normally returns one line, but
// accepting multiple non-empty lines keeps the parser resilient to wrappers and
// makes the first valid installation win deterministically.
func windowsVisualStudioDevenvCandidates(vswhereOutput string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(vswhereOutput, "\r\n", "\n"), "\n") {
		installPath := strings.TrimPrefix(strings.TrimSpace(line), "\ufeff")
		installPath = strings.Trim(strings.TrimSpace(installPath), `"`)
		if installPath == "" {
			continue
		}
		out = append(out, filepath.Join(installPath, "Common7", "IDE", "devenv.exe"))
	}
	return out
}

// windowsVisualStudioFallbackCandidatePaths returns standard Visual Studio
// install globs in newest-version-first order. Visual Studio 2022 (17.x) and
// 2026 (18.x) are 64-bit applications, but the x86 root remains a compatibility
// fallback for customized or upgraded installations.
func windowsVisualStudioFallbackCandidatePaths(programFiles, programFilesX86 string) []string {
	var roots []string
	for _, root := range []string{programFiles, programFilesX86} {
		root = strings.TrimSpace(root)
		if root != "" {
			roots = append(roots, root)
		}
	}
	var out []string
	for _, version := range []string{"2026", "2022"} {
		for _, root := range roots {
			out = append(out, filepath.Join(root, "Microsoft Visual Studio", version, "*", "Common7", "IDE", "devenv.exe"))
		}
	}
	return out
}

// windowsTerminalIconCandidatePaths returns ordered icon lookup paths for
// Windows Terminal. Store installs register only a wt.exe App Execution Alias
// under LocalAppData\Microsoft\WindowsApps; the real WindowsTerminal.exe lives
// under Program Files\WindowsApps\Microsoft.WindowsTerminal_*.
//
// Non-elevated processes often cannot glob the protected WindowsApps package
// directory, so callers must treat a missing package hit as normal and apply
// pickWindowsTerminalIconSource (renderable fallback before zero-byte aliases).
//
// Patterns may contain filepath globs; callers expand them on the host.
func windowsTerminalIconCandidatePaths(wtPath, localAppData, programFiles string) []string {
	var out []string
	if programFiles = strings.TrimSpace(programFiles); programFiles != "" {
		out = append(out,
			filepath.Join(programFiles, "WindowsApps", "Microsoft.WindowsTerminal_*", "WindowsTerminal.exe"),
			filepath.Join(programFiles, "WindowsApps", "Microsoft.WindowsTerminalPreview_*", "WindowsTerminal.exe"),
		)
	}
	// Legacy/portable layouts occasionally nest the package under LocalAppData.
	if localAppData = strings.TrimSpace(localAppData); localAppData != "" {
		out = append(out,
			filepath.Join(localAppData, "Microsoft", "WindowsApps", "Microsoft.WindowsTerminal_*", "WindowsTerminal.exe"),
			filepath.Join(localAppData, "Microsoft", "WindowsApps", "Microsoft.WindowsTerminalPreview_*", "WindowsTerminal.exe"),
		)
	}
	if wtPath = strings.TrimSpace(wtPath); wtPath != "" {
		out = append(out, wtPath)
	}
	return out
}

// windowsIconCandidate is a filesystem path considered for SHGetFileInfo, with
// its observed size. Size 0 is typical for App Execution Alias reparse points.
type windowsIconCandidate struct {
	Path string
	Size int64
}

// pickWindowsTerminalIconSource chooses an IconSource path for the WT menu row.
// Order:
//  1. first non-zero-size package/binary path (real WindowsTerminal.exe)
//  2. renderableFallback (e.g. powershell.exe) — must beat zero-byte aliases so
//     SHGetFileInfo is not pointed at an empty stub that yields a blank glyph
//  3. zero-size alias / last-known path only when nothing else is available
func pickWindowsTerminalIconSource(resolved []windowsIconCandidate, renderableFallback string) string {
	for _, c := range resolved {
		if strings.TrimSpace(c.Path) == "" || c.Size <= 0 {
			continue
		}
		return c.Path
	}
	if fb := strings.TrimSpace(renderableFallback); fb != "" {
		return fb
	}
	for _, c := range resolved {
		if path := strings.TrimSpace(c.Path); path != "" {
			return path
		}
	}
	return ""
}

// windowsConsoleLaunch describes a console opener launch that never goes
// through cmd.exe / start (which re-parse working directories as shell text).
type windowsConsoleLaunch struct {
	File string // absolute or PATH-resolved executable
	Dir  string // working directory passed to ShellExecute lpDirectory
}

// planWindowsConsoleLaunch builds a shell-free console launch. File is opened
// with ShellExecute("open") and Dir is the process working directory — paths
// with spaces or shell metacharacters (& | ( ) ^ %) are never concatenated into
// a cmd.exe command line.
func planWindowsConsoleLaunch(target, workdir string) windowsConsoleLaunch {
	return windowsConsoleLaunch{
		File: strings.TrimSpace(target),
		Dir:  workdir,
	}
}

// windowsConsoleLaunchIsDirect reports that the plan opens the target binary
// itself (via ShellExecute) rather than a cmd.exe /c start wrapper. Command
// Prompt as a terminal is still direct: File is cmd.exe and Dir is the
// workspace — there is no intermediate shell command line.
func windowsConsoleLaunchIsDirect(plan windowsConsoleLaunch) bool {
	if strings.TrimSpace(plan.File) == "" {
		return false
	}
	// The plan struct has no Args/CommandLine field by design; a regression that
	// reintroduces cmd /c start would need extra fields or a different shape.
	return true
}
