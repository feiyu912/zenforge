package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

type FileOperation string

const (
	FileRead  FileOperation = "read"
	FileList  FileOperation = "list"
	FileGrep  FileOperation = "grep"
	FileWrite FileOperation = "write"
)

type FilePolicy struct {
	ReadRoots       []string
	WriteRoots      []string
	RequireApproval bool
}

type FileAccessPlan struct {
	Operation        FileOperation `json:"operation"`
	RawPath          string        `json:"rawPath"`
	Path             string        `json:"path"`
	Root             string        `json:"root,omitempty"`
	Allowed          bool          `json:"allowed"`
	RequiresApproval bool          `json:"requiresApproval,omitempty"`
	RuleKey          string        `json:"ruleKey"`
	Fingerprint      string        `json:"fingerprint"`
	Reason           string        `json:"reason"`
}

type FileWritePlan struct {
	FilePath    string `json:"filePath"`
	Root        string `json:"root,omitempty"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
	Description string `json:"description"`
	Fingerprint string `json:"fingerprint"`
	RuleKey     string `json:"ruleKey"`
}

func PlanFileAccess(policy FilePolicy, operation FileOperation, rawPath string) FileAccessPlan {
	path, ok := normalizeWorkspacePath(rawPath)
	plan := FileAccessPlan{
		Operation:   operation,
		RawPath:     rawPath,
		Path:        path,
		RuleKey:     "file:" + string(operation),
		Fingerprint: fileFingerprint(string(operation), path),
	}
	if !ok {
		plan.Reason = "path escapes workspace policy roots"
		return plan
	}
	roots := policy.ReadRoots
	if operation == FileWrite {
		roots = policy.WriteRoots
	}
	if len(roots) == 0 {
		if policy.RequireApproval {
			plan.RequiresApproval = true
			plan.Reason = "file operation requires approval"
			return plan
		}
		plan.Allowed = true
		plan.Reason = "no additional file roots configured"
		return plan
	}
	for _, rawRoot := range roots {
		root, valid := normalizeWorkspacePath(rawRoot)
		if !valid {
			continue
		}
		if pathWithinRoot(path, root) {
			plan.Allowed = true
			plan.Root = root
			plan.RuleKey = "file:" + string(operation) + ":" + root
			plan.Reason = "path allowed by file root"
			return plan
		}
	}
	if policy.RequireApproval {
		plan.RequiresApproval = true
		plan.Reason = "path is outside configured file roots"
		return plan
	}
	plan.Reason = "path is outside configured file roots"
	return plan
}

func PlanFileWrite(access FileAccessPlan, content, description string) FileWritePlan {
	sum := sha256.Sum256([]byte(content))
	contentSHA := hex.EncodeToString(sum[:])
	return FileWritePlan{
		FilePath:    access.Path,
		Root:        access.Root,
		SizeBytes:   int64(len(content)),
		SHA256:      contentSHA,
		Description: description,
		Fingerprint: fileFingerprint(string(FileWrite), access.Path+"\x00"+contentSHA),
		RuleKey:     access.RuleKey,
	}
}

func normalizeWorkspacePath(raw string) (string, bool) {
	if raw == "" {
		raw = "."
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(filepath.Separator))
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(clean), false
	}
	return filepath.ToSlash(clean), true
}

func pathWithinRoot(path, root string) bool {
	if root == "." {
		return true
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

func fileFingerprint(operation, path string) string {
	sum := sha256.Sum256([]byte(operation + "\x00" + path))
	return hex.EncodeToString(sum[:])
}
