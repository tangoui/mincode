package misc

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestClipboardWriteRichHandlerRejectsInvalidImagesBeforePlatformWrite(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "invalid MIME type",
			args: `{"imageData":"aGVsbG8=","imageMimeType":"text/plain"}`,
			want: "MIME type is invalid",
		},
		{
			name: "invalid base64",
			args: `{"imageData":"%%%","imageMimeType":"image/png"}`,
			want: "decode clipboard image",
		},
		{
			name: "empty image",
			args: `{"imageData":"","imageMimeType":"image/png"}`,
			want: "clipboard image is empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := clipboardWriteRichHandler("", test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("clipboardWriteRichHandler() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestClipboardWriteRichHandlerRejectsOversizedImage(t *testing.T) {
	image := make([]byte, maxRichClipboardImageBytes+1)
	args := `{"imageData":"` + base64.StdEncoding.EncodeToString(image) + `","imageMimeType":"image/png"}`
	_, err := clipboardWriteRichHandler("", args)
	if err == nil || !strings.Contains(err.Error(), "exceeds the size limit") {
		t.Fatalf("clipboardWriteRichHandler() error = %v, want size-limit error", err)
	}
}
