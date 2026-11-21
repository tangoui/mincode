package computer

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"

	"golang.org/x/image/draw"
)

// Vision models downsample oversized images on their side, which silently
// breaks the screenshot-pixel coordinate frame. Keep captures within common
// model limits so the coordinates the model sees are the ones we map back.
const (
	maxScreenshotLongEdge = 1568
	maxScreenshotPixels   = 1_150_000
)

func scaledScreenshotSize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	scale := 1.0
	if longEdge := math.Max(float64(width), float64(height)); longEdge > maxScreenshotLongEdge {
		scale = maxScreenshotLongEdge / longEdge
	}
	if pixels := float64(width) * float64(height) * scale * scale; pixels > maxScreenshotPixels {
		scale *= math.Sqrt(maxScreenshotPixels / pixels)
	}
	if scale >= 1 {
		return width, height
	}
	return max(1, int(math.Floor(float64(width)*scale))), max(1, int(math.Floor(float64(height)*scale)))
}

// fitCaptureForModel downscales an oversized PNG capture and updates the
// screenshot coordinate frame to the delivered image dimensions.
func fitCaptureForModel(capture Capture) (Capture, error) {
	targetWidth, targetHeight := scaledScreenshotSize(capture.Width, capture.Height)
	if targetWidth == capture.Width && targetHeight == capture.Height {
		return capture, nil
	}
	raw, err := base64.StdEncoding.DecodeString(capture.Data)
	if err != nil {
		return Capture{}, fmt.Errorf("decode desktop capture: %w", err)
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Capture{}, fmt.Errorf("decode desktop capture image: %w", err)
	}
	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(target, target.Bounds(), source, source.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&encoded, target); err != nil {
		return Capture{}, fmt.Errorf("encode scaled desktop capture: %w", err)
	}

	capture.MimeType = "image/png"
	capture.Data = base64.StdEncoding.EncodeToString(encoded.Bytes())
	capture.Width, capture.Height = targetWidth, targetHeight
	capture.Display.PixelWidth, capture.Display.PixelHeight = targetWidth, targetHeight
	if capture.Display.PointWidth > 0 {
		capture.Display.ScaleX = float64(targetWidth) / capture.Display.PointWidth
	}
	if capture.Display.PointHeight > 0 {
		capture.Display.ScaleY = float64(targetHeight) / capture.Display.PointHeight
	}
	return capture, nil
}
