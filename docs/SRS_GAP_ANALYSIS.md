# SRS gap analysis — NXV-BTN-SRS-001 v1.0 vs. the built system

Independent re-audit of branch `feat/btn-operations-system` at commit `55f026f` (27/11/2026) against
`docs/SRS_REQUIREMENTS.md`. It supersedes the audit of `4801060` (commit 9941a63) and the rows edited after it.

Method:
- Every item that was not ✅ before was re-checked in the code and, where possible, by running it.
- The rest was spot-checked for regressions, in particular workflows that may have lost a role in the §8 re-alignment.
- The runs used an API on :4900 against fresh scratch databases `btn_ops_audit` / `btn_ops_audit_test`, with
  `JOBS_ENABLED=false` and SMTP pointed at a dead port.

Legend: ✅ met · ⚠️ partial (the missing clause is stated) · ❌ missing · env = depends on the production environment
(what the code and config provide is stated).

## Summary

**122 numbered requirements: 116 ✅ · 1 ⚠️ · 0 ❌ · 5 env.** The cross-cutting §7.4 finding (payroll needed two Finance
Managers) is fixed after the re-audit: every approval workflow is walked with exactly one user per role (see §8 below).

| Module | Reqs | ✅ | ⚠️ | ❌ | env | Open points |
|---|---|---|---|---|---|---|
| Access control (AUTH) | 10 | 10 | 0 | 0 | 0 | — |
| Production (PRD) | 10 | 10 | 0 | 0 | 0 | — |
| Stock (STK) | 10 | 10 | 0 | 0 | 0 | — |
| Clients & orders (ORD) | 10 | 10 | 0 | 0 | 0 | — |
| Delivery (DEL) | 6 | 6 | 0 | 0 | 0 | — |
| Workforce (WRK) | 7 | 7 | 0 | 0 | 0 | — (§7.4 fixed: one user per role completes payroll; supervisors register casual workers pending Finance approval) |
| Finance (FIN) | 9 | 9 | 0 | 0 | 0 | — |
| Procurement (PRC) | 8 | 8 | 0 | 0 | 0 | — |
| Reporting (RPT) | 7 | 7 | 0 | 0 | 0 | — |
| Administration (ADM) | 5 | 5 | 0 | 0 | 0 | — |
| Offline & sync (OFF) | 22 | 22 | 0 | 0 | 0 | — |
| Non-functional (NFR) | 18 | 12 | 1 | 0 | 5 | NFR-M1: the parallel race suite is flaky |

**Test evidence from this audit:**

| Check | Result |
|---|---|
| `go test -race ./...`, packages in parallel, run 1 | 41/42 packages pass. `syncsvc:TestPullDeltaScopeAndSnapshots` failed with "paged total 20 want 22" |
| `go test -race ./...`, run 2 | 42/42 pass |
| `TestPull*` repeated 6× on its own | Passes every time |
| Frontend `tsc -b` | Clean |
| Frontend vitest | 23 files, 141 tests pass |
| Frontend eslint | 0 errors, 49 warnings |
| `cmd/loadtest run`, 50 users, default limits, nearly empty scratch DB | 100 % of reads < 2 s. The harness still reports FAIL, because 21 and then 3 requests got 429 from the per-IP `global` limit (600/min) when all 50 virtual users share 127.0.0.1 |
| Delta pull with an unrelated transaction held open for 20 s | Delivered the central product change at once. The stall reported against `4801060` is fixed |
| Push with `X-Sync-Schema: 99` | 409 `schema_too_new` |
| Push with `X-Sync-Schema: 0` | 400 |
| CSV export | Ends with `# Data cut-off,DD/MM/YYYY HH:mm` |
| Transfer request without a reason | 400 "Give the reason for the transfer" |
| Seeded report schedules | All 10 enabled, SRS recipients, separate weekly and monthly orders/sales rows |
| §8 cells by curl | sysadmin reads production, expenses and payroll · procurement reads stock · sales reads expenses and the finance dashboard · PM reads expenses · stock reads deliveries · supervisor → 403 on expenses · sales → 403 on production |

**Allowed deviations:**
- Stack: Go + Fiber, React + Vite, and an in-process PostgreSQL-leader job scheduler instead of FastAPI, Next.js and
  Celery/Redis. SRS §2.5 allows the stack to change in discovery.
- No payment gateway (owner decision). Payments are recorded only, and the SRS never requires a gateway.

## Previously open items — re-verified

