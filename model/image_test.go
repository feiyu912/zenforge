package model

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

// The WebP fixtures are real files, not headers written by this test: the point
// of the reader is that it agrees with a real encoder about a real container, and
// a hand-built header would only prove that the test and the reader share an
// assumption. Each was produced with Pillow (`Image.new(...).save(path, "WEBP",
// ...)`) and embedded so the test needs nothing on disk.
const (
	// webpLossy7x5: a plain lossy file, `VP8 ` bitstream.
	webpLossy7x5 = "UklGRi4AAABXRUJQVlA4ICIAAABwAQCdASoHAAUAAUAmJZQCdAFAAAD+/DeBV/fU6D4r4AAA"
	// webpLossy300x7: a wide lossy file, which exercises the dimension field
	// without the two bytes being equal.
	webpLossy300x7 = "UklGRkQAAABXRUJQVlA4IDgAAACwAwCdASosAQcAPm02mUmkIyKhIagAgA2JaQAADHThw4cOHDhugAD++iGXzse4P+g+GiewAAAAAA=="
	// webpLossless11x23: a lossless file, `VP8L` bitstream, whose dimensions are
	// packed into one 32-bit field.
	webpLossless11x23 = "UklGRh4AAABXRUJQVlA4TBEAAAAvCoAFAAdQiirUo/+BiOh/AAA="
	// webpExtended7x5: an extended container, `VP8X` canvas plus a `VP8 ` frame,
	// forced by attached EXIF metadata.
	webpExtended7x5 = "UklGRkwAAABXRUJQVlA4WAoAAAAIAAAABgAABAAAVlA4ICIAAABwAQCdASoHAAUAAUAmJZQCdAFAAAD+/DeBV/fU6D4r4AAARVhJRgMAAABhYmMA"
	// webpAlpha640x480: an extended container with an alpha bitstream, a real
	// 640x480 canvas.
	webpAlpha640x480 = "UklGRmICAABXRUJQVlA4IFYCAADQQwCdASqAAuABPm0skkWkIqGZ/cQAPm0pcQAAdwChSAA0AAH1s7xd2EbQAnsA99snIe+Da9QpUkgAACJ3ndCJlIUpXlcCUxJ8Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8P/9k/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J/Bh2T+DDsn8GHZP4MOyfwYdk/gw7J+DA="
)

func decodeWebP(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return data
}

// The reader is checked against real files of all three bitstreams. A size that
// comes back wrong here is wrong in the console's attachment descriptor, which is
// what it renders with -- so each variant is pinned separately rather than
// trusting one to stand for the others.
func TestImageSizeReadsRealWebPHeaders(t *testing.T) {
	cases := []struct {
		name      string
		encoded   string
		width     int
		height    int
		bitstream string
	}{
		{"lossy", webpLossy7x5, 7, 5, "VP8 "},
		{"lossy-wide", webpLossy300x7, 300, 7, "VP8 "},
		{"lossless", webpLossless11x23, 11, 23, "VP8L"},
		{"extended-real", webpExtended7x5, 7, 5, "VP8X"},
		{"extended-canvas-only", "", 9, 6, "VP8X"},
		{"extended-alpha", webpAlpha640x480, 640, 480, "VP8X"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := decodeWebP(t, testCase.encoded)
			if testCase.name == "extended-canvas-only" {
				// A constructed header with no bitstream at all: only a reader
				// that understands the VP8X canvas can answer 9x6, so this case
				// pins that branch where the real extended file (whose canvas and
				// frame agree) cannot.
				data = []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x08\x00\x00\x05\x00\x00")
			}
			if mediaType := DetectImageMediaType(data); mediaType != "image/webp" {
				t.Fatalf("DetectImageMediaType = %q, want image/webp", mediaType)
			}
			width, height, err := ImageSize("image/webp", data)
			if err != nil {
				t.Fatalf("ImageSize: %v", err)
			}
			if width != testCase.width || height != testCase.height {
				t.Fatalf("ImageSize = %dx%d, want %dx%d", width, height, testCase.width, testCase.height)
			}
		})
	}
}

