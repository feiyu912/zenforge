// Package viewimage adds a view_image tool: the model can ask to look at an
// image in the workspace. The tool returns the image as message content and
// leaves the text answer to the model, so an image never masquerades as a
// tool result string.
package viewimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	"github.com/feiyu912/zenforge/workspace"
)

// Name is the tool name.
const Name = "view_image"

// ImageMetadataKey is where the decoded image travels on the tool result.
// The agent copies it onto the next model message, which is the only way a
// tool result can carry non-text content.
const ImageMetadataKey = "zenforge.images"

// Config configures the tool.
type Config struct {
	// Workspace confines the paths the tool may read. Required.
	Workspace workspace.Workspace
	// MaxBytes bounds one image. Zero uses model.MaxImageBytes.
	MaxBytes int64
}

// New builds the tool.
func New(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("view_image requires a workspace")
	}
	maxBytes := config.MaxBytes
	if maxBytes <= 0 {
		maxBytes = model.MaxImageBytes
	}
	return tools.New(Name,
		"Show an image file to the model. Use it for screenshots, diagrams, and design references in the workspace.",
		func(ctx context.Context, input Input) (Output, error) {
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
			path := strings.TrimSpace(input.Path)
			if path == "" {
				return Output{}, fmt.Errorf("%w: path is required", tool.ErrInvalidArguments)
			}
			raw, err := config.Workspace.Read(ctx, path)
			if err != nil {
				if errors.Is(err, workspace.ErrUnsupportedFile) {
					// The workspace refuses binary reads by default; an
					// image is binary by definition, so the hint matters.
					return Output{}, fmt.Errorf("%w: %s could not be read as an image (%v); the workspace may have binary reads disabled", tool.ErrInvalidArguments, path, err)
				}
				return Output{}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
			}
			if int64(len(raw)) > maxBytes {
				return Output{}, fmt.Errorf("%w: %s is %d bytes, over the %d byte limit", tool.ErrInvalidArguments, path, len(raw), maxBytes)
			}
			// The media type comes from the bytes, not the extension: a
			// mislabelled file must not be sent as a format the provider
			// will reject, and a renamed executable must not be treated as
			// an image.
			mediaType := detectMediaType(raw)
			if mediaType == "" {
				return Output{}, fmt.Errorf("%w: %s is not a PNG, JPEG, GIF, or WebP image", tool.ErrInvalidArguments, path)
			}
			relative := filepath.ToSlash(filepath.Clean(path))
			image := model.Image{
				MediaType: mediaType,
				Data:      raw,
				Path:      relative,
				Detail:    strings.TrimSpace(input.Detail),
			}
			return Output{
				Path:      relative,
				MediaType: mediaType,
				Bytes:     len(raw),
				Note:      fmt.Sprintf("showing %s (%s, %d bytes)", relative, mediaType, len(raw)),
				Images:    []model.Image{image},
			}, nil
		})
}

// Input is the tool's arguments.
type Input struct {
	Path string `json:"path" description:"Workspace-relative path of the image"`
	// Detail optionally asks the provider for low or high fidelity.
	Detail string `json:"detail,omitempty" description:"Optional fidelity hint: low, high, or auto"`
}

// Output is the tool's result.
type Output struct {
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Bytes     int    `json:"bytes"`
	Note      string `json:"note"`
	// Images carries the decoded images to the agent. It is unexported from
	// JSON on purpose: the bytes belong in the message, not in the text.
	Images []model.Image `json:"-"`
}

// ToolMetadata exposes the images to the agent through the tools package's
// metadata carrier, which is how a typed tool result carries non-text
// content.
func (o Output) ToolMetadata() map[string]any {
	if len(o.Images) == 0 {
		return nil
	}
	return map[string]any{ImageMetadataKey: o.Images}
}

// detectMediaType sniffs the supported formats from their magic bytes.
// http.DetectContentType is used first, then corrected for WebP, which it
// reports as application/octet-stream.
// detectMediaType is the model package's sniffer: the console's attachment
// admission uses the same one (ADR 0138), so a type this tool would accept and a
// type the console would store cannot disagree.
func detectMediaType(raw []byte) string {
	return model.DetectImageMediaType(raw)
}

// MarshalJSON keeps the metadata out of the model-visible JSON: the image
// travels in the message, and repeating the bytes in the tool result text
// would double the cost of every later request.
func (o Output) MarshalJSON() ([]byte, error) {
	type wire struct {
		Path      string `json:"path"`
		MediaType string `json:"mediaType"`
		Bytes     int    `json:"bytes"`
		Note      string `json:"note"`
	}
	return json.Marshal(wire{Path: o.Path, MediaType: o.MediaType, Bytes: o.Bytes, Note: o.Note})
}