| Item (audit of 4801060) | Now | How verified |
|---|---|---|
| AUTH-05 lockout not configurable | ✅ | Settings → Account lockout (Super Admin), `adminapi/adminapi.go` lockout fields with min/max, `SettingsPage.tsx`; `app/lockout_policy_test.go` |
| ORD-04 draft cancel without approval | ✅ | `sales/lifecycle.go` `cancelNeedsApproval` = every cancellable state; `sales.cancel` approver ≠ requester |
| RPT-06 CSV without cut-off | ✅ | `export/export.go` + `dataexport` append `# Data cut-off` / `# Unsynchronised data` rows (seen live) |
| OFF-11 delta pull stalled by any open txn | ✅ | Watermark + sync advisory lock (`syncsvc/pull.go` `pullUpper`, `migrations/03250_sync_watermark.sql`); `idle_in_transaction_session_timeout` 5 min (`db/db.go`); live 20 s open-txn test passed |
| NFR-P1 `/auth/me` 30/min per IP | ✅ | New `session` group, per user, 120/min (`ratelimit.go`); `app/ratelimit_test.go`. See the note in the NFR table |
| NFR-M1 parallel tests / e2e not in CI | ⚠️ | Template-DB clones removed the lock exhaustion, and CI now has an `e2e` Chrome job (`frontend/e2e/run-ci.sh`). One sync test is still flaky under parallel load (see the NFR table) |
| NFR-M2 no external uptime probe | ✅ | `deploy/uptime-check.sh` + `btn-ops-uptime.timer`; `/api/health?db=1` |
| §5.2 no offline dashboard/report cache; no online-only gate | ✅ | `offline/cache.ts` (`offlineQueryFn`/`useReportCache`) used by the dashboard, boards, reports, finance, sales, procurement, production and workforce; `OfflineDataBanner` "data as of"; `Button onlineOnly` on approve/verify/pay/admin actions in 29 module files; `OnlineOnlyZone`; `lib/api.ts` refuses direct writes offline; `cache.test.ts`, `onlineOnly.test.ts` |
| §5.5 raw-JSON merge; one version in the audit | ✅ | `ConflictsPage.tsx` + `conflictMerge.ts`: merge target prefilled, "Keep as additional batch", per-field picker. `syncsvc/conflicts.go` writes `sync.conflict_open` / `sync.conflict_resolve` with server Before / device After; `conflictMerge.test.ts` |
| §6.3 hard deletes | ✅ | `migrations/03230_no_hard_delete.sql`: `btn_forbid_delete` on budget, threshold, charge rule, attachments, target, recipe, piece-work; `btn_archive_deleted` archives replaced child lines into `deleted_record`; `httpx.RemovalReason` + RemoveDialog |
| §7.2 transfer reason optional | ✅ | `note` is required, at least 3 characters (live 400) |
| §7.4 supervisor could review wages | ✅ | `payroll.check` = PM only; the payroll staffing constraint found by the re-audit is fixed (7.4 row below) |
| §8 ~20 mismatched cells | ✅ | `auth/permissions.go` matches the matrix (deviations below); live cells above |
| §9.2 sync schema version not enforced | ✅ | `X-Sync-Schema` + per-record-type ranges (426/409, nothing stored); `app/sync_schema_test.go`; client "Update required" notice (`SchemaUpgradeNotice.tsx`) |
| §10 weekly+monthly orders; finance on variance | ✅ | `migrations/03200_srs_roles_schedules.sql`: `orders_sales` weekly + `orders_sales_monthly`; `stock_movement_variance` → PM, FM; all enabled |

## Per requirement

### AUTH — Access control and users
| ID | Status | Evidence | Note |
|---|---|---|---|
| AUTH-01 | ✅ | Argon2id `auth/password.go`; `TestHashRoundTripAndFormat`, `app/auth_flow_test.go:TestFullSignInFlow` | Email sign-in only (the SRS says "email or username") |
| AUTH-02 | ✅ | `auth/permissions.go` Matrix + `RequirePermission` on every route (`TestOpenAPIDocumentCoversEveryRoute`); UI route guards | |
| AUTH-03 | ✅ | `IsPlantScoped`/`UserPlantScoped`; `production:TestPermissionsAndPlantScope`, `stock:TestReadEndpointsPermissionsAndScope` | |
| AUTH-04 | ✅ | Single-use 30-min link `auth/reset_link.go`; forced change `auth/middleware.go`; `TestPasswordResetLink`, `TestPasswordChangeForcedAndPolicy` | |
| AUTH-05 | ✅ | Configurable threshold and period (Settings → Account lockout); attempts in `AuthAttempt`; `TestAccountLockout`, `lockout_policy_test.go` | |
| AUTH-06 | ✅ | 15-min access + rotating refresh `auth/tokens.go`; device revoke `auth/devices.go`, AdminDevicesPage; `TestAccessExpiryAndRefreshRotation` | |
| AUTH-07 | ✅ | Append-only audit + device `01100_audit_immutable.sql`; `TestAuditImmutableAndDevice` | |
| AUTH-08 | ✅ | Deactivate/reactivate, no delete `users/users.go`; `TestUsersCRUD` | |
| AUTH-09 | ✅ | Email OTP on every password sign-in + passkeys; `TestOTPAttemptsLockTheChallenge` | Exceeds "optional" |
| AUTH-10 | ✅ | Signed offline package `auth/offline.go` (14 days); `OfflineUnlockScreen.tsx`; `TestOfflinePackageSignature`; e2e T-07 | |

