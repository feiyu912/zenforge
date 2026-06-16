package policy

import "errors"

var ErrPathEscape = errors.New("path escapes policy root")
var ErrFileAccessDenied = errors.New("file access denied by policy")
