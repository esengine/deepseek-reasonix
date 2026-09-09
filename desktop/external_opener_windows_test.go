//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindWindowsVisualStudioExecutablePrefersVSWhere(t *testing.T) {
	programFiles := filepath.Join(t.TempDir(), "Program Files")
	programFilesX86 := filepath.Join(t.TempDir(), "Program Files (x86)")
	vswhere := filepath.Join(programFilesX86, "Microsoft Visual Studio", "Installer", "vswhere.exe")
	install := filepath.Join(t.TempDir(), "Microsoft Visual Studio", "2026", "Professional")
	devenv := filepath.Join(install, "Common7", "IDE", "devenv.exe")
	for _, path := range []string{vswhere, devenv} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := findWindowsVisualStudioExecutable(programFiles, programFilesX86, func(path string) (string, error) {
		if path != vswhere {
			t.Fatalf("vswhere path = %q, want %q", path, vswhere)
		}
		return install + "\r\n", nil
	})
	if got != devenv {
		t.Fatalf("Visual Studio executable = %q, want vswhere result %q", got, devenv)
	}
}

func TestFindWindowsVisualStudioExecutableFallsBackToStandardPaths(t *testing.T) {
	programFiles := filepath.Join(t.TempDir(), "Program Files")
	programFilesX86 := filepath.Join(t.TempDir(), "Program Files (x86)")
	devenv := filepath.Join(programFiles, "Microsoft Visual Studio", "2026", "Community", "Common7", "IDE", "devenv.exe")
	if err := os.MkdirAll(filepath.Dir(devenv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devenv, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findWindowsVisualStudioExecutable(programFiles, programFilesX86, func(string) (string, error) {
		return "", errors.New("vswhere unavailable")
	})
	if got != devenv {
		t.Fatalf("Visual Studio executable = %q, want standard-path result %q", got, devenv)
	}
}

func TestWindowsExternalOpenerIconUsesShellIcon(t *testing.T) {
	explorer := filepath.Join(os.Getenv("WINDIR"), "explorer.exe")
	if info, err := os.Stat(explorer); err != nil || info.IsDir() {
		t.Skip("Windows Explorer executable is unavailable")
	}
	got := platformExternalOpenerIconDataURL(externalOpenerSpec{IconSource: explorer})
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("native Explorer icon = %q, want PNG data URL", got)
	}
}

func TestWindowsAppPathExecutableEmptyName(t *testing.T) {
	if got := windowsAppPathExecutable(""); got != "" {
		t.Fatalf("empty name = %q, want empty", got)
	}
	if got := windowsAppPathExecutable("   "); got != "" {
		t.Fatalf("blank name = %q, want empty", got)
	}
}

func TestWindowsTerminalIconSourceSkipsZeroByteAliasForPowershell(t *testing.T) {
	// When only a zero-byte execution alias is visible (package dir unreadable),
	// IconSource must not stick on the alias — PowerShell is a known renderable
	// PE that SHGetFileInfo can extract.
	dir := t.TempDir()
	alias := filepath.Join(dir, "wt.exe")
	if err := os.WriteFile(alias, nil, 0o644); err != nil {
		t.Fatalf("write zero-byte alias: %v", err)
	}
	// Point candidate resolution at our temp alias only by calling pick through
	// the real resolver with overridden env via windowsTerminalIconSource on a
	// path that will not find package globs, then assert via pick contract.
	// Direct integration: windowsTerminalIconSource(alias) should return a
	// non-zero file when powershell is on PATH / System32.
	got := windowsTerminalIconSource(alias)
	if got == "" {
		t.Fatal("icon source empty")
	}
	if got == alias {
		info, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat result: %v", err)
		}
		if info.Size() == 0 {
			t.Fatalf("icon source stayed on zero-byte alias %q; want renderable fallback", got)
		}
	}
	if info, err := os.Stat(got); err != nil || info.IsDir() || info.Size() == 0 {
		t.Fatalf("icon source %q is not a non-zero file (err=%v)", got, err)
	}
}

func TestWindowsConsoleLaunchDoesNotInvokeCmdStart(t *testing.T) {
	// Integration-free: planWindowsConsoleLaunch + ShellExecute path must never
	// build cmd /c start. The pure helpers test covers special-character dirs;
	// here we only assert the platform launcher uses that plan shape.
	ps := firstWindowsExecutable([]string{"powershell.exe"},
		joinWindowsInstallPath(os.Getenv("WINDIR"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	if ps == "" {
		t.Skip("powershell.exe unavailable")
	}
	workdir := filepath.Join(t.TempDir(), "repo&calc")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	plan := planWindowsConsoleLaunch(ps, workdir)
	if !windowsConsoleLaunchIsDirect(plan) {
		t.Fatalf("console plan must be a direct ShellExecute open: %+v", plan)
	}
	if plan.File != ps {
		t.Fatalf("File = %q, want powershell target %q", plan.File, ps)
	}
	if plan.Dir != workdir {
		t.Fatalf("Dir = %q, want %q", plan.Dir, workdir)
	}
	// Do not call launchPlatformExternalOpener here: it would open a real
	// interactive console window in CI. ShellExecute wiring is covered by
	// openWorkspacePath and compile-time type checks.
}