### PRD — Production
| ID | Status | Evidence |
|---|---|---|
| PRD-01 | ✅ | `POST /production/reports` with site, date, shift, product, `batchRef`, qty; ProductionFormPage; `TestBatchesAndAdditionalBatch` |
| PRD-02 | ✅ | `defect_reason` refdata + note; `TestDefectsByReason` |
| PRD-03 | ✅ | `materialUsages` → CONSUMPTION movements; `TestWorkflowApprovePostsLedgersAtomically` |
| PRD-04 | ✅ | Posts on submit, deltas on edit; `TestLedgerFollowsStatus` |
| PRD-05 | ✅ | `/production/targets`, `/targets/progress`; TargetsPage; `TestTargetsAndProgress` |
| PRD-06 | ✅ | 409 `duplicate_batch` unless `additionalBatch`; `TestBatchesAndAdditionalBatch` |
| PRD-07 | ✅ | Day close/reopen, corrections approved by PM; `TestDayCloseAndCorrections`, `TestAutoCloseJob` |
| PRD-08 | ✅ | `/summary/periods` day/week/month/custom, site + consolidated, yield/defect rate; `TestPeriodsExportsAndDenials` |
| PRD-09 | ✅ | Notes + flags (breakdown, weather, shortage); `TestNotesAndExceptionFlags` |
| PRD-10 | ✅ | Versioned recipes, tolerance, variance flag; RecipesPage; `TestRecipesAndVariance` |

### STK — Stock and warehouse
| ID | Status | Evidence |
|---|---|---|
| STK-01 | ✅ | `Plant.kind` SITE/HQ/WAREHOUSE; separate product and material ledgers |
| STK-02 | ✅ | Category, unit, reorder level (global + per location); `TestCatalogueCategoriesUnitsAndReorderLevel` |
| STK-03 | ✅ | Immutable ledger rows, balances derived; `TestReceiptsIssuesLotsAndHistory` |
| STK-04 | ✅ | `/transfers` with 6 states, HQ↔site and site↔site; `TestTransferFullWorkflowWithShortfall`, `TestTransferStateMachine` |
| STK-05 | ✅ | Online 409 `insufficient_stock`; offline accepted + `NEGATIVE_STOCK`; `TestStockMovementSyncHandler` |
| STK-06 | ✅ | `/stock-counts` → variance → PM approval → adjustments; `TestBlindCategoryCountVarianceApproval` |
| STK-07 | ✅ | `stock.low_stock` in-app + email; `TestLowStockAlertsDedupEmailAndJob` |
| STK-08 | ✅ | `/stock/history` filters item, location, type, user, period, lot |
| STK-09 | ✅ | PO receipts → `stock.ReceiveGoods`; `TestReceiveGoodsAPI` |
| STK-10 | ✅ | `lotRef`, `/stock/lots`, FIFO; `TestAllocateFIFO` |

### ORD — Clients and orders
| ID | Status | Evidence |
|---|---|---|
| ORD-01 | ✅ | Client type, status, notes, TIN, contacts, communications (`02200_orders_delivery.sql`); `TestClientDatabaseDuplicatesAndHistory` |
| ORD-02 | ✅ | `POST /sales/customers/duplicates` on name, phone or TIN; `TestDuplicateScoring` |
| ORD-03 | ✅ | Line discount, delivery requirements, dates, payment terms; `TestLineDiscountMaths` |
| ORD-04 | ✅ | All Figure 3 states (`sales/lifecycle.go`). Cancel from Draft/Quoted/Confirmed always needs reason + approval. Value/role thresholds via `approval_threshold`. Partial dispatch keeps the order open. `TestFigure3Transitions`, `TestValueApprovalsAndCancellation` |
| ORD-05 | ✅ | `/sales/:id/availability`; `sales.order_shortfall` notification |
| ORD-06 | ✅ | `POST /:id/payments` (deposit/part/balance) recorded as income; `income:TestIncomeOrderPayments` |
| ORD-07 | ✅ | Per-client orders and communications, searchable |
| ORD-08 | ✅ | Quotation, confirmation and delivery-note PDFs with branding; `sales:TestPDFs` |
| ORD-09 | ✅ | `/sales/reports/orders` + exports; `TestReportsExportsAndOverdue` |
| ORD-10 | ✅ | Approved, versioned amendments; `TestAmendmentsVersionHistory` |

