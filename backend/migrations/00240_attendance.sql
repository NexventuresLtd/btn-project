-- +goose Up
-- +goose StatementBegin
-- Worker attendance (internal/attendance): one row per worker per calendar day, feeding payroll days.
-- status ∈ PRESENT | HALF_DAY | ABSENT | LEAVE; day-fraction PRESENT=1, HALF_DAY=0.5, ABSENT/LEAVE=0.
CREATE TABLE "Attendance" (
    "id" TEXT NOT NULL DEFAULT gen_random_uuid()::text,
    "workerId" TEXT NOT NULL,
    "plantId" TEXT NOT NULL,
    "date" DATE NOT NULL,
    "status" TEXT NOT NULL,
    "recordedById" TEXT,
    "note" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    "updatedAt" TIMESTAMPTZ,

    CONSTRAINT "Attendance_pkey" PRIMARY KEY ("id")
);

-- one attendance mark per worker per day (also serves lookups keyed by (workerId, date))
CREATE UNIQUE INDEX "Attendance_workerId_date_key" ON "Attendance"("workerId", "date");
-- the site roster for a day, and range summaries per site, read by (plantId, date)
CREATE INDEX "Attendance_plantId_date_idx" ON "Attendance"("plantId", "date");

ALTER TABLE "Attendance" ADD CONSTRAINT "Attendance_workerId_fkey" FOREIGN KEY ("workerId") REFERENCES "Worker"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Attendance" ADD CONSTRAINT "Attendance_plantId_fkey" FOREIGN KEY ("plantId") REFERENCES "Plant"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Attendance" ADD CONSTRAINT "Attendance_recordedById_fkey" FOREIGN KEY ("recordedById") REFERENCES "User"("id") ON DELETE SET NULL ON UPDATE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE "Attendance";
-- +goose StatementEnd
