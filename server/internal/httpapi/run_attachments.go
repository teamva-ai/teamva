package httpapi

import (
	"strings"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func validateRequestedAttachments(items []domain.ContentBlock) error {
	for _, item := range items {
		if item.Type != "image" || item.Text != "" || item.Internal {
			return domain.Invalid("attachments 只能包含公开图片附件")
		}
	}
	return nil
}

func validateTextOnlyContent(items []domain.ContentBlock) error {
	for _, item := range items {
		if item.Type != "text" || item.MimeType != "" || item.Filename != "" || item.DataURL != "" {
			return domain.Invalid("该输入只支持文本内容")
		}
		text := strings.ToLower(item.Text)
		if strings.Contains(text, "data:image/") && strings.Contains(text, ";base64,") {
			return domain.Invalid("文本输入不能内嵌图片 data URL")
		}
	}
	return nil
}
