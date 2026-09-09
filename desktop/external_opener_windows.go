//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"reasonix/internal/proc"
)

const (
	shgfiIcon              = 0x000000100
	shgfiLargeIcon         = 0x000000000
	dibRGBColors           = 0
	drawIconNormal         = 0x0003
	windowsOpenerIconWidth = 32
	windowsVSWhereTimeout  = 2 * time.Second
)

type windowsShellFileInfo struct {
	Icon        windows.Handle
	IconIndex   int32
	Attributes  uint32
	DisplayName [260]uint16
	TypeName    [80]uint16
}

type windowsBitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ColorsUsed    uint32
	ColorsNeeded  uint32
}

type windowsBitmapInfo struct {
	Header windowsBitmapInfoHeader
	Colors [1]uint32
}

var (
	windowsShell32            = windows.NewLazySystemDLL("shell32.dll")
	windowsUser32             = windows.NewLazySystemDLL("user32.dll")
	windowsGDI32              = windows.NewLazySystemDLL("gdi32.dll")
	windowsSHGetFileInfo      = windowsShell32.NewProc("SHGetFileInfoW")
	windowsDestroyIcon        = windowsUser32.NewProc("DestroyIcon")
	windowsDrawIconEx         = windowsUser32.NewProc("DrawIconEx")
	windowsGetDC              = windowsUser32.NewProc("GetDC")
	windowsReleaseDC          = windowsUser32.NewProc("ReleaseDC")
	windowsCreateCompatibleDC = windowsGDI32.NewProc("CreateCompatibleDC")
	windowsCreateDIBSection   = windowsGDI32.NewProc("CreateDIBSection")
	windowsSelectObject       = windowsGDI32.NewProc("SelectObject")
	windowsDeleteObject       = windowsGDI32.NewProc("DeleteObject")
	windowsDeleteDC           = windowsGDI32.NewProc("DeleteDC")
)

func joinWindowsInstallPath(root string, parts ...string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func firstWindowsExecutable(names []string, candidates ...string) string {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		if path := windowsAppPathExecutable(name); path != "" {
			return path
		}
	}
	return firstWindowsExistingFile(candidates...)
}

func firstWindowsExistingFile(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if matches, _ := filepath.Glob(candidate); len(matches) > 0 {
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					return match
				}
			}
		}
	}
	return ""
}

func queryWindowsVSWhere(vswherePath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), windowsVSWhereTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, vswherePath,
		"-latest", "-products", "*", "-property", "installationPath", "-utf8").Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func findWindowsVisualStudioExecutable(programFiles, programFilesX86 string, queryVSWhere func(string) (string, error)) string {
	vswhere := firstWindowsExistingFile(
		joinWindowsInstallPath(programFilesX86, "Microsoft Visual Studio", "Installer", "vswhere.exe"),
		joinWindowsInstallPath(programFiles, "Microsoft Visual Studio", "Installer", "vswhere.exe"),
	)
	if vswhere == "" {
		vswhere = firstWindowsExecutable([]string{"vswhere.exe"})
	}
	if vswhere != "" && queryVSWhere != nil {
		if output, err := queryVSWhere(vswhere); err == nil {
			if path := firstWindowsExistingFile(windowsVisualStudioDevenvCandidates(output)...); path != "" {
				return path
			}
		}
	}
	if path := firstWindowsExistingFile(windowsVisualStudioFallbackCandidatePaths(programFiles, programFilesX86)...); path != "" {
		return path
	}
	return firstWindowsExecutable([]string{"devenv.exe"})
}

func windowsVisualStudioExecutable(programFiles, programFilesX86 string) string {
	return findWindowsVisualStudioExecutable(programFiles, programFilesX86, queryWindowsVSWhere)
}

