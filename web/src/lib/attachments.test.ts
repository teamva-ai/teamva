import { describe, expect, it } from "vitest";
import {
  imageAttachmentBlocks, maximumImageAttachmentBytes, validateImageAttachmentFile
} from "./attachments";

describe("image attachments", () => {
  it("builds API image content blocks", () => {
    expect(imageAttachmentBlocks([{
      filename: "capture.png", mimeType: "image/png",
      dataURL: "data:image/png;base64,iVBORw0KGgo="
    }])).toEqual([{
      type: "image", mime_type: "image/png", filename: "capture.png",
      data_url: "data:image/png;base64,iVBORw0KGgo="
    }]);
  });

  it("enforces file type, count, and byte limits", () => {
    expect(validateImageAttachmentFile({ type: "image/svg+xml", size: 20 }, 0, 0)).toBe("unsupported");
    expect(validateImageAttachmentFile({ type: "image/png", size: maximumImageAttachmentBytes + 1 }, 0, 0)).toBe("too_large");
    expect(validateImageAttachmentFile({ type: "image/png", size: 1 }, 4, 0)).toBe("too_many");
    expect(validateImageAttachmentFile({ type: "image/png", size: 400 * 1024 }, 1, 400 * 1024)).toBe("total_too_large");
    expect(validateImageAttachmentFile({ type: "image/png", size: 20 }, 0, 0)).toBeUndefined();
  });
});
