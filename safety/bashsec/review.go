package bashsec

import (
	"path"
	"strings"

	"github.com/feiyu912/zenforge/safety/bashast"
)

type ReviewDecision string

const (
	ReviewAllow            ReviewDecision = "allow"
	ReviewRequiresApproval ReviewDecision = "requires_approval"
	ReviewBlock            ReviewDecision = "block"
)

type ReviewResult struct {
	Decision ReviewDecision
	Reason   string
	RuleKey  string
	Risk     string
	Commands [][]string
}

var dangerousShellCommands = map[string]struct{}{
	".": {}, "alias": {}, "eval": {}, "exec": {}, "source": {}, "trap": {},
	"enable": {}, "set": {}, "shopt": {}, "unset": {}, "zmodload": {},
}

func Review(command string, knownVariables map[string]string) ReviewResult {
	parsed, embedded := bashast.ParseWithEmbeddedDetectionAndKnownVariables(command, knownVariables)
	switch parsed.Kind {
	case bashast.TooComplex:
		if bashast.IsHardBlockReason(parsed.Reason) {
			return ReviewResult{Decision: ReviewBlock, Reason: parsed.Reason, RuleKey: "bashast:hard_block", Risk: "high"}
		}
		return ReviewResult{Decision: ReviewRequiresApproval, Reason: reasonOrDefault(parsed.Reason), RuleKey: "bashast:too_complex", Risk: "high"}
	case bashast.ParseUnavailable:
		return ReviewResult{Decision: ReviewRequiresApproval, Reason: "shell parser unavailable", RuleKey: "bashast:too_complex", Risk: "high"}
	case bashast.Simple:
	default:
		return ReviewResult{Decision: ReviewRequiresApproval, Reason: "shell command could not be classified", RuleKey: "bashast:too_complex", Risk: "high"}
	}
	if parsed.HasCommandSubstitution {
		return ReviewResult{Decision: ReviewBlock, Reason: "command substitution is not allowed", RuleKey: "shell_substitution", Risk: "high"}
	}
	if parsed.HasControlStructure {
		return ReviewResult{Decision: ReviewBlock, Reason: "shell control operators or structures are not allowed", RuleKey: "shell_control", Risk: "high"}
	}
	if parsed.HasRedirection {
		return ReviewResult{Decision: ReviewBlock, Reason: "shell redirection is not allowed", RuleKey: "shell_redirection", Risk: "high"}
	}
	for _, script := range embedded {
		if bashast.IsDangerousEmbeddedScript(script) {
			return ReviewResult{Decision: ReviewBlock, Reason: "dangerous embedded " + script.Language + " code is not allowed", RuleKey: "embedded_script:" + script.Language, Risk: "critical"}
		}
	}
	commands := make([][]string, 0, len(parsed.Commands))
	for _, parsedCommand := range parsed.Commands {
		if len(parsedCommand.Argv) == 0 {
			continue
		}
		argv := append([]string(nil), parsedCommand.Argv...)
		commands = append(commands, argv)
		base := strings.ToLower(path.Base(argv[0]))
		if _, dangerous := dangerousShellCommands[base]; dangerous {
			return ReviewResult{Decision: ReviewBlock, Reason: "dangerous shell command is not allowed: " + base, RuleKey: "dangerous:" + base, Risk: "critical"}
		}
	}
	if len(commands) == 0 {
		return ReviewResult{Decision: ReviewBlock, Reason: "command does not execute an analyzable program", RuleKey: "no_command", Risk: "invalid"}
	}
	return ReviewResult{Decision: ReviewAllow, Commands: commands}
}

func reasonOrDefault(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "command is too complex for static shell analysis"
	}
	return reason
}
