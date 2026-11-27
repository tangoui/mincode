package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"mincode/app/backend/core"
)

const (
	localThumbnailEdge      = 512
	maxLocalThumbnailPixels = 24_000_000
)

var localThumbnailSlot = make(chan struct{}, 1)

func handleLocalImageThumbnail(ctx context.Context, _, argsJSON string) (string, error) {
	var args localPathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path, original, err := inspectLocalPath(args.Path)
	if err != nil {
		return "", err
	}
	if !original.Mode().IsRegular() || original.Size() > maxExternalPreviewBytes {
		return "", errors.New("expected a regular image file of at most 20 MiB")
	}

	select {
	case localThumbnailSlot <- struct{}{}:
		defer func() { <-localThumbnailSlot }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	file, err := openLocalThumbnailSource(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !current.Mode().IsRegular() || !os.SameFile(original, current) || current.Size() > maxExternalPreviewBytes {
		return "", errors.New("image file changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxExternalPreviewBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxExternalPreviewBytes {
		return "", errors.New("image file exceeds 20 MiB")
	}
	return encodeLocalThumbnail(data)
}

func encodeLocalThumbnail(data []byte) (string, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxLocalThumbnailPixels/config.Height {
		return "", errors.New("image dimensions are too large")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if edge := max(width, height); edge > localThumbnailEdge {
		target := image.NewRGBA(image.Rect(0, 0, max(1, width*localThumbnailEdge/edge), max(1, height*localThumbnailEdge/edge)))
		draw.ApproxBiLinear.Scale(target, target.Bounds(), source, source.Bounds(), draw.Src, nil)
		source = target
	}
	var preview bytes.Buffer
	if err := png.Encode(&preview, source); err != nil {
		return "", err
	}
	return core.EncodeResult("data:image/png;base64," + base64.StdEncoding.EncodeToString(preview.Bytes()))
}
