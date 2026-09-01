package acp

import (
	"sort"
	"strings"
)

// availableCommandsFor lists the slash commands a session advertises to the
// client: custom commands, skills, MCP prompts, and extension actions, plus
// the builtin /clear the ACP server handles itself.
func availableCommandsFor(ctrl acpController) []AvailableCommand {
	if ctrl == nil {
		return nil
	}
	byName := map[string]AvailableCommand{}
	for _, cmd := range ctrl.Commands() {
		if cmd.Hidden {
			continue
		}
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(cmd.Description)
		if desc == "" {
			desc = "Run the " + name + " command"
		}
		ac := AvailableCommand{Name: name, Description: desc}
		if hint := strings.TrimSpace(cmd.ArgHint); hint != "" {
			ac.Input = &AvailableCommandInput{Hint: hint}
		}
		byName[name] = ac
	}
	for _, sk := range ctrl.SlashSkills() {
		name := strings.TrimSpace(sk.SlashName())
		if name == "" {
			continue
		}
		if _, exists := byName[name]; exists {
			continue
		}
		desc := strings.TrimSpace(sk.Description)
		if desc == "" {
			desc = "Run the " + name + " skill"
		}
		byName[name] = AvailableCommand{
			Name:        name,
			Description: desc,
			Input:       &AvailableCommandInput{Hint: "instructions"},
		}
	}
	if host := ctrl.Host(); host != nil {
		for _, prompt := range host.Prompts() {
			name := strings.TrimSpace(prompt.Name)
			if name == "" {
				continue
			}
			desc := strings.TrimSpace(prompt.Description)
			if desc == "" {
				desc = "Run the " + name + " MCP prompt"
			}
			ac := AvailableCommand{Name: name, Description: desc}
			if len(prompt.Args) > 0 {
				ac.Input = &AvailableCommandInput{Hint: "arguments"}
			}
			byName[name] = ac
		}
	}
	// Extension actions surface as "<plugin>:<action>" commands so ACP clients
	// can discover them in the slash menu alongside commands/skills/prompts.
	for _, action := range ctrl.ExtensionActions() {
		name := strings.TrimPrefix(strings.TrimSpace(action.Slash), "/")
		if name == "" {
			continue
		}
		if _, exists := byName[name]; exists {
			continue
		}
		desc := strings.TrimSpace(action.Label)
		if desc == "" {
			desc = "Run the " + name + " extension action"
		}
		byName[name] = AvailableCommand{
			Name:        name,
			Description: desc,
			Input:       &AvailableCommandInput{Hint: "arguments"},
		}
	}
	// /clear is a builtin the ACP server handles itself (handleClearPrompt), so
	// advertise it like any other slash command; a custom command of the same
	// name is shadowed, matching Submit's builtin-first precedence.
	byName["clear"] = AvailableCommand{Name: "clear", Description: "Clear the current session"}
	out := make([]AvailableCommand, 0, len(byName))
	for _, cmd := range byName {
		out = append(out, cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
