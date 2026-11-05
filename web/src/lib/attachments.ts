import type { ContentBlock } from "./types";

export const allowedImageAttachmentTypes = new Set([
  "image/png", "image/jpeg", "image/gif", "image/webp"
]);
export const maximumImageAttachmentCount = 4;
export const maximumImageAttachmentBytes = 512 * 1024;
export const maximumImageAttachmentsBytes = 768 * 1024;

export interface ImageAttachmentDraft {
  filename: string;
  mimeType: string;
  dataURL: string;
}

export type ImageAttachmentValidation =
  | "unsupported"
  | "too_large"
  | "total_too_large"
  | "too_many";

export function validateImageAttachmentFile(
  file: Pick<File, "type" | "size">,
  currentCount: number,
  currentBytes: number
): ImageAttachmentValidation | undefined {
  if (currentCount >= maximumImageAttachmentCount) return "too_many";
  if (!allowedImageAttachmentTypes.has(file.type)) return "unsupported";
  if (file.size <= 0 || file.size > maximumImageAttachmentBytes) return "too_large";
  if (currentBytes + file.size > maximumImageAttachmentsBytes) return "total_too_large";
  return undefined;
}

export function imageAttachmentBlocks(items: ImageAttachmentDraft[]): ContentBlock[] {
  return items.map((item) => ({
    type: "image",
    mime_type: item.mimeType,
    filename: item.filename,
    data_url: item.dataURL
  }));
}
