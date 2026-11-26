import { describe, expect, it } from "vitest";
import { buildMerged, compareProduction, defaultPicks, fieldDiffs, isDuplicateBatch, mergeIntoPayload, mergeTargets, serverBase } from "./conflictMerge";

const client = {
  plantId: "p1", date: "2026-11-20", shift: "DAY", downtimeMins: 30, notes: "offline",
  lines: [{ productId: "hb20", batchRef: "", producedQty: 100, targetQty: 120, damageProdQty: 2, damageHandQty: 0, sampleQty: 1 }, { productId: "hb15", batchRef: "X", producedQty: 5 }],
  materialUsages: [{ materialId: "cement", quantity: 1.5 }, { materialId: "sand", quantity: 2 }],
};
const entry = {
  id: "r1", number: "PR-1", status: "SUBMITTED", plantId: "p1", date: "2026-11-20", shift: "DAY", downtimeMins: 1430, notes: "server",
  lines: [{ productId: "hb20", batchRef: "B-01", producedQty: 50, targetQty: 60, damageProdQty: 1, damageHandQty: 1, sampleQty: 0 }],
  materialUsages: [{ materialId: "cement", quantity: 0.25 }],
};
const row = {
  type: "production_entry", entityId: "r1", clientPayload: client,
  serverData: { reasonCode: "duplicate_batch", entry, clashes: [{ index: 0, batchRef: "B-01", productId: "hb20", reportId: "r1", number: "PR-1" }, { index: 1, batchRef: "X", productId: "hb15", reportId: "r2", number: "PR-2" }] },
};

describe("production duplicate batch", () => {
  it("is recognised and offers every clashing entry, the conflict's own first", () => {
    expect(isDuplicateBatch(row)).toBe(true);
    expect(isDuplicateBatch({ ...row, type: "attendance_mark" })).toBe(false);
    expect(mergeTargets(row)).toEqual([{ id: "r1", number: "PR-1" }, { id: "r2", number: "PR-2" }]);
  });

  it("merge payload carries the chosen target", () => {
    expect(mergeIntoPayload(client, "r2")).toEqual({ ...client, mergeInto: "r2" });
  });

  it("compares line by line with the server-resolved batch and sums like the server", () => {
    const cmp = compareProduction(entry, client, row.serverData.clashes);
    expect(cmp.lines).toHaveLength(2);
    expect(cmp.lines[0]).toMatchObject({ productId: "hb20", batchRef: "B-01" });
    expect(cmp.lines[0].server?.producedQty).toBe(50);
    expect(cmp.lines[0].device?.producedQty).toBe(100);
    expect(cmp.lines[0].merged).toEqual({ producedQty: 150, targetQty: 180, damageProdQty: 3, damageHandQty: 1, sampleQty: 1 });
    expect(cmp.lines[1].server).toBeNull();
    expect(cmp.lines[1].merged.producedQty).toBe(5);
    expect(cmp.materials).toEqual([
      { key: "cement", server: 0.25, device: 1.5, merged: 1.75 },
      { key: "sand", server: null, device: 2, merged: 2 },
    ]);
    expect(cmp.downtime.merged).toBe(1440); // capped like the server
    expect(cmp.notes.merged).toBe("server\noffline");
  });
});

describe("generic field-by-field merge", () => {
  const server = { reasonCode: "x", workerId: "w1", status: "PRESENT", hours: 2, note: "srv" };
  const device = { workerId: "w1", status: "ABSENT", hours: 0, extra: true };

  it("uses the stored entry when there is one", () => {
    expect(serverBase(row.serverData)).toBe(entry);
    expect(serverBase(server)).toBe(server);
    expect(serverBase(null)).toBeNull();
  });

  it("lists differing fields and prefills the device value", () => {
    const diffs = fieldDiffs(device, server);
    expect(diffs.map((d) => d.key)).toEqual(["workerId", "status", "hours", "extra", "note"]);
    expect(diffs.filter((d) => d.differs).map((d) => d.key)).toEqual(["status", "hours", "note"]);
    expect(defaultPicks(diffs)).toEqual({ status: "device", hours: "device", note: "device" });
    expect(buildMerged(device, server, defaultPicks(diffs))).toEqual(device);
  });

  it("takes the server value for the fields picked", () => {
    const merged = buildMerged(device, server, { status: "server", hours: "device", note: "server" });
    expect(merged).toEqual({ workerId: "w1", status: "PRESENT", hours: 0, extra: true, note: "srv" });
  });
});
