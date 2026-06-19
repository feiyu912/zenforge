package workspace

import (
	"path/filepath"
	"strings"
)

// binaryExtensions mirrors the platform file-tool denylist for formats that
// should never be treated as text based only on their current byte contents.
var binaryExtensions = map[string]struct{}{
	".7z": {}, ".a": {}, ".bin": {}, ".bz2": {}, ".class": {},
	".dll": {}, ".dmg": {}, ".dylib": {}, ".exe": {}, ".gz": {},
	".ico": {}, ".iso": {}, ".jar": {}, ".o": {}, ".pdf": {},
	".rar": {}, ".so": {}, ".tar": {}, ".tgz": {}, ".war": {},
	".xz": {}, ".zip": {},
}

var blockedDeviceFiles = map[string]struct{}{
	"/dev/full":    {},
	"/dev/null":    {},
	"/dev/random":  {},
	"/dev/urandom": {},
	"/dev/zero":    {},
}

func IsBinaryPath(path string) bool {
	_, ok := binaryExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func IsBlockedDevicePath(path string) bool {
	_, ok := blockedDeviceFiles[filepath.Clean(path)]
	return ok
}