### DEL — Delivery
| ID | Status | Evidence |
|---|---|---|
| DEL-01 | ✅ | `transportMode` CLIENT/BTN |
| DEL-02 | ✅ | `delivery_charge_rule` FIXED/ZONE/DISTANCE; `TestDeliveryChargeRules` |
| DEL-03 | ✅ | Partial dispatch with vehicle, driver and destination; `TestOrderLifecycleChargesDeliveryAndPayments` |
| DEL-04 | ✅ | Dispatch reduces stock at the dispatching location |
| DEL-05 | ✅ | `/deliveries/:id/delivered` with receiver, note, photo |
| DEL-06 | ✅ | `/sales/deliveries` filters + `/deliveries/pending` |

### WRK — Workforce
| ID | Status | Evidence |
|---|---|---|
| WRK-01 | ✅ | Worker ID, phone, site, rate basis, status; access-controlled; `TestWorkerRecordAndReports` |
| WRK-02 | ✅ | Present/absent/half-day/overtime, site-scoped; headcount; `TestRosterAndRecord` |
| WRK-03 | ✅ | `workforce/calc.go` daily/half/overtime/piece, per-worker breakdown; `TestCalculateDailyWithHalfAndOvertime`, `TestCalculatePieceAndMixed` |
| WRK-04 | ✅ | `wage_payment`, `/periods/:id/record-payment`, per-worker history; `TestLockCorrectionPaymentAndAdjustment` |
| WRK-05 | ✅ | Attendance day lock, audited manager corrections, paid periods locked |
| WRK-06 | ✅ | `/workforce/reports/summary` per site + consolidated |
| WRK-07 | ✅ | Cross-site mark needs an exception + reason; `TestCrossSiteException` |

### FIN — Finance
| ID | Status | Evidence |
|---|---|---|
| FIN-01 | ✅ | Expense with category, amount, date, site/department, supplier, method, attachments, budget link or unbudgeted; `TestExpenseApprovalPaymentAndReceipts`, `TestExpenseBudgetLinkRules` |
| FIN-02 | ✅ | Per-custodian floats, top-ups, approvals, PETTY_CASH expenses, reconciliation; `TestFloatsTopUpsAndReconciliation` |
| FIN-03 | ✅ | Budgets by category and department; `/budgets/vs-actual`; `TestBudgetLifecycleAndPermissions` |
| FIN-04 | ✅ | Income module, order payments as income, receivables; `TestIncomeOrderPayments`, `TestFinanceDashboard` |
| FIN-05 | ✅ | `/procurement/trace`, `/expenses/trace`; `TestOrderReceiveInvoiceTrace` |
| FIN-06 | ✅ | `/finance/dashboard`: revenue/income, expenses, petty cash, budgets, balances, procurement spend |
| FIN-07 | ✅ | CSV/XLSX on every finance report |
| FIN-08 | ✅ | `/expenses/budget-check` warning + overrun approval; `TestBudgetOverrunControl` |
| FIN-09 | ✅ | Finance permissions per route; supervisor → 403 (live) |

### PRC — Procurement
| ID | Status | Evidence |
|---|---|---|
| PRC-01 | ✅ | Requests with items, qty, justification, required date, estimated cost |
| PRC-02 | ✅ | `RouteLevels` by value and requester; self-approval escalates; `TestRouteLevelsByValue`, `TestRouteLevelsByRequester` |
| PRC-03 | ✅ | Suppliers + purchase history; `TestSupplierDirectory` |
| PRC-04 | ✅ | PO quotation ref + invoices |
| PRC-05 | ✅ | All 8 statuses; `TestStateMachines` |
| PRC-06 | ✅ | Partial or full receipt into any location; `TestUnderReceiptShortCloseAndCancel` |
| PRC-07 | ✅ | `/requests/from-low-stock`; `TestLowStockProposal` |
| PRC-08 | ✅ | `/reports/spend` by supplier, category, period, location |

### RPT — Reporting, dashboards, notifications
| ID | Status | Evidence |
|---|---|---|
| RPT-01 | ✅ | 6 role boards (`dashboard/boards.go`); checklist in §10; `boards_test.go` |
| RPT-02 | ✅ | `report_schedule` (10 rows, enabled) + job `reports.scheduled`; `TestScheduleDispatchCreatesOneRunPerSlot`, `TestSendNowEmailsAttachmentsPerRecipient` |
| RPT-03 | ✅ | Site/date filters, PDF + CSV/XLSX/JSON; `TestEveryCatalogReportServesPDFAndExports` |
| RPT-04 | ✅ | Notification centre: low stock, approvals, transfers, sync conflicts, procurement |
| RPT-05 | ✅ | Per-event email preferences; `TestPreferencesAPI` |
| RPT-06 | ✅ | Cut-off + unsynced sites on screens, dashboards, PDF, XLSX, CSV (comment rows) and emails; `TestCutoffStatement`, dataexport tests |
| RPT-07 | ✅ | Boards render in milliseconds; `TestBoardsRenderTwelveMonthsWithinBudget` |

