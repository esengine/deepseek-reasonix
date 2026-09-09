package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsVisualStudioDevenvCandidatesParseVSWhereOutput(t *testing.T) {
	install := filepath.Join(`C:\Program Files`, "Microsoft Visual Studio", "2026", "Professional")
	preview := filepath.Join(`D:\Apps`, "Microsoft Visual Studio", "2022", "Preview")
	got := windowsVisualStudioDevenvCandidates("\ufeff\"" + install + "\"\r\n\r\n" + preview + "\n")
	want := []string{
		filepath.Join(install, "Common7", "IDE", "devenv.exe"),
		filepath.Join(preview, "Common7", "IDE", "devenv.exe"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devenv candidates = %v, want %v", got, want)
	}
}

func TestWindowsVisualStudioFallbackCandidatesPreferNewestVersion(t *testing.T) {
	programFiles := filepath.Join("C:", "Program Files")
	programFilesX86 := filepath.Join("C:", "Program Files (x86)")
	got := windowsVisualStudioFallbackCandidatePaths(programFiles, programFilesX86)
	want := []string{
		filepath.Join(programFiles, "Microsoft Visual Studio", "2026", "*", "Common7", "IDE", "devenv.exe"),
		filepath.Join(programFilesX86, "Microsoft Visual Studio", "2026", "*", "Common7", "IDE", "devenv.exe"),
		filepath.Join(programFiles, "Microsoft Visual Studio", "2022", "*", "Common7", "IDE", "devenv.exe"),
		filepath.Join(programFilesX86, "Microsoft Visual Studio", "2022", "*", "Common7", "IDE", "devenv.exe"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback candidates = %v, want %v", got, want)
	}
}

func TestWindowsTerminalIconCandidatePathsPreferPackageBinary(t *testing.T) {
	// Use fixed Windows-style roots so the assertion is OS-independent; helpers
	// only join path segments and never touch the filesystem.
	wtAlias := `C:\Users\x\AppData\Local\Microsoft\WindowsApps\wt.exe`
	local := `C:\Users\x\AppData\Local`
	programFiles := `C:\Program Files`

	got := windowsTerminalIconCandidatePaths(wtAlias, local, programFiles)
	if len(got) < 2 {
		t.Fatalf("candidates = %v, want package paths before alias", got)
	}
	wantFirst := filepath.Join(programFiles, "WindowsApps", "Microsoft.WindowsTerminal_*", "WindowsTerminal.exe")
	if got[0] != wantFirst {
		t.Fatalf("first candidate = %q, want %q", got[0], wantFirst)
	}
	// Primary hit must be the Store package under Program Files\WindowsApps, not
	// a nested path under the App Execution Alias directory.
	if !strings.Contains(got[0], "WindowsApps"+string(filepath.Separator)+"Microsoft.WindowsTerminal_") {
		t.Fatalf("first candidate = %q, want Microsoft.WindowsTerminal_* under WindowsApps", got[0])
	}
	last := got[len(got)-1]
	if last != wtAlias {
		t.Fatalf("last candidate = %q, want wt alias %q", last, wtAlias)
	}
}

func TestWindowsConsoleLaunchPlanBypassesCmdAndPreservesMetacharacters(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		workdir string
	}{
		{name: "spaces", target: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, workdir: `C:\Users\demo\My Project`},
		{name: "ampersand", target: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, workdir: `C:\src\repo&calc`},
		{name: "pipe_parens", target: `C:\Windows\System32\cmd.exe`, workdir: `C:\src\a|b(c)^%d`},
		{name: "command_prompt_is_direct_target", target: `C:\Windows\System32\cmd.exe`, workdir: `D:\work`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planWindowsConsoleLaunch(tc.target, tc.workdir)
			if !windowsConsoleLaunchIsDirect(plan) {
				t.Fatalf("plan is not a direct ShellExecute open: %+v", plan)
			}
			if plan.File != tc.target {
				t.Fatalf("File = %q, want %q (target binary, not cmd /c start wrapper)", plan.File, tc.target)
			}
			if plan.Dir != tc.workdir {
				t.Fatalf("Dir = %q, want exact workdir (no shell rewriting)", plan.Dir)
			}
		})
	}
}

func TestWindowsConsoleLaunchIsDirectRejectsEmptyFile(t *testing.T) {
	if windowsConsoleLaunchIsDirect(windowsConsoleLaunch{}) {
		t.Fatal("empty plan must not count as direct")
	}
}

func TestPickWindowsTerminalIconSourcePrefersRenderableOverZeroByteAlias(t *testing.T) {
	wtAlias := `C:\Users\x\AppData\Local\Microsoft\WindowsApps\wt.exe`
	packageBin := `C:\Program Files\WindowsApps\Microsoft.WindowsTerminal_1.0_x64__8wekyb3d8bbwe\WindowsTerminal.exe`
	powershell := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`

	t.Run("nonzero_package_wins", func(t *testing.T) {
		got := pickWindowsTerminalIconSource([]windowsIconCandidate{
			{Path: wtAlias, Size: 0},
			{Path: packageBin, Size: 1_024_000},
		}, powershell)
		if got != packageBin {
			t.Fatalf("got %q, want package binary", got)
		}
	})
	t.Run("renderable_beats_zero_alias", func(t *testing.T) {
		// Protected WindowsApps often cannot be globbed; only the zero-byte
		// execution alias remains. PowerShell must win so SHGetFileInfo has a
		// real PE to extract (blank glyph fix for #6547).
		got := pickWindowsTerminalIconSource([]windowsIconCandidate{
			{Path: wtAlias, Size: 0},
		}, powershell)
		if got != powershell {
			t.Fatalf("got %q, want powershell renderable fallback", got)
		}
	})
	t.Run("zero_alias_only_without_fallback", func(t *testing.T) {
		got := pickWindowsTerminalIconSource([]windowsIconCandidate{
			{Path: wtAlias, Size: 0},
		}, "")
		if got != wtAlias {
			t.Fatalf("got %q, want alias when no renderable fallback exists", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := pickWindowsTerminalIconSource(nil, ""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}
