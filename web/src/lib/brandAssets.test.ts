import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const assetPath = (path: string) => resolve(process.cwd(), "..", path);
const asset = (path: string) => readFileSync(assetPath(path));
const digest = (path: string) => createHash("sha256").update(asset(path)).digest("hex");
const pngSize = (path: string) => {
  const bytes = asset(path);
  expect(bytes.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  expect(bytes[25]).toBe(6); // RGBA keeps the native transparent corners.
  return [bytes.readUInt32BE(16), bytes.readUInt32BE(20)];
};

describe("approved Silken branding", () => {
  it("keeps the approved Apple-rendered desktop icon and installer icon identical", () => {
    expect(pngSize("desktop/build/appicon.png")).toEqual([1024, 1024]);
    expect(digest("desktop/build/appicon.png")).toBe("9871b9efc8d7e189f6916db84fb1982cc9803c8f44aaaa7d257a27de703eb369");
    expect(digest("desktop/build/appicon.icns")).toBe("1e82d34ce91caa996a18d2ecd5fdad4899d3dffee97df300f75aaf0b28c3d37b");
    expect(asset("desktop/build/icon_installer.icns").equals(asset("desktop/build/appicon.icns"))).toBe(true);
    expect(asset("scripts/release/build-desktop.sh").toString()).toContain('cp "$ROOT_DIR/desktop/build/appicon.icns" "$APP_PATH/Contents/Resources/iconfile.icns"');
  });

  it("shares the same UI mark and favicon across the app, website and bootstrap", () => {
    expect(pngSize("assets/brand/icon.png")).toEqual([256, 256]);
    expect(digest("assets/brand/icon.png")).toBe("9ae45861649d6958b417d50258a0b8093e65868ecca82ff9902b400b1e4a9cc2");
    expect(asset("desktop/bootstrap/assets/brand-icon.png").equals(asset("assets/brand/icon.png"))).toBe(true);
    expect(pngSize("web/public/favicon.png")).toEqual([64, 64]);
    expect(asset("website/public/favicon.png").equals(asset("web/public/favicon.png"))).toBe(true);
    for (const path of ["web/index.html", "website/index.html", "website/docs/index.html"]) {
      expect(asset(path).toString()).toContain("favicon.png");
      expect(asset(path).toString()).not.toContain("favicon.svg");
    }
    for (const path of ["web/public/favicon.svg", "website/public/favicon.svg"]) {
      expect(existsSync(assetPath(path))).toBe(false);
    }
  });

  it("preserves the exact eightfold vector construction without TV lettering", () => {
    const document = new DOMParser().parseFromString(asset("desktop/build/appicon.svg").toString(), "image/svg+xml");
    expect(document.querySelector("parsererror")).toBeNull();
    expect(document.querySelectorAll("text")).toHaveLength(0);
    expect(document.querySelectorAll("defs path")).toHaveLength(1);
    const instances = [...document.querySelectorAll("use")];
    expect(instances).toHaveLength(8);
    expect(instances.map((node) => node.getAttribute("transform"))).toEqual(Array.from({ length: 8 }, (_, index) => `rotate(${index * 45} 512 512)`));
    for (const node of instances) {
      expect(node.getAttribute("href")).toBe("#silken-petal");
      expect(node.getAttribute("fill")).toBe("#c2ff75");
    }
  });
});
