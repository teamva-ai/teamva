package store

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

const tinyPNGDataURL = "data:image/png;base64,iVBORw0KGgo="

func TestValidateRunInputAcceptsBoundedPNG(t *testing.T) {
	blocks := []domain.ContentBlock{
		{Type: "text", Text: "inspect"},
		{Type: "image", MimeType: "image/png", Filename: "capture.png", DataURL: tinyPNGDataURL},
	}
	attachments, err := validateRunInput(blocks)
	if err != nil || len(attachments) != 1 || attachments[0].DataURL != tinyPNGDataURL {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	metadata := runAttachmentMetadata(attachments)
	if metadata[0].DataURL != "" {
		t.Fatalf("metadata=%+v", metadata)
	}
}

func TestValidateRunInputRejectsUnsafeImage(t *testing.T) {
	cases := []struct {
		name  string
		block domain.ContentBlock
		code  string
	}{
		{name: "mime", block: domain.ContentBlock{Type: "image", MimeType: "image/svg+xml", Filename: "x.svg", DataURL: "data:image/svg+xml;base64,PHN2Zz4="}, code: "attachment_invalid"},
		{name: "filename", block: domain.ContentBlock{Type: "image", MimeType: "image/png", Filename: "../x.png", DataURL: tinyPNGDataURL}, code: "attachment_invalid"},
		{name: "signature", block: domain.ContentBlock{Type: "image", MimeType: "image/png", Filename: "x.png", DataURL: "data:image/png;base64,SGVsbG8="}, code: "attachment_invalid"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateRunInput([]domain.ContentBlock{test.block})
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != test.code || domainErr.HTTPStatus != http.StatusUnprocessableEntity {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRunInputRejectsInlineImageDataURLInText(t *testing.T) {
	blocks := []domain.ContentBlock{{Type: "text", Text: "prefix DATA:IMAGE/PNG;BASE64,secret"}}
	if _, err := validateRunInput(blocks); err == nil {
		t.Fatal("general run input accepted an inline image data URL in text")
	}
	if err := validateTextOnlyRunInput(blocks); err == nil {
		t.Fatal("text-only run input accepted an inline image data URL")
	}
}

func TestValidateRunInputReportsAttachmentLimits(t *testing.T) {
	tooMany := make([]domain.ContentBlock, 5)
	for index := range tooMany {
		tooMany[index] = domain.ContentBlock{
			Type: "image", MimeType: "image/png", Filename: "capture.png", DataURL: tinyPNGDataURL,
		}
	}
	_, err := validateRunInput(tooMany)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "attachments_too_many" || domainErr.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateRunInputReportsDecodedByteLimits(t *testing.T) {
	oversized := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxRunAttachmentBytes)...)
	_, err := validateRunInput([]domain.ContentBlock{{
		Type: "image", MimeType: "image/png", Filename: "large.png",
		DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(oversized),
	}})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "attachment_too_large" || domainErr.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("err=%v", err)
	}

	medium := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 400*1024)...)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(medium)
	_, err = validateRunInput([]domain.ContentBlock{
		{Type: "image", MimeType: "image/png", Filename: "one.png", DataURL: dataURL},
		{Type: "image", MimeType: "image/png", Filename: "two.png", DataURL: dataURL},
	})
	if !errors.As(err, &domainErr) || domainErr.Code != "attachments_too_large" || domainErr.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("err=%v", err)
	}
}

func TestPublicRunInputStripsInlineImageData(t *testing.T) {
	blocks := publicRunInput([]domain.ContentBlock{{
		Type: "image", MimeType: "image/png", Filename: "capture.png", DataURL: tinyPNGDataURL,
	}})
	if len(blocks) != 1 || blocks[0].DataURL != "" || blocks[0].Filename != "capture.png" {
		t.Fatalf("blocks=%+v", blocks)
	}
}