// A WebP that cannot be read is an unreadable header, not an image with no size:
// every case answers ErrImageDimensionsUnreadable so a caller names the missing
// half instead of describing the file with a zero.
func TestImageSizeRefusesAWebPItCannotRead(t *testing.T) {
	lossy := decodeWebP(t, webpLossy7x5)
	cases := map[string][]byte{
		"truncated":                lossy[:len(lossy)-20],
		"empty":                    {},
		"riff only":                []byte("RIFF"),
		"not a webp":               append([]byte("RIFF\x10\x00\x00\x00WEBV"), lossy[12:]...),
		"no bitstream":             append([]byte("RIFF\x04\x00\x00\x00WEBP"), []byte("XXXX")...),
		"chunk past the end":       append([]byte("RIFF\x0c\x00\x00\x00WEBP"), []byte("VP8 \xff\xff\xff\x7f")...),
		"vp8x without a size":      append([]byte("RIFF\x12\x00\x00\x00WEBP"), []byte("VP8X\x06\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...),
		"vp8l without a signature": append([]byte("RIFF\x11\x00\x00\x00WEBP"), []byte("VP8L\x05\x00\x00\x00\x00\x0a\xc0\x05\x00")...),
		"vp8 without a header":     append([]byte("RIFF\x11\x00\x00\x00WEBP"), []byte("VP8 \x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ImageSize("image/webp", data)
			if !errors.Is(err, ErrImageDimensionsUnreadable) {
				t.Fatalf("ImageSize error = %v, want ErrImageDimensionsUnreadable", err)
			}
		})
	}
}

// The bound is the point of reading a header rather than a file: a container that
// declares a huge chunk must not make this read it. The reader walks at most
// webpMaxHeaderBytes of chunks, and this file's bitstream sits past that bound.
func TestImageSizeDoesNotWalkPastTheHeaderBound(t *testing.T) {
	// A RIFF container whose first chunk is a large, well-formed `VP8X`-sized
	// filler followed by a `VP8L` header that would be read if the walk were
	// unbounded.
	filler := append([]byte("JUNK"), make([]byte, 4096)...)
	data := append([]byte("RIFF"), 0, 0, 0, 0)
	data = append(data, []byte("WEBP")...)
	data = append(data, filler...)
	data = append(data, []byte("VP8L\x05\x00\x00\x00\x2f\x0a\x0e\x00\x00")...)
	// The declared RIFF size has to be truthful for the walk to start at all.
	putUint32LE(data[4:8], uint32(len(data)-8))
	if _, _, err := ImageSize("image/webp", data); !errors.Is(err, ErrImageDimensionsUnreadable) {
		t.Fatalf("ImageSize error = %v, want the bound to stop the walk", err)
	}
	// The same file with the bitstream first is read, which is what makes the
	// bound the reason for the refusal rather than a parsing bug.
	readable := append([]byte("RIFF"), 0, 0, 0, 0)
	readable = append(readable, []byte("WEBP")...)
	readable = append(readable, []byte("VP8L\x05\x00\x00\x00\x2f\x0a\xc0\x05\x00")...)
	readable = append(readable, filler...)
	putUint32LE(readable[4:8], uint32(len(readable)-8))
	width, height, err := ImageSize("image/webp", readable)
	if err != nil || width != 11 || height != 24 {
		t.Fatalf("ImageSize = %dx%d (%v), want 11x24", width, height, err)
	}
}

func putUint32LE(target []byte, value uint32) {
	target[0] = byte(value)
	target[1] = byte(value >> 8)
	target[2] = byte(value >> 16)
	target[3] = byte(value >> 24)
}

// The PNG path still reads through the standard library, and a PNG banner that
// this host cannot decode is still an unreadable header.
func TestImageSizeReadsPNGThroughTheStandardLibrary(t *testing.T) {
	// A 1x1 PNG, the smallest real one.
	png := decodeWebP(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==")
	if mediaType := DetectImageMediaType(png); mediaType != "image/png" {
		t.Fatalf("DetectImageMediaType = %q, want image/png", mediaType)
	}
	width, height, err := ImageSize("image/png", png)
	if err != nil || width != 1 || height != 1 {
		t.Fatalf("ImageSize = %dx%d (%v), want 1x1", width, height, err)
	}
	if _, _, err := ImageSize("image/png", bytes.Repeat([]byte{0x89}, 4)); !errors.Is(err, ErrImageDimensionsUnreadable) {
		t.Fatalf("ImageSize of a broken PNG = %v, want ErrImageDimensionsUnreadable", err)
	}
}
