package main

import (
	"fmt"

	"reasonix/internal/control"
	"reasonix/internal/transcript"
)

// ResumeTranscriptSessionForTab adopts a session without materializing a legacy
// history response. The caller obtains its bounded page from TranscriptSnapshotForTab.
func (a *App) ResumeTranscriptSessionForTab(tabID, path string) error {
	_, err := a.resumeSessionForTranscript(tabID, path, 0, false)
	return err
}

func (a *App) OpenChannelTranscriptSessionForTab(tabID, path string) error {
	_, err := a.openChannelSessionForTranscript(tabID, path, 0, false)
	return err
}

func (a *App) transcriptAPIForTab(tabID string) (control.TranscriptProjectionAPI, func() bool, error) {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil, nil, a.workspaceNotReadyErr(tab)
	}
	api, ok := ctrl.(control.TranscriptProjectionAPI)
	if !ok {
		return nil, nil, control.ErrTranscriptProjectionUnavailable
	}
	a.mu.RLock()
	bound := tab != nil && a.tabs[tabID] == tab && tab.Ctrl == ctrl
	epoch := a.runtimeEpochForTabLocked(tab)
	a.mu.RUnlock()
	if !bound {
		return nil, nil, fmt.Errorf("runtime changed while binding transcript")
	}
	return api, func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.tabs[tabID] == tab && tab.Ctrl == ctrl && a.runtimeEpochForTabLocked(tab) == epoch
	}, nil
}

func (a *App) TranscriptSnapshotForTab(tabID string, req transcript.PageRequest) (transcript.Snapshot, error) {
	api, current, err := a.transcriptAPIForTab(tabID)
	if err != nil {
		return transcript.Snapshot{}, err
	}
	result, err := api.TranscriptSnapshot(req)
	if !current() {
		return transcript.Snapshot{}, fmt.Errorf("runtime changed while reading transcript")
	}
	return result, err
}

func (a *App) TranscriptPageForTab(tabID string, req transcript.PageRequest) (transcript.Snapshot, error) {
	return a.TranscriptSnapshotForTab(tabID, req)
}

func (a *App) TranscriptContentForTab(tabID string, req transcript.ContentRequest) (transcript.ContentChunk, error) {
	api, current, err := a.transcriptAPIForTab(tabID)
	if err != nil {
		return transcript.ContentChunk{}, err
	}
	result, err := api.TranscriptContent(req)
	if !current() {
		return transcript.ContentChunk{}, fmt.Errorf("runtime changed while reading transcript content")
	}
	return result, err
}

func (a *App) TranscriptReplayForTab(tabID string, req control.TranscriptReplayRequest) (control.TranscriptReplay, error) {
	api, current, err := a.transcriptAPIForTab(tabID)
	if err != nil {
		return control.TranscriptReplay{}, err
	}
	result, err := api.TranscriptReplay(req)
	if !current() {
		return control.TranscriptReplay{}, fmt.Errorf("runtime changed while replaying transcript")
	}
	return result, err
}
