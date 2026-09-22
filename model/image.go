package model

import (
	"bytes"
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

// ImageSize reports the pixel dimensions of an encoded PNG, JPEG or GIF image.
//
// It reads the header, not the pixels: image.DecodeConfig parses exactly the
// bytes a size needs. A WebP is a real image whose dimensions this host cannot
// read -- no decoder for its header is linked -- and that is reported as
// ErrImageDimensionsUnreadable so a caller can name the missing capability
// instead of claiming the bytes are not an image.
func ImageSize(mediaType string, raw []byte) (int, int, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		if mediaType == "image/webp" {
			return 0, 0, fmt.Errorf("%w: WebP", ErrImageDimensionsUnreadable)
		}
		return 0, 0, fmt.Errorf("%w: %v", ErrImageDimensionsUnreadable, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return 0, 0, fmt.Errorf("%w: %s header reports %dx%d", ErrImageDimensionsUnreadable, format, config.Width, config.Height)
	}
	return config.Width, config.Height, nil
}
