// thumbnail: makes the small picture of an asset's image and stores it in the
// asset's `thumbnail` field — a field the rules refuse to clients, so this
// function is the only way it ever gets written (chapter 14). The app starts it
// after an upload; it is async (a job the app doesn't wait for) and `retry:`
// repeats it when the storage is busy.
//
// The image libraries are pure JavaScript (a function runs with no permission
// to read files, so a library that loads WebAssembly or native code from disk
// can't work here): upng-js reads and writes PNG, jpeg-js reads JPEG.
// deno-lint-ignore-file no-explicit-any
import UPNG from "npm:upng-js@2.1.0";
import jpeg from "npm:jpeg-js@0.4.4";
import { makeThumbnailer } from "../lib/thumbnail.ts";

type Raster = { width: number; height: number; data: Uint8Array };

function decode(bytes: Uint8Array): Raster {
  const isPng = bytes[0] === 0x89 && bytes[1] === 0x50;
  if (isPng) {
    const img = (UPNG as any).decode(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
    return { width: img.width, height: img.height, data: new Uint8Array((UPNG as any).toRGBA8(img)[0]) };
  }
  const img = (jpeg as any).decode(bytes, { useTArray: true });
  return { width: img.width, height: img.height, data: img.data };
}

// Averages the source pixels each target pixel covers (a box filter): plenty
// for a thumbnail.
function shrink(src: Raster, width: number): Raster {
  if (src.width <= width) return src;
  const w = width;
  const h = Math.max(1, Math.round((src.height * w) / src.width));
  const out = new Uint8Array(w * h * 4);
  for (let y = 0; y < h; y++) {
    const y0 = Math.floor((y * src.height) / h), y1 = Math.max(y0 + 1, Math.floor(((y + 1) * src.height) / h));
    for (let x = 0; x < w; x++) {
      const x0 = Math.floor((x * src.width) / w), x1 = Math.max(x0 + 1, Math.floor(((x + 1) * src.width) / w));
      const sum = [0, 0, 0, 0];
      let n = 0;
      for (let yy = y0; yy < y1; yy++) {
        for (let xx = x0; xx < x1; xx++) {
          const i = (yy * src.width + xx) * 4;
          for (let c = 0; c < 4; c++) sum[c] += src.data[i + c];
          n++;
        }
      }
      for (let c = 0; c < 4; c++) out[(y * w + x) * 4 + c] = Math.round(sum[c] / n);
    }
  }
  return { width: w, height: h, data: out };
}

export default makeThumbnailer((bytes, width) => {
  const small = shrink(decode(bytes), width);
  return Promise.resolve(new Uint8Array((UPNG as any).encode([small.data.buffer], small.width, small.height, 0)));
});
