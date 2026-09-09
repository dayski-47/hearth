import "@testing-library/jest-dom/vitest";

// jsdom lacks these; components under test touch them.
if (!("ResizeObserver" in globalThis)) {
  class RO {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = RO;
}

// jsdom ships getContext only as a "not implemented" stub that throws; xterm
// touches a 2d context at module load to parse CSS colours. A flat replacement
// keeps that path quiet in tests.
HTMLCanvasElement.prototype.getContext = (() => ({
  fillStyle: "",
  fillRect: () => {},
  getImageData: () => ({ data: new Uint8ClampedArray(4) }),
  measureText: () => ({ width: 0 }),
  createLinearGradient: () => ({ addColorStop: () => {} }),
})) as unknown as typeof HTMLCanvasElement.prototype.getContext;

// jsdom's Range implements no layout, so it has neither getClientRects nor
// getBoundingClientRect. CodeMirror measures text by range whenever the doc
// changes; give it empty rects so the measure pass is a no-op instead of a
// throw.
const emptyRect = () => ({
  x: 0,
  y: 0,
  width: 0,
  height: 0,
  top: 0,
  right: 0,
  bottom: 0,
  left: 0,
  toJSON: () => ({}),
});
if (typeof Range !== "undefined") {
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = function getClientRects() {
      return Object.assign([], {
        item: () => null,
      }) as unknown as DOMRectList;
    };
  }
  if (!Range.prototype.getBoundingClientRect) {
    Range.prototype.getBoundingClientRect =
      emptyRect as unknown as () => DOMRect;
  }
}

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