### ADM — Administration
| ID | Status | Evidence |
|---|---|---|
| ADM-01 | ✅ | Sites, products, materials, units, reason codes, expense categories, rate rules, thresholds (RefDataPage, MasterDataPage) |
| ADM-02 | ✅ | `/audit` search + CSV/XLSX/JSON export |
| ADM-03 | ✅ | `/sync/monitor` per device and site |
| ADM-04 | ✅ | Health, jobs, errors; critical alerts in-app + `OPS_ALERT_EMAIL`; external probe `deploy/uptime-check.sh` |
| ADM-05 | ✅ | `/data/export` CSV/JSON (with cut-off notes) |

### OFF — Offline operation and synchronisation
| ID | Status | Evidence | Note |
|---|---|---|---|
| OFF-01 | ✅ | VitePWA injectManifest, `src-sw/sw.ts` precache + navigation fallback; e2e T-02 | |
| OFF-02 | ✅ | Dexie IndexedDB + persistent storage; e2e T-02 | |
| OFF-03 | ✅ | Encrypted outbox written before the "Saved on this device" toast | |
| OFF-04 | ✅ | `core/connectivity.ts` real probe ≤5 s | |
| OFF-05 | ✅ | `ConnectionIndicator.tsx` Online/Offline/Syncing + pending count | |
| OFF-06 | ✅ | uuid, device, user, capturedAt, seq, schemaVersion per record | |
| OFF-07 | ✅ | `core/engine.ts` auto background sync | |
| OFF-08 | ✅ | Ordered batches ≤200, exponential back-off, resumable | |
| OFF-09 | ✅ | Dedup on (uuid, revision); `TestPushIdempotencyAndDuplicates` | |
| OFF-10 | ✅ | accepted / duplicate / conflict / rejected + reason | |
| OFF-11 | ✅ | Cursor delta pull, watermark-bounded (`syncsvc/pull.go`); live open-transaction test delivered at once | A transient upper-bound drop can end a pagination series early with `hasMore=false`; the rows come on the next pull (deferred, not lost). This is the likely cause of the flaky test under NFR-M1 |
| OFF-12 | ✅ | PIN/password unlock, 14 days, configurable; admin device revoke; `TestDeviceRevokeStopsSyncAndRefresh` | |
| OFF-13 | ✅ | AES-GCM-256 + PBKDF2 + device secret; wiped on sign-out | |
| OFF-14 | ✅ | Sign-out confirm with pending records; e2e T-10 | |
| OFF-15 | ✅ | Capture + receipt time, configurable skew flag; `TestClockSkewFlags` | |
| OFF-16 | ✅ | Capture-time role + current rules; `TestRevalidationCaptureRoleAndCurrentRules` | |
| OFF-17 | ✅ | `SyncIssuesPage.tsx` correct & resubmit | |
| OFF-18 | ✅ | `negative_stock` review + notify; `TestNegativeStockFlagRoutesToReview` | |
| OFF-19 | ✅ | Site + 60-day scope; quota warnings | |
| OFF-20 | ✅ | "Sync now"; passphrase-encrypted export | |
| OFF-21 | ✅ | Prompted SW update when no dirty form; Dexie migrations; server schema ranges with an "Update required" notice | |
| OFF-22 | ✅ | Timeouts, batch halving, back-off, idempotency; e2e T-03 | |

