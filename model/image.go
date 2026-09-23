package model

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	// The image decoders are registered for their config readers only: a header
	// parse is what dimensions need, and a full pixel decode would allocate a
	// buffer per upload for bytes nobody displays here.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
)

// ErrImageDimensionsUnreadable reports an image whose encoded size this host
// cannot read. It is a distinct error because the caller's answer differs from
// "not an image at all": the bytes are a real image of a type this host does not
// parse, and the honest thing to say is which part is missing.
var ErrImageDimensionsUnreadable = errors.New("image dimensions are not readable")

// DetectImageMediaType reports the media type of encoded image bytes, or "" when
// the bytes are not one of the formats this host may send to a provider.
//
// The type comes from the bytes and never from a name or an extension: a
// mislabelled file must not be sent as a format the provider rejects, and a
// renamed non-image must not be treated as an image. WebP is sniffed explicitly
// because http.DetectContentType does not recognise it.
func DetectImageMediaType(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	detected := strings.TrimSpace(strings.Split(http.DetectContentType(raw), ";")[0])
	if detected == "image/webp" {
		return detected
	}
	if len(raw) >= 12 && bytes.Equal(raw[0:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	if SupportedImageMediaType(detected) {
		return detected
	}
	return ""
}

// ImageSize reports the pixel dimensions of an encoded PNG, JPEG, GIF or WebP
// image.
//
// It reads the header, not the pixels: image.DecodeConfig parses exactly the
// bytes a size needs, and WebP -- which the standard library does not decode --
// is read by imageSizeWebP, a bounded header walk. Bytes that are not an image of
// one of those four types, or whose header is truncated or contradictory, are
// reported as ErrImageDimensionsUnreadable rather than described with a guess.
func ImageSize(mediaType string, raw []byte) (int, int, error) {
	if mediaType == "image/webp" {
		return imageSizeWebP(raw)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrImageDimensionsUnreadable, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return 0, 0, fmt.Errorf("%w: %s header reports %dx%d", ErrImageDimensionsUnreadable, format, config.Width, config.Height)
	}
	return config.Width, config.Height, nil
}

// imageSizeWebP reads a WebP's canvas size from its RIFF container header.
//
// WebP has three bitstreams and each states its size differently, so this walks
// the chunk list and reads whichever of them is present:
//
//   - `VP8X` (extended format) states the canvas as 24-bit little-endian values
//     minus one, in the chunk's first ten bytes;
//   - `VP8L` (lossless) packs both dimensions, minus one, into 4 bytes of a
//     14-bit field each, after the 0x2f signature;
//   - `VP8 ` (lossy) carries the size in the frame header's 16-bit fields, whose
//     seven low bits are a scaling hint rather than part of the dimension.
//
// Only the bytes a size needs are read: no pixel data is touched, and the walk is
// bounded by the container's own declared size and by webpMaxHeaderBytes, so a
// hostile file cannot make this read more than a few dozen bytes. Anything that
// does not add up -- a RIFF header too short, a chunk length past the end of the
// buffer, a zero dimension -- is ErrImageDimensionsUnreadable.
func imageSizeWebP(raw []byte) (int, int, error) {
	const (
		// webpMaxHeaderBytes bounds the walk: the largest header the three
		// bitstreams need is well under this, and the point of the bound is that
		// a file claiming a huge chunk cannot make this read the whole thing.
		webpMaxHeaderBytes = 64
	)
	if len(raw) < 12 || !bytes.Equal(raw[0:4], []byte("RIFF")) || !bytes.Equal(raw[8:12], []byte("WEBP")) {
		return 0, 0, fmt.Errorf("%w: not a WebP container", ErrImageDimensionsUnreadable)
	}
	// The RIFF size field counts everything after the first eight bytes, so the
	// container's declared end is 8 + size. The chunk list starts after
	// "RIFF<u32 size>WEBP" and runs to that end, capped at the bound as well, so
	// the walk reads a header and never a whole file.
	end := 8 + int(binary.LittleEndian.Uint32(raw[4:8]))
	if end > len(raw) {
		return 0, 0, fmt.Errorf("%w: the WebP container is truncated", ErrImageDimensionsUnreadable)
	}
	if limit := 12 + webpMaxHeaderBytes; end > limit {
		end = limit
	}
	for offset := 12; offset+8 <= end; {
		kind := raw[offset : offset+4]
		size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		body := offset + 8
		if size < 0 || body+size > len(raw) {
			return 0, 0, fmt.Errorf("%w: chunk %q declares %d bytes beyond the file", ErrImageDimensionsUnreadable, kind, size)
		}
		switch string(kind) {
		case "VP8X":
			if size < 10 {
				return 0, 0, fmt.Errorf("%w: VP8X chunk is %d bytes", ErrImageDimensionsUnreadable, size)
			}
			// VP8X starts with one flags byte and three reserved bytes; the
			// canvas dimensions follow as 24-bit little-endian values minus one.
			width := 1 + int(raw[body+4]) + int(raw[body+5])<<8 + int(raw[body+6])<<16
			height := 1 + int(raw[body+7]) + int(raw[body+8])<<8 + int(raw[body+9])<<16
			return positiveDimensions(width, height, "VP8X")
		case "VP8L":
			if size < 5 || raw[body] != 0x2f {
				return 0, 0, fmt.Errorf("%w: VP8L chunk has no lossless signature", ErrImageDimensionsUnreadable)
			}
			bits := binary.LittleEndian.Uint32(raw[body+1 : body+5])
			// 14 bits of width, then 14 bits of height, each stored minus one.
			width := 1 + int(bits&0x3fff)
			height := 1 + int((bits>>14)&0x3fff)
			return positiveDimensions(width, height, "VP8L")
		case "VP8 ":
			if size < 10 {
				return 0, 0, fmt.Errorf("%w: VP8 chunk is %d bytes", ErrImageDimensionsUnreadable, size)
			}
			// The lossy frame header starts at the first byte equal to 0x9d 0x01
			// 0x2a; the three bytes after it are the 16-bit dimensions, each
			// masked to its 14-bit value.
			start := -1
			for index := body; index+3 <= body+size && index+3 <= len(raw); index++ {
				if raw[index] == 0x9d && raw[index+1] == 0x01 && raw[index+2] == 0x2a {
					start = index + 3
					break
				}
			}
			if start < 0 || start+4 > len(raw) {
				return 0, 0, fmt.Errorf("%w: VP8 frame header was not found", ErrImageDimensionsUnreadable)
			}
			width := int(binary.LittleEndian.Uint16(raw[start:start+2]) & 0x3fff)
			height := int(binary.LittleEndian.Uint16(raw[start+2:start+4]) & 0x3fff)
			return positiveDimensions(width, height, "VP8")
		}
		// Chunks are padded to an even length.
		offset = body + size
		if size%2 == 1 {
			offset++
		}
	}
	return 0, 0, fmt.Errorf("%w: the WebP container declares no image bitstream", ErrImageDimensionsUnreadable)
}

// positiveDimensions rejects a size a header states as zero: a zero is a header
// this host did not read correctly, not an image with no pixels.
func positiveDimensions(width, height int, bitstream string) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("%w: %s reports %dx%d", ErrImageDimensionsUnreadable, bitstream, width, height)
	}
	return width, height, nil
}
