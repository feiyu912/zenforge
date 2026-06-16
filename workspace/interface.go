package workspace

import "context"

type Workspace interface {
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, path string) ([]FileInfo, error)
	Grep(ctx context.Context, query GrepQuery) ([]Match, error)
	Stat(ctx context.Context, path string) (FileInfo, error)
}

type FileInfo struct {
	Path    string
	IsDir   bool
	Size    int64
	ModTime int64
	SHA256  string
}

type GrepQuery struct {
	Pattern    string
	Path       string
	MaxMatches int
}

type Match struct {
	Path string
	Line int
	Text string
}