### NFR — Non-functional
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| NFR-P1 | ✅ | Load test: 50 users, 100 % < 2 s; signed-in `/auth/*` is limited per user | Note: the `global` limit is still 600/min **per IP**, so the harness logs a few 429s when 50 users share one IP. The commit's claim that it passes on default limits was not reproduced. It is fine for a site NAT of roughly 10–20 users; raise it, or key it by user for signed-in traffic, if a large site shares one IP |
| NFR-P2 | ✅ | `loadtest reports` sweep; `TestBoardsRenderTwelveMonthsWithinBudget`; commit 4988b17 records 172/172 < 5 s on the volume DB | |
| NFR-P3 | ✅ | `TestBatchOf200Under10s`; e2e NFR scenario (now in the CI e2e job) | |
| NFR-P4 | ✅ | `e2e/load-size.mjs`: about 1.7 MB initial, cached repeat visits | |
| NFR-A1 | env | systemd units, health endpoint, uptime timer, offline-first client | Hosting SLA |
| NFR-R1 | env | `deploy/backup.sh` nightly encrypted dump + off-site with a timer; `deploy/restore-test.sh` | The quarterly restore drill still has no timer or runbook date |
| NFR-S1 | ✅ | nginx TLS 1.2/1.3; permission on every route; CSRF header guard; parameterised SQL | |
| NFR-S2 | ✅ | `.env` gitignored, only examples tracked | |
| NFR-S3 | ✅ | Encrypted vault, wiped on sign-out and revoke; `TestRevokedDeviceFinalUpload` | |
| NFR-S4 | ✅ | `auth` (per IP), `session` (per user) and sync push limits + DB lockout | |
| NFR-D1 | ✅ | Transactions around every posting; ledger-derived balances | |
| NFR-D2 | ✅ | Privacy subject detail, export, correction, access log | |
| NFR-U1 | env | Guided tour, help centre, keyboard e2e | Measure at UAT |
| NFR-C1 | env | Chromium e2e in CI | No Edge/macOS run |
| NFR-M1 | ⚠️ | CI: gofmt, vet, `go test -race ./...`, frontend typecheck/test/build, and a new browser e2e job (`frontend/e2e/run-ci.sh`); template-DB test clones | **The parallel race suite is flaky.** `syncsvc:TestPullDeltaScopeAndSnapshots` failed 1 of 2 parallel runs ("paged total 20 want 22") and passed 6× alone. CI runs exactly this command, so expect intermittent red builds → make the pagination assertion loop until `hasMore=false` on a quiescent schema (or retry the final pull), or keep the bound from dropping within a page series |
| NFR-M2 | ✅ | Structured logs, error capture, job metrics, external uptime probe | |
| NFR-SC1 | env | Sites/users are data; volume generator `cmd/devtools volume.go` (10 sites, ~737k rows per commit 3d0431c) | 5M-row target not reproduced here |
| NFR-L1 | ✅ | DD/MM/YYYY, Africa/Kigali timestamps, RWF, en/rw/fr (+am). Server business dates follow the Kigali calendar: one fakeable clock `dates.Now()` / `Today()` / `Year()` / `DateOf(t)` used for today, default report periods, order due dates, expense payment dates, document numbering (PO/GRN/PR/TR/SC/ADJ/PC-<year>), day close, attendance cut-off, schedules and the cut-off note; SQL uses `dates.SQLToday` (`(now() AT TIME ZONE 'Africa/Kigali')::date`) instead of `CURRENT_DATE` (sync windows, overdue counters, budget defaults); `db.Now()` uses the same clock. `dates:TestKigaliClockAtLateUTCEvening` (23:30 UTC → next Kigali day; 22:15 UTC on 31/12 → year 2027); `sales:TestOrderLifecycleAtLateUTCEvening`, `expenses:TestExpenseApprovalPaymentAndReceiptsAtLateUTCEvening` run the full flows under a 23:30 UTC clock | Frontend `formatDate(Date)` takes the UTC day of a Date object (fine for date-only values) |

## §5 Offline sections

**§5.1 principles — ✅.** Local first, event-based records, provisional balances, idempotent upload, visible
connection and per-record state, and an encrypted, user/device-bound store are all in place.

**§5.2 offline capability matrix — ✅ (all 10 rows).**
- Sign-in on a known device: offline unlock.
- Capture offline:
  - production
  - attendance/headcount
  - stock receipts, issues and adjustments
  - transfer requests and receipts
  - order drafts and dispatch notes
  - petty cash, expense and income entries
- Look-ups: cached reference data.
- Cached dashboards and reports: shown offline with an "Offline — data as of …" banner (`offline/cache.ts`,
  `OfflineDataBanner.tsx`).
- Approvals, admin, email schedules, conflict resolution and exports: disabled offline with an explanation
  (`Button onlineOnly`, `OnlineOnlyZone`); `lib/api.ts` refuses direct writes offline.

**§5.5 conflict rules — ✅ (all 7 + audit).**

| Rule | How it is handled |
|---|---|
| Stock movements from two devices | Both accepted as events; negative stock goes to review |
| Duplicate production batch | Held as Conflict; resolved as merge (target prefilled) or as an additional batch |
| Attendance entered by two users | Conflict, resolved by the supervisor |
| Master data | Read-only offline, so no clash can arise |
| Order status changed centrally | Server wins; the offline edit is shown for review |
| Edit to a locked record | Becomes a correction request or a PM-only conflict. This meets "correction via approval workflow" |
| Update to a deleted record | Rejected `not_found`; the local copy is kept until dismissed |

