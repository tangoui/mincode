package ai

import "testing"

const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestNormalizedImageAttachmentRejectsEmptyAndInvalidData(t *testing.T) {
	t.Parallel()

	for _, data := range []string{"", "data:image/png;base64,", "not-base64", "aGVsbG8="} {
		if _, ok := normalizedImageAttachment(aiAttachment{
			Kind:     "image",
			MimeType: "image/png",
			Data:     data,
		}); ok {
			t.Fatalf("expected invalid image data %q to be rejected", data)
		}
	}
}

func TestNormalizedImageAttachmentUnwrapsDataURL(t *testing.T) {
	t.Parallel()

	image, ok := normalizedImageAttachment(aiAttachment{
		Kind:     "image",
		MimeType: "image/jpeg",
		Data:     "data:image/jpeg;base64," + testPNGBase64,
	})
	if !ok {
		t.Fatal("expected valid data URL image to be accepted")
	}
	if image.MimeType != "image/png" {
		t.Fatalf("unexpected MIME type: %q", image.MimeType)
	}
	if image.Data != testPNGBase64 {
		t.Fatalf("expected raw base64 payload, got %q", image.Data)
	}
}

func TestInvalidHistoricalImageDoesNotCreateOpenAIImageContent(t *testing.T) {
	t.Parallel()

	message := chatMessage{
		Role:    "user",
		Content: "Nihao",
		Attachments: []aiAttachment{{
			Kind:     "image",
			MimeType: "image/png",
			Data:     "data:image/png;base64,",
		}},
	}

	payload := openaiMessageJSON(&message)
	content, ok := payload["content"].(string)
	if !ok {
		t.Fatalf("expected text-only content after invalid image removal, got %#v", payload["content"])
	}
	if content != "Nihao" {
		t.Fatalf("unexpected content: %q", content)
	}
}
