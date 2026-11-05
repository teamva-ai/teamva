package store

import (
	"net/http"
	"strings"

	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	maxRunAttachments       = nodeprotocol.MaxRunAttachments
	maxRunAttachmentBytes   = nodeprotocol.MaxRunAttachmentBytes
	maxRunAttachmentsBytes  = nodeprotocol.MaxRunAttachmentsBytes
	maxRunAttachmentNameLen = nodeprotocol.MaxRunAttachmentNameLen
)

func validateRunInput(blocks []domain.ContentBlock) ([]nodeprotocol.Attachment, error) {
	return validateRunInputOptions(blocks)
}

func validateTextOnlyRunInput(blocks []domain.ContentBlock) error {
	for _, block := range blocks {
		if block.Type != "text" || block.MimeType != "" || block.Filename != "" || block.DataURL != "" {
			return domain.Invalid("该运行输入只支持文本内容")
		}
		if containsInlineImageDataURL(block.Text) {
			return domain.Invalid("文本输入不能内嵌图片 data URL")
		}
	}
	return nil
}

func validateRunInputOptions(blocks []domain.ContentBlock) ([]nodeprotocol.Attachment, error) {
	attachments := make([]nodeprotocol.Attachment, 0)
	totalBytes := 0
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Type != strings.TrimSpace(block.Type) {
				return nil, domain.Invalid("运行输入内容类型无效")
			}
			if block.MimeType != "" || block.Filename != "" || block.DataURL != "" {
				return nil, invalidRunAttachment("文本输入包含附件字段")
			}
			if containsInlineImageDataURL(block.Text) {
				return nil, domain.Invalid("文本输入不能内嵌图片 data URL")
			}
			if len(block.Text) > nodeprotocol.MaxCommandPayloadBytes {
				return nil, domain.Invalid("运行输入文本过长")
			}
		case "image":
			if block.Type != strings.TrimSpace(block.Type) {
				return nil, invalidRunAttachment("图片附件类型无效")
			}
			if len(attachments) >= maxRunAttachments {
				return nil, &domain.Error{
					Code: "attachments_too_many", Message: "一次最多上传 4 张图片",
					HTTPStatus: http.StatusRequestEntityTooLarge,
					Details:    map[string]any{"max_attachments": maxRunAttachments},
				}
			}
			if block.Internal || block.Text != "" {
				return nil, invalidRunAttachment("图片附件不能是内部控制消息或包含文本")
			}
			mimeType := strings.ToLower(strings.TrimSpace(block.MimeType))
			filename := strings.TrimSpace(block.Filename)
			if filename == "" || len(filename) > maxRunAttachmentNameLen || strings.ContainsAny(filename, "/\\\x00") {
				return nil, invalidRunAttachment("图片文件名无效")
			}
			prefix := "data:" + mimeType + ";base64,"
			if !strings.HasPrefix(block.DataURL, prefix) {
				return nil, invalidRunAttachment("图片必须使用匹配 MIME 的 base64 data URL")
			}
			attachment := nodeprotocol.Attachment{MimeType: mimeType, Filename: filename, DataURL: block.DataURL}
			decodedBytes, err := nodeprotocol.DecodedAttachmentBytes(attachment)
			if err != nil {
				return nil, invalidRunAttachment("图片 base64 数据无效")
			}
			if decodedBytes > maxRunAttachmentBytes {
				return nil, runAttachmentTooLarge(maxRunAttachmentBytes)
			}
			totalBytes += decodedBytes
			if totalBytes > maxRunAttachmentsBytes {
				return nil, runAttachmentsTooLarge(maxRunAttachmentsBytes)
			}
			if err := nodeprotocol.ValidateAttachments([]nodeprotocol.Attachment{attachment}); err != nil {
				return nil, invalidRunAttachment("图片内容与 MIME 类型不匹配")
			}
			attachments = append(attachments, attachment)
		default:
			return nil, domain.Invalid("运行输入包含不支持的内容类型")
		}
	}
	return attachments, nil
}

func containsInlineImageDataURL(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "data:image/") && strings.Contains(value, ";base64,")
}

func runAttachmentMetadata(items []nodeprotocol.Attachment) []nodeprotocol.Attachment {
	result := make([]nodeprotocol.Attachment, len(items))
	for index, item := range items {
		result[index] = nodeprotocol.Attachment{MimeType: item.MimeType, Filename: item.Filename}
	}
	return result
}

func contentBlockText(blocks []domain.ContentBlock) string {
	var value strings.Builder
	for _, block := range blocks {
		if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		if value.Len() > 0 {
			value.WriteString("\n")
		}
		value.WriteString(block.Text)
	}
	return strings.TrimSpace(value.String())
}

func publicRun(item domain.Run) domain.Run {
	item.Input = publicRunInput(item.Input)
	return item
}

func publicRunInput(blocks []domain.ContentBlock) []domain.ContentBlock {
	result := make([]domain.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "image" {
			block.MimeType = strings.ToLower(strings.TrimSpace(block.MimeType))
			block.Filename = strings.TrimSpace(block.Filename)
			block.DataURL = ""
		}
		result = append(result, block)
	}
	return result
}

func invalidRunAttachment(message string) error {
	return &domain.Error{Code: "attachment_invalid", Message: message, HTTPStatus: http.StatusUnprocessableEntity}
}

func runAttachmentTooLarge(maxBytes int) error {
	return &domain.Error{
		Code: "attachment_too_large", Message: "单张图片不能超过 512 KiB",
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Details:    map[string]any{"max_bytes": maxBytes},
	}
}

func runAttachmentsTooLarge(maxBytes int) error {
	return &domain.Error{
		Code: "attachments_too_large", Message: "图片附件总大小不能超过 768 KiB",
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Details:    map[string]any{"max_bytes": maxBytes},
	}
}