Both versions of every conflict are written to the immutable audit (`sync.conflict_open`, `sync.conflict_resolve`).

## §6.3 Data rules
| Rule | Status | Evidence / note |
|---|---|---|
| Device UUIDs; server numbers on acceptance | ✅ | Push keyed by client UUID; `seq` numbering |
| RWF exact, quantities exact, UTC storage, Kigali display | ✅ | NUMERIC/BIGINT money; UTC writes (stored in `timestamp without time zone`) |
| Never hard-delete | ✅ | `03230_no_hard_delete.sql`: delete-forbidding triggers + archive of replaced child lines into `deleted_record`; removals need a reason and are audited. Remaining DELETEs are housekeeping (sessions, challenges, OTP, read notifications, sent mail, job runs), user-owned passkeys, i18n overrides, alert state, and the legacy importer replacing its own imported rows. None of these delete business records |
| CSV import validated, with an import report | ✅ | `/data/import/:kind/validate` + report; `TestDryRunCommitIdempotent`, `TestOpeningBalances` |
| Personal data minimal, access-controlled, exportable | ✅ | Privacy module; worker PII stripped from offline pulls |

## §7 Business rules
| Rule | Status | Evidence | Missing → fix |
|---|---|---|---|
| 7.2 Transfers | ✅ | Reason required; rejection needs a reason; dispatch posts the source issue; shortfall → adjustment for review; cancel before In transit, then `reverse`; `TestTransferValidationCancelReverseInsufficient` | |
| 7.3 Approvals, petty cash, receipts | ✅ | Value bands + role thresholds; receipt reference required; no self top-up approval; reconciliation differences; over/under receipt needs a reason + exception notification (`TestTopUpSelfApprovalBlocked`, `TestReconcileDifference`, `TestEvaluateReceipt`) | |
| 7.4 Wages | ✅ | Formula, admin rate rules, locked periods, adjustments in the next period. Steps now follow §7.4/§8 with **one user per role**: the site **Supervisor prepares** the wage sheet from the attendance he recorded (`payroll.prepare` = SUP, site-scoped; §8 Sup. C R U on attendance, the calculation itself is automatic, WRK-03) → **Production Manager reviews** (`payroll.check`, PM only) → **Finance verifies** (`payroll.verify`, Fin. U) → GM/MD authorise (stated deviation: wages are PAYROLL expenses, Finance "Top Mgt A") → Finance records the payment. `CheckTransition` keeps every signer distinct. `payroll:TestPayrollOneUserPerRole` walks a period to PAID with exactly one SUP, PM, FM, GM and MD (and an attendance day: SUP records, PM locks, SUP correction, PM approves); `TestWorkflowSeparationOfDutiesAndPay` | Each site that pays wage workers needs a supervisor (the PM or FM preparing would block their own later step) — BTN to confirm |

## §8 Roles and permissions — ✅

`auth/permissions.go` now matches the matrix cell by cell. A Super Administrator holds everything, and the extra roles
are Lead Operator and Operator (production and attendance capture) and Operations Advisor (read).

Remaining deviations, each one justifiable:
- Sales may **create income records**. Order payments themselves are FM-only (`payments.record`).
- Stock Officer, Procurement Officer and Supervisor may **receive procurement goods**. Receiving is a stock receipt,
  required by STK-09/PRC-06.
- GM/MD **authorise payroll** ("Attendance & wages" says Top Mgt R). This is documented as Finance "Top Mgt A", because
  wages are PAYROLL expenses.

**Workflow walk-through after the re-alignment.** Each step has a holder, and the module tests use those real roles.
Petty cash and expenses work with one FM plus GM/MD.

