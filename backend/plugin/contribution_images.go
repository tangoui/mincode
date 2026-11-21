package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"

	"mincode/app/plugins/api/svgsafe"
)

// Shared image validation for contribution points whose assets include
// icons or pictures (fileIcons, walkthroughs). The returned bytes are the
// canonical asset that is hashed and served: SVG re-serialized through the
// svgsafe allowlist (reject, never strip) and PNG dimension-checked before
// decoding, then re-encoded without ancillary chunks. Images are only ever
// rendered in image context on the frontend.
const (
	contributionImageMimeSVG = "image/svg+xml"
	contributionImageMimePNG = "image/png"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// contributionImageLimits bounds one point's images, read from its contract
// limits (pngMaxDimension, svgMax*) and assets.maxBytes.
type contributionImageLimits struct {
	PNGMaxDimension int
	SVG             svgsafe.Limits
}

// validateContributionImage validates an .svg or .png asset and returns its
// canonical bytes and mime type. ext is the lower-case extension with dot.
func validateContributionImage(ext string, data []byte, limits contributionImageLimits) ([]byte, string, error) {
	switch ext {
	case ".svg":
		canonical, err := svgsafe.Sanitize(data, limits.SVG)
		if err != nil {
			return nil, "", err
		}
		return canonical, contributionImageMimeSVG, nil
	case ".png":
		canonical, err := canonicalContributionPNG(data, limits.PNGMaxDimension)
		if err != nil {
			return nil, "", err
		}
		return canonical, contributionImageMimePNG, nil
	default:
		return nil, "", errors.New("must be an .svg or .png file")
	}
}

func canonicalContributionPNG(data []byte, maxDimension int) ([]byte, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errors.New("png: not a PNG file")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("png: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxDimension || config.Height > maxDimension {
		return nil, fmt.Errorf("png: dimensions %dx%d exceed %dx%d", config.Width, config.Height, maxDimension, maxDimension)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("png: %w", err)
	}
	var buffer bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buffer, decoded); err != nil {
		return nil, fmt.Errorf("png: re-encode: %w", err)
	}
	return buffer.Bytes(), nil
}
