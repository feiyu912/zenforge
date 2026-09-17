package jobs

import "sync"

// Buffer is a bounded, offset-addressed output buffer. Every byte written
// advances a monotonically increasing offset that never rewinds, even when
// the buffer drops old bytes, so a reader can ask for "everything after
// offset N" and learn whether the bytes it missed are gone. That is what
// makes polling a long-running command safe: the reader never re-reads and
// never silently loses the fact that output was dropped.
type Buffer struct {
	mu       sync.Mutex
	data     []byte
	start    int64 // absolute offset of data[0]
	total    int64 // absolute offset just past the newest byte
	capacity int
}

// NewBuffer returns a buffer holding at most capacity bytes. A capacity of
// zero or less selects DefaultBufferBytes.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultBufferBytes
	}
	return &Buffer{capacity: capacity}
}

// DefaultBufferBytes bounds each output stream of a job.
const DefaultBufferBytes = 256 << 10

// Write appends bytes, dropping the oldest ones past the capacity.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if overflow := len(b.data) - b.capacity; overflow > 0 {
		b.data = b.data[overflow:]
		b.start += int64(overflow)
	}
	b.total += int64(len(p))
	return len(p), nil
}

// Chunk is a slice of a buffer.
type Chunk struct {
	// Data is the requested bytes.
	Data []byte
	// Offset is the absolute offset Data starts at.
	Offset int64
	// Next is the offset to pass to the following read.
	Next int64
	// Dropped is true when bytes between the requested offset and Offset
	// were discarded, so the reader knows its view has a hole.
	Dropped bool
	// Total is the absolute offset just past the newest byte.
	Total int64
}

// Read returns up to max bytes starting at the absolute offset since; a
// max of zero or less reads everything retained. An offset below the
// oldest retained byte is answered from the oldest byte with Dropped set,
// so a reader resynchronises instead of failing.
func (b *Buffer) Read(since int64, max int) Chunk {
	b.mu.Lock()
	defer b.mu.Unlock()
	chunk := Chunk{Total: b.total}
	if since < 0 {
		since = 0
	}
	if since < b.start {
		chunk.Dropped = true
		since = b.start
	}
	if since > b.total {
		since = b.total
	}
	chunk.Offset = since
	chunk.Next = since
	if since == b.total {
		return chunk
	}
	index := int(since - b.start)
	end := len(b.data)
	if max > 0 && index+max < end {
		end = index + max
	}
	if end > len(b.data) {
		end = len(b.data)
	}
	chunk.Data = append([]byte(nil), b.data[index:end]...)
	chunk.Next = since + int64(len(chunk.Data))
	return chunk
}

// Len is the number of retained bytes.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

// Total is the number of bytes ever written.
func (b *Buffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}
