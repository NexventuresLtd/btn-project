import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { NAMESPACES } from "./ns";
import { en as enT } from "./en";
import { am as amT } from "./am";
import { rw as rwT } from "./rw";
import { fr as frT } from "./fr";
import { CATALOGS, tr, setLang, setOverrides, trx } from "./index";
import { HELP_GUIDES, TOUR_STEPS } from "@/components/guide/steps";

const en: Record<string, string> = enT;
const am: Record<string, string> = amT;
const rw: Record<string, string> = rwT;
const fr: Record<string, string> = frT;
const langs: Record<string, Record<string, string>> = { en, am, rw, fr };
const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!).sort().join(",");

describe("catalog completeness", () => {
  it("has the same keys in en, am, rw and fr", () => {
    const e = Object.keys(en).sort();
    expect(Object.keys(am).sort()).toEqual(e);
    expect(Object.keys(rw).sort()).toEqual(e);
    expect(Object.keys(fr).sort()).toEqual(e);
  });

  it("ships all four languages", () => {
    expect(Object.keys(CATALOGS).sort()).toEqual(["am", "en", "fr", "rw"]);
  });

  it("really translates French (few values equal English)", () => {
    const same = Object.keys(en).filter((k) => fr[k] === en[k]).length;
    expect(same / Object.keys(en).length).toBeLessThan(0.08);
    expect(fr["common.save"]).toBe("Enregistrer");
  });

  it("has no empty values", () => {
    for (const [l, cat] of Object.entries(langs)) for (const [k, v] of Object.entries(cat)) expect(v.trim(), `${l}:${k}`).not.toBe("");
  });

  it("namespaces own their key prefix and never collide", () => {
    const seen = new Set<string>();
    for (const [name, ns] of Object.entries(NAMESPACES)) {
      for (const l of ["en", "am", "rw", "fr"] as const) {
        const keys = Object.keys(ns[l]);
        expect(keys.sort(), `${name}.${l} vs en`).toEqual(Object.keys(ns.en).sort());
      }
      for (const k of Object.keys(ns.en)) {
        expect(k.startsWith(`${name}.`), `${k} should start with ${name}.`).toBe(true);
        expect(seen.has(k), `duplicate key ${k}`).toBe(false);
        seen.add(k);
      }
    }
  });

  it("keeps {placeholders} identical across languages", () => {
    for (const k of Object.keys(en)) {
      expect(placeholders(am[k]!), `am:${k}`).toBe(placeholders(en[k]!));
      expect(placeholders(rw[k]!), `rw:${k}`).toBe(placeholders(en[k]!));
      expect(placeholders(fr[k]!), `fr:${k}`).toBe(placeholders(en[k]!));
    }
  });

  it("has _other for every plural key", () => {
    for (const k of Object.keys(en)) if (k.endsWith("_one")) expect(en[k.replace(/_one$/, "_other")], k).toBeTruthy();
  });

  it("writes Amharic in Ethiopic script", () => {
    const ethiopic = /[ሀ-፿]/;
    const values = Object.values(am);
    const share = values.filter((v) => ethiopic.test(v)).length / values.length;
    expect(share).toBeGreaterThan(0.9);
  });

  it("every static t('…') key used in the source exists", () => {
    const missing: string[] = [];
    const walk = (dir: string) => {
      for (const f of readdirSync(dir)) {
        const p = join(dir, f);
        if (statSync(p).isDirectory()) walk(p);
        else if (/\.tsx?$/.test(f) && !f.endsWith(".test.ts") && !p.includes(`${join("src", "i18n")}`)) {
          const src = readFileSync(p, "utf8");
          for (const m of src.matchAll(/\bt\(\s*"([a-z]+\.[A-Za-z0-9_.]+)"/g)) {
            const k = m[1]!;
            if (!(k in en) && !(`${k}_other` in en)) missing.push(`${p}: ${k}`);
          }
        }
      }
    };
    walk(join(process.cwd(), "src"));
    expect(missing).toEqual([]);
  });
});

describe("tour and help centre text", () => {
  it("has title + text for every tour step in all languages", () => {
    for (const s of TOUR_STEPS) for (const part of ["title", "text"]) for (const l of Object.keys(langs)) expect(langs[l]![`guide.step.${s.id}.${part}`], `${l}:guide.step.${s.id}.${part}`).toBeTruthy();
  });
  it("has title, summary and every numbered step for each help guide in all languages", () => {
    for (const g of HELP_GUIDES) {
      const keys = [`guide.help.${g.id}.title`, `guide.help.${g.id}.summary`, ...Array.from({ length: g.steps }, (_, i) => `guide.help.${g.id}.s${i + 1}`)];
      for (const l of Object.keys(langs)) for (const k of keys) expect(langs[l]![k], `${l}:${k}`).toBeTruthy();
      expect(en[`guide.help.${g.id}.s${g.steps + 1}`], `${g.id} has extra unlisted step`).toBeUndefined();
    }
  });
});

describe("runtime", () => {
  it("interpolates, pluralises, falls back and honours overrides", () => {
    setLang("en");
    expect(tr("common.pageRange", { from: 1, to: 20, total: 55 })).toBe("1–20 of 55");
    expect(tr("guide.help.stepsCount", { count: 1 })).toBe("1 step");
    expect(tr("guide.help.stepsCount", { count: 3 })).toBe("3 steps");
    expect(trx("nope.nothing", undefined, "fallback")).toBe("fallback");
    setLang("rw");
    expect(tr("common.save")).toBe(CATALOGS.rw["common.save"]);
    setOverrides("rw", { "common.save": "Emeza ubike" });
    expect(tr("common.save")).toBe("Emeza ubike");
    setOverrides("rw", {});
    setLang("en");
  });
});
