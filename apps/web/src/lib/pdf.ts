// pdf.js, loaded on demand (only when a PDF is opened): the library, its
// worker, and where its fonts, CJK character maps and image decoders are
// (copied to dist/pdfjs/ by vite.config.ts).
import * as pdfjs from "pdfjs-dist";
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl;

const base = import.meta.env.BASE_URL + "pdfjs/";

/** Opens a PDF from its bytes. Its scripts never run (pdf.js does not run
 *  them without a viewer that asks it to). */
export function openPdf(data: Uint8Array) {
  return pdfjs.getDocument({
    data,
    cMapUrl: base + "cmaps/",
    cMapPacked: true,
    standardFontDataUrl: base + "standard_fonts/",
    wasmUrl: base + "wasm/",
  }).promise;
}

export const TextLayer = pdfjs.TextLayer;
export type PDFDocumentProxy = pdfjs.PDFDocumentProxy;
export type PDFPageProxy = pdfjs.PDFPageProxy;
