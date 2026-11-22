/** Colour helpers: generate a navy-50…950 scale from a primary colour, contrast ratios. */

export const SHADES = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950] as const;

export const DEFAULT_SCALE: Record<number, string> = {
  50: "#eef2f9", 100: "#d9e1f0", 200: "#b4c3e0", 300: "#8299c6", 400: "#5372ac", 500: "#34558f",
  600: "#234274", 700: "#18335e", 800: "#0c234b", 900: "#081a39", 950: "#050f24",
};

export const isHex = (v: string) => /^#[0-9a-f]{6}$/i.test(v);

export function hexToRgb(hex: string): [number, number, number] {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
export function rgbToHex([r, g, b]: [number, number, number]) {
  return `#${[r, g, b].map((v) => Math.round(Math.max(0, Math.min(255, v))).toString(16).padStart(2, "0")).join("")}`;
}
const mix = (a: [number, number, number], b: [number, number, number], t: number): [number, number, number] => [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t];

/** primary = shade 800. Lighter shades blend towards white, darker towards black. */
export function generateScale(primary: string): Record<number, string> {
  if (!isHex(primary) || primary.toLowerCase() === DEFAULT_SCALE[800]) return DEFAULT_SCALE;
  const base = hexToRgb(primary);
  const white: [number, number, number] = [255, 255, 255];
  const black: [number, number, number] = [0, 0, 0];
  const light: Record<number, number> = { 50: 0.93, 100: 0.84, 200: 0.68, 300: 0.5, 400: 0.34, 500: 0.2, 600: 0.1, 700: 0.04 };
  const dark: Record<number, number> = { 900: 0.28, 950: 0.5 };
  const out: Record<number, string> = {};
  for (const s of SHADES) {
    out[s] = s === 800 ? primary.toLowerCase() : s in light ? rgbToHex(mix(base, white, light[s]!)) : rgbToHex(mix(base, black, dark[s]!));
  }
  return out;
}

export function darken(hex: string, amount: number) {
  return rgbToHex(mix(hexToRgb(hex), [0, 0, 0], amount));
}

function lum(hex: string) {
  const [r, g, b] = hexToRgb(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
export function contrast(a: string, b: string) {
  if (!isHex(a) || !isHex(b)) return 21;
  const [l1, l2] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
  return (l1 + 0.05) / (l2 + 0.05);
}
