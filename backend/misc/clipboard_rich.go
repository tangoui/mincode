package misc

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"mincode/app/backend/core"
)

const (
	maxRichClipboardHTMLBytes  = 64 * 1024 * 1024
	maxRichClipboardImageBytes = 8 * 1024 * 1024
)

type clipboardRichArg struct {
	Text          string `json:"text"`
	HTML          string `json:"html"`
	ImageData     string `json:"imageData"`
	ImageMimeType string `json:"imageMimeType"`
}

func init() {
	core.Register("clipboard_write_rich", clipboardWriteRichHandler)
}

func clipboardWriteRichHandler(_, argsJSON string) (string, error) {
	var arg clipboardRichArg
	if err := core.DecodeArgs(argsJSON, &arg); err != nil {
		return "", err
	}
	if len(arg.HTML) > maxRichClipboardHTMLBytes {
		return "", errors.New("clipboard rich HTML exceeds the size limit")
	}
	if !strings.HasPrefix(strings.ToLower(arg.ImageMimeType), "image/") {
		return "", errors.New("clipboard image MIME type is invalid")
	}
	image, err := base64.StdEncoding.DecodeString(arg.ImageData)
	if err != nil {
		return "", fmt.Errorf("decode clipboard image: %w", err)
	}
	if len(image) == 0 {
		return "", errors.New("clipboard image is empty")
	}
	if len(image) > maxRichClipboardImageBytes {
		return "", errors.New("clipboard image exceeds the size limit")
	}
	if err := clipboardWriteRich(arg.Text, arg.HTML, arg.ImageMimeType, image); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}