| Workflow | Steps and who holds them |
|---|---|
| Production | record: Sup/Lead → approve, corrections, close a day: PM; recipes: Admin; targets: PM (`TestOneUserPerRoleProduction`) |
| Orders | create, quote, confirm: Sales → value approval and cancel approval: Sales/PM (never the requester) → dispatch, delivered: Sales/Stock → payment: FM, recorded as income; one approver's decision covers every following level he qualifies for (each level still recorded), so the single PM completes 2-level amendments and cancellations (`TestOneUserPerRoleOrders`) |
| Procurement | request: Sup/Proc → approve by threshold: PM/FM/GM/MD → PO, invoice: Proc → receive: Stock/Proc/Sup → expense: FM (`TestOneUserPerRoleProcurement`) |
| Petty cash | custodian, entries: FM → top-up / over-limit / count review: GM/MD, never the custodian (`TestOneUserPerRolePettyCash`) |
| Expenses | record: FM → approve: FM/GM/MD; a level the recorder would sign is taken by GM/MD, so the single FM's own expenses and overruns complete (`TestOneUserPerRoleExpenses`, `TestOneUserPerRoleFinanceRecords`). Income: recorded by Sales/FM → verified by FM; FM's own income verified by GM/MD (`TestOneUserPerRoleIncome`). Budgets: FM → GM (`TestOneUserPerRoleBudgets`) |
| Transfers | request: Sup/Stock → approve: PM (one PM decision covers every following level his role may approve; a level naming another role still waits) → dispatch, receive: Stock/Sup → shortfall review: PM (`TestOneUserPerRoleTransfer`) |
| Stock counts / adjustments | count or request: Stock/Sup → approve: PM, never the counter/requester (`TestOneUserPerRoleStockCount`, `TestOneUserPerRoleStockAdjustments`) |
| Payroll | prepare: Sup (site) → check: PM → verify: FM → authorise: GM/MD → record payment: FM (`TestPayrollOneUserPerRole`) |
| Attendance | record: Sup/Lead → approve: PM. A supervisor registers a new casual worker at his site (`workers.register`, also offline: record type `worker_registration`); the worker can be marked present at once but is left out of wage sheets until Finance approves (`POST /payroll/workers/:id/approve`, audited; migration 03240); `TestSupervisorRegistersWorker` |

## §9 Interfaces
| Item | Status | Evidence |
|---|---|---|
| 9.1 UI | ✅ | Responsive; rw + fr catalogs; SyncBadge on offline records; axe WCAG 2.1 AA + keyboard e2e, now in the CI e2e job |
| 9.2 REST | ✅ | `/api/v1`; OpenAPI 3.1 `/api/v1/openapi.json` + `/docs`; pagination; `{"error":{code,message}}`; SMTP templates; CSV/XLSX/PDF |
| 9.2 Sync API | ✅ | `/sync/push`, `/sync/pull` (cursor delta), `/sync/status`; schema ranges enforced (live 409/400); `app/sync_schema_test.go` |

## §10 Dashboards and scheduled reports — ✅

**Dashboards** (`dashboard/boards.go`, `widgets.go`, `frontend/src/modules/dashboard/boards.tsx`): every SRS item is
present.

| Dashboard | SRS items |
|---|---|
| Executive / Top Mgt | production vs target · sales/orders · stock · revenue · expenses · budget · site comparison · alerts · site sync status |
| Production Manager | daily production and defects · material usage/variance · site stock · pending transfers · headcount · open orders · pending deliveries · conflicts |
| Finance | revenue · expenses · petty cash · budget vs actual · client balances · procurement spend |
| Procurement | pending requests · approved purchases · suppliers · awaiting receipt |
| Site Supervisor | own-site production · stock · attendance · pending/unsynced · sync issues |
| System Administrator | users/roles · devices · sync monitor · audit log · job health |

**Scheduled reports** (scratch DB after migrations; all `enabled=true`):

| Report | Frequency | Recipients |
|---|---|---|
| Daily production | daily 06:30 | PM, MD, GM, OA |
| Daily stock | daily 06:35 | PM, Stock |
| Daily headcount | daily 06:40 | PM, FM |
| Orders & sales | weekly 07:00 **and** monthly 07:05 | Sales, MD, GM, OA |
| Stock movement & variance | weekly + send-now | PM, FM |
| Procurement & expense | monthly | FM, MD, GM, OA |
| Wage & workforce | per wage period | FM, PM |
| Budget monitoring | monthly | FM, MD, GM, OA |
| Site performance | monthly | MD, GM, OA |

## §12.2 Offline acceptance — ✅
T-01…T-10 are automated in `frontend/e2e/offline-acceptance.mjs`, which now runs in the CI `e2e` job. Backend tests
back them up: `TestPushIdempotencyAndDuplicates` (T-03), `TestNegativeStockFlagRoutesToReview` (T-04),
`TestConflictCreationAndResolution` (T-05), `TestRevalidationCaptureRoleAndCurrentRules` (T-06),
`TestOfflinePackageSignature` (T-07), `TestDeviceRevokeStopsSyncAndRefresh` / `TestRevokedDeviceFinalUpload` (T-08)
and `TestClockSkewFlags` (T-09). This audit did not re-run the browser suite.

## Found when running the system
1. **Flaky parallel test** `syncsvc:TestPullDeltaScopeAndSnapshots`: 1 of 2 parallel race runs failed ("paged total
   20 want 22") (NFR-M1).
2. **Load-test harness reports FAIL on default limits**: a few 429s from the per-IP `global` limit with 50 users on one
   IP. Latency was well within budget.
3. ~~Payroll needs two distinct Finance Managers~~ — fixed after the re-audit (§7.4).
