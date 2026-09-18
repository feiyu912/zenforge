package jobs

import "sync"

// Buffer is a bounded, offset-addressed output buffer that keeps both ends
// of a stream: the first HeadBytes ever written and the most recent bytes up
// to the capacity. A command that floods its output therefore still shows how
// it started — the banner, the startup line, the first error — instead of
// only the last screenful, which is what makes a long-running service's
// output readable after the fact.
//
// Every byte written advances a monotonically increasing offset that never
// rewinds, even when the buffer drops the middle, so a reader can ask for
// "everything after offset N" and learn whether the bytes it missed are gone.
// That is what makes polling a long-running command safe: the reader never
// re-reads and never silently loses the fact that output was dropped.
type Buffer struct {
	mu       sync.Mutex
	head     []byte
	tail     []byte
	total    int64 // absolute offset just past the newest byte
	capacity int
	headCap  int
}

// NewBuffer returns a buffer holding at most capacity bytes. A capacity of
// zero or less selects DefaultBufferBytes.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultBufferBytes
	}
	return &Buffer{capacity: capacity, headCap: headCapacity(capacity)}
}

// DefaultBufferBytes bounds each output stream of a job.
const DefaultBufferBytes = 256 << 10

// MaxHeadBytes caps the retained head however large the capacity is: a
// quarter of the buffer, at most 8KiB.
const MaxHeadBytes = 8 << 10

func headCapacity(capacity int) int {
	head := capacity / 4
	if head > MaxHeadBytes {
		head = MaxHeadBytes
	}
	return head
}

// tailCapacity is how much of the capacity is left for the newest bytes.
func (b *Buffer) tailCapacity() int { return b.capacity - b.headCap }

// Write appends bytes, keeping the head and dropping the oldest middle bytes
// past the capacity.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.headCap - len(b.head); room > 0 {
		take := len(p)
		if take > room {
			take = room
		}
		b.head = append(b.head, p[:take]...)
	}
	b.tail = append(b.tail, p...)
	if overflow := len(b.tail) - b.tailCapacity(); overflow > 0 {
		b.tail = b.tail[overflow:]
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
	// Elided is how many bytes Dropped stands for. It is zero when nothing
	// was lost between the request and the returned bytes.
	Elided int64
	// Total is the absolute offset just past the newest byte.
	Total int64
}

// Read returns up to max bytes starting at the absolute offset since; a
// max of zero or less reads everything available at that offset. Bytes
// before the retained tail are answered from the retained head first, and a
// read that lands in the discarded middle jumps to the oldest retained tail
// byte with Dropped and Elided set, so a reader resynchronises instead of
// failing or receiving a hole it cannot see.
func (b *Buffer) Read(since int64, max int) Chunk {
	b.mu.Lock()
	defer b.mu.Unlock()
	chunk := Chunk{Total: b.total}
	if since < 0 {
		since = 0
	}
	if since > b.total {
		since = b.total
	}
	chunk.Offset = since
	chunk.Next = since
	headEnd := int64(len(b.head))
	if since < headEnd {
		end := headEnd
		if max > 0 && since+int64(max) < end {
			end = since + int64(max)
		}
		chunk.Data = append([]byte(nil), b.head[int(since):int(end)]...)
		chunk.Next = since + int64(len(chunk.Data))
		return chunk
	}
	tailStart := b.total - int64(len(b.tail))
	if since < tailStart {
		chunk.Dropped = true
		chunk.Elided = tailStart - since
		since = tailStart
		chunk.Offset = since
		chunk.Next = since
	}
	if since >= b.total {
		return chunk
	}
	index := int(since - tailStart)
	end := len(b.tail)
	if max > 0 && index+max < end {
		end = index + max
	}
	chunk.Data = append([]byte(nil), b.tail[index:end]...)
	chunk.Next = since + int64(len(chunk.Data))
	return chunk
}

// Len is the number of retained bytes.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.head) + len(b.tail)
}

// Total is the number of bytes ever written.
func (b *Buffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}
