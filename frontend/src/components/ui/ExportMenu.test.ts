import { describe, expect, it } from "vitest";
import { exportPath } from "./ExportMenu";
import { apiUrl } from "@/lib/api";

describe("exportPath", () => {
  it("adds /export, the format, the language and non-empty filters", () => {
    expect(exportPath("/stock/movements", "xlsx", { from: "2026-11-01", plantId: "", type: undefined })).toBe("/stock/movements/export?from=2026-11-01&format=xlsx&lang=en");
  });
  it("keeps an existing query string and strips a trailing slash", () => {
    expect(exportPath("/sales/?status=PAID", "csv")).toBe("/sales/export?status=PAID&format=csv&lang=en");
  });
  it("builds versioned absolute URLs", () => {
    expect(apiUrl(exportPath("/audit", "json"))).toBe("/api/v1/audit/export?format=json&lang=en");
  });
});
