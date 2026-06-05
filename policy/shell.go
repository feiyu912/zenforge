package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

type ReviewDecision string

const (
	ReviewAllow           ReviewDecision = "allow"
	ReviewRequireApproval ReviewDecision = "require_approval"
	ReviewBlock           ReviewDecision = "block"
)

type ShellPolicy struct {
	WorkingDir      string
	AllowCommands   []string
	DenyCommands    []string
	RequireApproval bool
	MaxTimeout      time.Duration
	MaxOutputBytes  int64
	Env             map[string]string
	AllowedEnvKeys  []string
}

type CommandReview struct {
	Decision    ReviewDecision
	Reason      string
	RuleKey     string
	Fingerprint string
	Risk        string
}

func ReviewCommand(policy ShellPolicy, command string) CommandReview {
	normalized := normalizeCommand(command)
	fingerprint := fingerprint(normalized)
	if normalized == "" {
		return CommandReview{Decision: ReviewBlock, Reason: "empty command", RuleKey: "empty", Fingerprint: fingerprint, Risk: "invalid"}
	}
	if hasShellControl(normalized) {
		return CommandReview{Decision: ReviewBlock, Reason: "shell control operators are not allowed", RuleKey: "shell_control", Fingerprint: fingerprint, Risk: "high"}
	}
	for _, deny := range policy.DenyCommands {
		if commandMatches(normalized, deny) {
			return CommandReview{Decision: ReviewBlock, Reason: "command denied by policy", RuleKey: "deny:" + deny, Fingerprint: fingerprint, Risk: "blocked"}
		}
	}
	for _, allow := range policy.AllowCommands {
		if commandMatches(normalized, allow) {
			return CommandReview{Decision: ReviewAllow, Reason: "command allowed by policy", RuleKey: "allow:" + allow, Fingerprint: fingerprint}
		}
	}
	if policy.RequireApproval {
		return CommandReview{Decision: ReviewRequireApproval, Reason: "command requires approval", RuleKey: "approval_required", Fingerprint: fingerprint, Risk: "approval_required"}
	}
	return CommandReview{Decision: ReviewBlock, Reason: "command is not allowlisted", RuleKey: "not_allowlisted", Fingerprint: fingerprint, Risk: "blocked"}
}

func ResolveWorkingDir(root, cwd string) (string, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if cwd == "" {
		cwd = "."
	}
	clean := filepath.Clean(filepath.FromSlash(cwd))
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(filepath.Separator))
	}
	candidate := filepath.Join(absRoot, clean)
	rel, err := filepath.Rel(absRoot, candidate)
	if err != nil {
		return "", err
	}
	if rel != "." && (strings.HasPrefix(rel, "..") || filepath.IsAbs(rel)) {
		return "", ErrPathEscape
	}
	return candidate, nil
}

func AllowedEnv(policy ShellPolicy) []string {
	if len(policy.Env) == 0 || len(policy.AllowedEnvKeys) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(policy.AllowedEnvKeys))
	for _, key := range policy.AllowedEnvKeys {
		allowed[key] = struct{}{}
	}
	var env []string
	for key, value := range policy.Env {
		if _, ok := allowed[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func normalizeCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func commandMatches(command, rule string) bool {
	normalizedRule := normalizeCommand(rule)
	return command == normalizedRule || strings.HasPrefix(command, normalizedRule+" ")
}

func hasShellControl(command string) bool {
	control := []string{"&&", "||", ";", "|", "\n", "`", "$(", ">", "<"}
	for _, op := range control {
		if strings.Contains(command, op) {
			return true
		}
	}
	return false
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
