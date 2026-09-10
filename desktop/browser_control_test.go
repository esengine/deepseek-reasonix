package main

import (
	"testing"

	"reasonix/internal/browser"
)

func browserControlTab() *WorkspaceTab {
	return &WorkspaceTab{ID: "tab-control"}
}

func TestBrowserControlGatesNewSessions(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.hostShell = &hostShellBridge{app: app}
	tab := browserControlTab()

	if exec := app.browserExecutorForTab(tab); exec == nil {
		t.Fatal("browser control is on by default, so a host session gets an executor")
	}
	app.setBrowserControlEnabled(false)
	if exec := app.browserExecutorForTab(tab); exec != nil {
		t.Fatalf("disabled control returned %T, want nil so no browser tool is registered", exec)
	}
	app.setBrowserControlEnabled(true)
	if exec := app.browserExecutorForTab(tab); exec == nil {
		t.Fatal("re-enabling control must restore the executor")
	}
}

func TestBrowserControlGateCoversRemoteExecutors(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.hostShell = &hostShellBridge{app: app}
	app.setBrowserControlEnabled(false)
	if exec := app.browserExecutorForRemoteTab(&remoteTab{id: "remote-1"}, "/workspace"); exec != nil {
		t.Fatalf("disabled control returned %T for a remote tab, want nil", exec)
	}
}

func TestBrowserControlGateKeepsNonHostModeClosed(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.setBrowserControlEnabled(true)
	if exec := app.browserExecutorForTab(browserControlTab()); exec != nil {
		t.Fatalf("executor without the Electron shell = %T, want nil", exec)
	}
	var _ browser.Executor = (*hostBrowserExecutor)(nil)
}
