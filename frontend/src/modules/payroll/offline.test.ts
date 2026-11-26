import { describe, expect, it } from "vitest";
import { getRecordType } from "@/offline/core/registry";
import { tr } from "@/i18n";
import { invalidRegistration, withRegisteredWorker, WORKER_REGISTRATION_TYPE } from "./offline";
import type { WorkerPayload } from "./api";

const ok: WorkerPayload = {
  firstName: "Eric", lastName: "Nshuti", nationalId: "1199880012345678", momoNumber: "0781234567", momoName: null, jobTitle: "Helper",
  plantId: "p1", dailyWage: 5000, startDate: null, phone: null, rateBasis: "DAILY", employmentStatus: "ACTIVE", leftDate: null,
};

describe("worker registration offline record type (WRK-01)", () => {
  it("is registered for supervisors (workers.register) and Finance", () => {
    expect(getRecordType(WORKER_REGISTRATION_TYPE)?.permission).toEqual(["workers.register", "workers.manage"]);
  });
  it("validates like the server", () => {
    expect(invalidRegistration(ok)).toBeNull();
    expect(invalidRegistration({ ...ok, lastName: " " })).toBe(tr("payroll.worker.err.firstName"));
    expect(invalidRegistration({ ...ok, nationalId: "123" })).toBe(tr("payroll.worker.err.nationalId"));
    expect(invalidRegistration({ ...ok, momoNumber: "12" })).toBe(tr("payroll.worker.err.momo"));
    expect(invalidRegistration({ ...ok, plantId: "" })).toBe(tr("payroll.worker.err.plant"));
    expect(invalidRegistration({ ...ok, dailyWage: 0 })).toBe(tr("payroll.worker.err.wagePositive"));
    expect(invalidRegistration({ ...ok, dailyWage: 0, rateBasis: "PIECE" })).toBeNull();
  });
  it("adds the new worker to the cached site roster once, sorted by name", () => {
    const cached = { plant: { id: "p1", code: "BTN-1", name: "Site 1" }, workers: [{ workerId: "w2", name: "ZENA", jobTitle: "Helper" }] };
    const once = withRegisteredWorker(cached, { workerId: "u1", name: "ERIC NSHUTI", jobTitle: "Helper" });
    const twice = withRegisteredWorker(once, { workerId: "u1", name: "ERIC NSHUTI", jobTitle: "Mason" });
    expect(once.workers.map((w) => w.workerId)).toEqual(["u1", "w2"]);
    expect(twice.workers).toHaveLength(2);
    expect(twice.workers[0]!.jobTitle).toBe("Mason");
    expect(cached.workers).toHaveLength(1); // pure
  });
});