// windowsAppPathExecutable resolves an install registered under
// HKCU/HKLM ...\App Paths\<name>. Custom VS Code / editor installs often only
// appear there, not on PATH or under the default Program Files trees.
func windowsAppPathExecutable(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	subKey := `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\` + name
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		key, err := registry.OpenKey(root, subKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		raw, _, err := key.GetStringValue("")
		key.Close()
		if err != nil {
			continue
		}
		path := strings.Trim(strings.TrimSpace(raw), `"`)
		if path == "" {
			continue
		}
		path = os.ExpandEnv(path)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// windowsTerminalIconSource prefers a real Windows Terminal package binary for
// SHGetFileInfo. Store installs expose wt.exe as an App Execution Alias (often
// zero bytes under LocalAppData\Microsoft\WindowsApps). The package binary may
// live under Program Files\WindowsApps, but non-elevated processes frequently
// cannot enumerate that protected directory — so a renderable console-host
// fallback must win over a zero-byte alias (see pickWindowsTerminalIconSource).
func windowsTerminalIconSource(wtPath string) string {
	var resolved []windowsIconCandidate
	for _, candidate := range windowsTerminalIconCandidatePaths(
		wtPath,
		os.Getenv("LOCALAPPDATA"),
		os.Getenv("ProgramFiles"),
	) {
		matches := []string{candidate}
		if strings.ContainsAny(candidate, `*?[`) {
			globbed, err := filepath.Glob(candidate)
			if err != nil || len(globbed) == 0 {
				continue
			}
			matches = globbed
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			resolved = append(resolved, windowsIconCandidate{Path: match, Size: info.Size()})
		}
	}
	// Prefer a normal console host icon over a zero-byte wt.exe alias so the
	// menu never ships a blank glyph when WindowsApps is unreadable.
	renderable := firstWindowsExecutable([]string{"powershell.exe"},
		joinWindowsInstallPath(os.Getenv("WINDIR"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	if picked := pickWindowsTerminalIconSource(resolved, renderable); picked != "" {
		return picked
	}
	return strings.TrimSpace(wtPath)
}

// shellExecuteOpenFile launches file via ShellExecuteW("open") without cmd.exe.
// parameters and directory are optional; directory becomes the process CWD.
func shellExecuteOpenFile(file, parameters, directory string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var paramsPtr *uint16
	if parameters != "" {
		paramsPtr, err = windows.UTF16PtrFromString(parameters)
		if err != nil {
			return err
		}
	}
	var dirPtr *uint16
	if directory != "" {
		dirPtr, err = windows.UTF16PtrFromString(directory)
		if err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, verb, filePtr, paramsPtr, dirPtr, windows.SW_SHOWNORMAL)
}

func platformExternalOpenerSpecs() []externalOpenerSpec {
	local := os.Getenv("LOCALAPPDATA")
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	windowsDir := os.Getenv("WINDIR")
	var specs []externalOpenerSpec
	add := func(id, name, kind, mode string, names []string, candidates ...string) {
		if path := firstWindowsExecutable(names, candidates...); path != "" {
			specs = append(specs, externalOpenerSpec{
				View:       ExternalOpenerView{ID: id, Name: name, Kind: kind},
				Target:     path,
				LaunchMode: mode,
				IconSource: path,
			})
		}
	}
	jetbrainsProgram := func(product, executable string) string {
		return joinWindowsInstallPath(programFiles, "JetBrains", product+" *", "bin", executable)
	}
	jetbrainsToolbox := func(product, executable string) string {
		return joinWindowsInstallPath(local, "JetBrains", "Toolbox", "apps", product, "*", "*", "bin", executable)
	}

	add("vscode", "VS Code", externalOpenerEditor, "path", []string{"Code.exe"},
		joinWindowsInstallPath(local, "Programs", "Microsoft VS Code", "Code.exe"),
		joinWindowsInstallPath(programFiles, "Microsoft VS Code", "Code.exe"),
		joinWindowsInstallPath(programFilesX86, "Microsoft VS Code", "Code.exe"))
	add("vscode-insiders", "VS Code Insiders", externalOpenerEditor, "path", []string{"Code - Insiders.exe"},
		joinWindowsInstallPath(local, "Programs", "Microsoft VS Code Insiders", "Code - Insiders.exe"))
	if path := windowsVisualStudioExecutable(programFiles, programFilesX86); path != "" {
		specs = append(specs, externalOpenerSpec{
			View:       ExternalOpenerView{ID: "visual-studio", Name: "Visual Studio", Kind: externalOpenerEditor},
			Target:     path,
			LaunchMode: "path",
			IconSource: path,
		})
	}
	add("cursor", "Cursor", externalOpenerEditor, "path", []string{"Cursor.exe"},
		joinWindowsInstallPath(local, "Programs", "cursor", "Cursor.exe"),
		joinWindowsInstallPath(programFiles, "Cursor", "Cursor.exe"))
	add("file-explorer", "File Explorer", externalOpenerFileManager, "shell-open", []string{"explorer.exe"},
		joinWindowsInstallPath(windowsDir, "explorer.exe"))
	if wt := firstWindowsExecutable([]string{"wt.exe"}); wt != "" {
		specs = append(specs, externalOpenerSpec{
			View:       ExternalOpenerView{ID: "windows-terminal", Name: "Windows Terminal", Kind: externalOpenerTerminal},
			Target:     wt,
			LaunchMode: "windows-terminal",
			IconSource: windowsTerminalIconSource(wt),
		})
	}
	add("powershell", "PowerShell", externalOpenerTerminal, "console", []string{"pwsh.exe", "powershell.exe"},
		joinWindowsInstallPath(windowsDir, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	add("command-prompt", "Command Prompt", externalOpenerTerminal, "console", []string{"cmd.exe"},
		joinWindowsInstallPath(windowsDir, "System32", "cmd.exe"))
	add("android-studio", "Android Studio", externalOpenerEditor, "path", []string{"studio64.exe", "studio.exe"},
		joinWindowsInstallPath(programFiles, "Android", "Android Studio", "bin", "studio64.exe"))
	add("goland", "GoLand", externalOpenerEditor, "path", []string{"goland64.exe", "goland.exe"},
		jetbrainsProgram("GoLand", "goland64.exe"), jetbrainsToolbox("GoLand", "goland64.exe"))
	add("pycharm", "PyCharm", externalOpenerEditor, "path", []string{"pycharm64.exe", "pycharm.exe"},
		jetbrainsProgram("PyCharm", "pycharm64.exe"), jetbrainsToolbox("PyCharm*", "pycharm64.exe"))
	add("intellij-idea", "IntelliJ IDEA", externalOpenerEditor, "path", []string{"idea64.exe", "idea.exe"},
		jetbrainsProgram("IntelliJ IDEA", "idea64.exe"), jetbrainsToolbox("IDEA*", "idea64.exe"))
	add("webstorm", "WebStorm", externalOpenerEditor, "path", []string{"webstorm64.exe", "webstorm.exe"},
		jetbrainsProgram("WebStorm", "webstorm64.exe"), jetbrainsToolbox("WebStorm", "webstorm64.exe"))
	add("datagrip", "DataGrip", externalOpenerEditor, "path", []string{"datagrip64.exe", "datagrip.exe"},
		jetbrainsProgram("DataGrip", "datagrip64.exe"), jetbrainsToolbox("DataGrip", "datagrip64.exe"))
	add("codebuddy", "CodeBuddy", externalOpenerEditor, "path", []string{"CodeBuddy.exe"},
		joinWindowsInstallPath(local, "Programs", "CodeBuddy", "CodeBuddy.exe"),
		joinWindowsInstallPath(programFiles, "CodeBuddy", "CodeBuddy.exe"))
	add("windsurf", "Windsurf", externalOpenerEditor, "path", []string{"Windsurf.exe"},
		joinWindowsInstallPath(local, "Programs", "Windsurf", "Windsurf.exe"))
	add("zed", "Zed", externalOpenerEditor, "path", []string{"zed.exe"},
		joinWindowsInstallPath(local, "Programs", "Zed", "zed.exe"))
	add("sublime-text", "Sublime Text", externalOpenerEditor, "path", []string{"sublime_text.exe"},
		joinWindowsInstallPath(programFiles, "Sublime Text", "sublime_text.exe"))
	add("kiro", "Kiro", externalOpenerEditor, "path", []string{"Kiro.exe"},
		joinWindowsInstallPath(local, "Programs", "Kiro", "Kiro.exe"))
	return specs
}

func platformExternalOpenerIconDataURL(spec externalOpenerSpec) string {
	if spec.IconSource == "" {
		return ""
	}
	path, err := windows.UTF16PtrFromString(spec.IconSource)
	if err != nil {
		return ""
	}
	var info windowsShellFileInfo
	result, _, _ := windowsSHGetFileInfo.Call(
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		shgfiIcon|shgfiLargeIcon,
	)
	if result == 0 || info.Icon == 0 {
		return ""
	}
	defer windowsDestroyIcon.Call(uintptr(info.Icon))

	black, ok := renderWindowsExternalOpenerIcon(info.Icon, 0)
	if !ok {
		return ""
	}
	white, ok := renderWindowsExternalOpenerIcon(info.Icon, 255)
	if !ok {
		return ""
	}
	return externalOpenerPNGDataURL(externalOpenerPNGFromBGRAComposites(
		black,
		white,
		windowsOpenerIconWidth,
		windowsOpenerIconWidth,
	))
}

func renderWindowsExternalOpenerIcon(icon windows.Handle, background byte) ([]byte, bool) {
	screenDC, _, _ := windowsGetDC.Call(0)
	if screenDC == 0 {
		return nil, false
	}
	defer windowsReleaseDC.Call(0, screenDC)

	memoryDC, _, _ := windowsCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return nil, false
	}
	defer windowsDeleteDC.Call(memoryDC)

	bitmapInfo := windowsBitmapInfo{Header: windowsBitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(windowsBitmapInfoHeader{})),
		Width:       windowsOpenerIconWidth,
		Height:      -windowsOpenerIconWidth,
		Planes:      1,
		BitCount:    32,
		Compression: 0,
	}}
	var bits unsafe.Pointer
	bitmap, _, _ := windowsCreateDIBSection.Call(
		memoryDC,
		uintptr(unsafe.Pointer(&bitmapInfo)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 || bits == nil {
		return nil, false
	}
	defer windowsDeleteObject.Call(bitmap)

	previous, _, _ := windowsSelectObject.Call(memoryDC, bitmap)
	if previous == 0 {
		return nil, false
	}
	defer windowsSelectObject.Call(memoryDC, previous)

	pixels := unsafe.Slice((*byte)(bits), windowsOpenerIconWidth*windowsOpenerIconWidth*4)
	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset] = background
		pixels[offset+1] = background
		pixels[offset+2] = background
		pixels[offset+3] = 255
	}
	drawn, _, _ := windowsDrawIconEx.Call(
		memoryDC,
		0,
		0,
		uintptr(icon),
		windowsOpenerIconWidth,
		windowsOpenerIconWidth,
		0,
		0,
		drawIconNormal,
	)
	if drawn == 0 {
		return nil, false
	}
	return append([]byte(nil), pixels...), true
}

func launchPlatformExternalOpener(spec externalOpenerSpec, path string, isDir bool) error {
	var cmd *exec.Cmd
	launchPath := externalOpenerLaunchPath(spec, path)
	switch spec.LaunchMode {
	case "shell-open":
		return openWorkspacePathWithType(path, isDir)
	case "path":
		cmd = proc.VisibleCommand(spec.Target, path)
	case "windows-terminal":
		// exec.Command uses CreateProcess argument escaping, not cmd.exe, so
		// workspace paths with shell metacharacters stay a single -d argument.
		cmd = proc.VisibleCommand(spec.Target, "-d", launchPath)
	case "console":
		// Never route console openers through cmd.exe / start: working-directory
		// text would be re-parsed as shell syntax (& | ^ etc.). ShellExecute
		// opens the binary with lpDirectory set to the workspace path.
		plan := planWindowsConsoleLaunch(spec.Target, launchPath)
		if plan.File == "" {
			return os.ErrNotExist
		}
		return shellExecuteOpenFile(plan.File, "", plan.Dir)
	default:
		cmd = proc.VisibleCommand(spec.Target, path)
	}
	return startDetachedExternalOpener(cmd)
}
