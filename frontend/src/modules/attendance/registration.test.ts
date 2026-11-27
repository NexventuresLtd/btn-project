import { describe, expect, it } from "vitest";
import { withRegistrationStatus } from "./offline";

describe("WRK-01 pending registrations on the offline roster", () => {
  it("takes the registration status from the pulled worker list", () => {
    const entries: { workerId: string; name: string; registrationStatus?: "PENDING" | "APPROVED" }[] = [{ workerId: "w1", name: "A" }, { workerId: "w2", name: "B" }, { workerId: "w3", name: "C" }];
    const pulled = [{ _id: "w1", registrationStatus: "PENDING" as const }, { _id: "w2", registrationStatus: "APPROVED" as const }];
    const out = withRegistrationStatus(entries, pulled);
    expect(out.map((e) => e.registrationStatus)).toEqual(["PENDING", "APPROVED", undefined]);
  });
  it("keeps a status the server roster already carries, and is a no-op without pulled data", () => {
    const entries = [{ workerId: "w1", registrationStatus: "APPROVED" as const }];
    expect(withRegistrationStatus(entries, [{ _id: "w1", registrationStatus: "PENDING" }])[0]!.registrationStatus).toBe("APPROVED");
    expect(withRegistrationStatus(entries, [])).toBe(entries);
  });
});
