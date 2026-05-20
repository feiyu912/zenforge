package workspace

import "context"

type Workspace interface {
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, path string) ([]FileInfo, error)
	Grep(ctx context.Context, query GrepQuery) ([]Match, error)
}

type FileInfo struct {
	Path  string
	IsDir bool
	Size  int64
}

type GrepQuery struct {
	Pattern string
	Path    string
}

type Match struct {
	Path string
	Line int
	Text string
}

