# SRS gap analysis — NXV-BTN-SRS-001 v1.0 vs. the built system

Independent compliance audit of branch `feat/btn-operations-system` at commit `4801060` (26/11/2026), against
`docs/SRS_REQUIREMENTS.md`. Each requirement was checked in the code: routes and handlers, migrations, frontend pages
and automated tests. Where the code was unclear, the API was built and run on :4900 against a scratch database
(`btn_ops_audit`, `JOBS_ENABLED=false`, SMTP pointed at a dead port) and exercised with curl.

Legend: ✅ met · ⚠️ partial (the missing clause is stated) · ❌ missing · env = depends on the production environment
(what the code and config provide is stated).

## Summary

**122 numbered requirements: 115 ✅ · 2 ⚠️ · 0 ❌ · 5 env** (after the post-audit fixes of AUTH-05, OFF-11, NFR-P1, NFR-M1, NFR-M2; audit: 110 ✅ · 7 ⚠️).

There are also 9 ⚠️ findings in the cross-cutting sections (§5.2 closed, see below): §5.5 (2), §6.3 (1), §7.2 (1), §7.4 (1),
§8 (1, many cells) and §10 scheduled reports (2) (§9.2 sync closed after the audit).

| Module | Reqs | ✅ | ⚠️ | ❌ | env | Open points |
|---|---|---|---|---|---|---|
| Access control (AUTH) | 10 | 10 | 0 | 0 | 0 | — (AUTH-05 lockout policy now set by the Super Administrator) |
| Production (PRD) | 10 | 10 | 0 | 0 | 0 | — (the §5.5 merge UI is listed below) |
| Stock (STK) | 10 | 10 | 0 | 0 | 0 | — (§7.2: a transfer reason is optional) |
| Clients & orders (ORD) | 10 | 9 | 1 | 0 | 0 | ORD-04: a draft can be cancelled without approval |
| Delivery (DEL) | 6 | 6 | 0 | 0 | 0 | — |
| Workforce (WRK) | 7 | 7 | 0 | 0 | 0 | — (§7.4: a supervisor can do the PM review) |
| Finance (FIN) | 9 | 9 | 0 | 0 | 0 | — (§8: Sales and PM have no finance read) |
| Procurement (PRC) | 8 | 8 | 0 | 0 | 0 | — |
| Reporting (RPT) | 7 | 6 | 1 | 0 | 0 | RPT-06: CSV exports have no cut-off note |
| Administration (ADM) | 5 | 5 | 0 | 0 | 0 | — |
| Offline & sync (OFF) | 22 | 22 | 0 | 0 | 0 | — (OFF-11 pull stall fixed) |
| Non-functional (NFR) | 18 | 13 | 0 | 0 | 5 | — (P1, M1, M2 fixed after the audit; P2 export note) |

**Test evidence (run during this audit):**
- Backend `go test -p 1 ./...`: 42 packages pass.
- Backend `go test ./...` (packages in parallel against one Postgres): fails on this host with `out of shared memory`
  (SQLSTATE 53200). An earlier parallel run on the previous tree failed 8 sync-pull tests (cause under OFF-11).
- Frontend: `tsc -b` clean, vitest 20 files / 121 tests pass, preview build initial load 1.66 MB.
- `cmd/loadtest run`, 50 users for 60 s: 7,444 reads, p95 6 ms, PASS. This only passed after raising
  `RATE_LIMIT_AUTH_PER_MIN`; with the default, `/auth/me` returned 450 × 429 (see NFR-P1).
- `cmd/loadtest reports`: 172 boards and reports, slowest 329 ms. The scratch DB was nearly empty.

**Post-audit re-run (fixes for AUTH-05, OFF-11, NFR-P1, NFR-M1, NFR-M2, §9.2):**
- Backend `go vet ./...` clean; `go test -race ./...` with packages in parallel: all packages pass (template-DB clones).
- Frontend: `pnpm typecheck` clean, vitest passes (incl. lockout/syncschema catalogs, engine upgrade/too-new tests).
- `cmd/loadtest run` with **default** rate limits, 50 users × 3 min, 12-month / 10-site / ~737k-row volume DB
  (`devtools volume -scale 4 -sites 7 -perf-users 90`): 23 099 reads, p95 38 ms, p99 80 ms, 0 × 429, PASS.
- `cmd/loadtest reports` on the same DB: 171/172 within 5 s; `pettycash-statement` XLSX 11.6 s (see NFR-P2).

**Allowed deviations:**
- Stack: Go + Fiber, React + Vite, and an in-process PostgreSQL-leader job scheduler instead of FastAPI, Next.js and
  Celery/Redis. SRS §2.5 allows the stack to be adjusted in discovery.
- No payment gateway (owner decision). Payments are recorded, not processed. The SRS never requires a gateway, so no
  requirement is affected.

## Per requirement

### AUTH — Access control and users
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| AUTH-01 | ✅ | Argon2id PHC `auth/password.go:14-32`; login `auth/routes.go:120`; `auth_unit_test.go:TestHashRoundTripAndFormat`, `app/auth_flow_test.go:TestFullSignInFlow` | Sign-in is by email only, no username (the SRS says "or", so this is acceptable) |
| AUTH-02 | ✅ | Matrix `auth/permissions.go:67-156`, `RequirePermission` on every route (`openapi:TestOpenAPIDocumentCoversEveryRoute`); UI guards in each `routes.tsx`; `TestPermissions`, `TestNewRoleMatrix` | Cell-level differences from §8 are listed under §8 |
| AUTH-03 | ✅ | `IsPlantScoped`/`UserPlantScoped` `permissions.go:201-213`. curl: sup1 requesting `/stock/balances?plantId=BTN-2` got only BTN-1 data. `production:TestPermissionsAndPlantScope`, `stock:TestReadEndpointsPermissionsAndScope` | |
| AUTH-04 | ✅ | Single-use 30-min link `auth/reset_link.go:22,59` (`used_at`/`invalidated_at`); forced first-login change `auth/middleware.go:16`; `f3_tokens_test.go:TestPasswordResetLink`, `TestPasswordChangeForcedAndPolicy` | |
| AUTH-05 | ✅ | Fixed after the audit: the Super Administrator sets the threshold (3–20 failures, default 5) and period (1–1440 min, default 15) in Administration → Settings → *Account lockout* (`settings.Security`, `adminapi.putSettings`, 403 for any other role); the login flow reads it on every failure (`auth/routes.go` → `settings.LoadSecurity`); the change is audited with before/after (`settings.update`) and each lock records the period (`auth.lock`); UI in en/am/rw/fr (`i18n/ns/lockout.ts`). `app:TestLockoutPolicyConfigurable`, `TestAccountLockout` | |
| AUTH-06 | ✅ | 15-min access token + rotating 14-day refresh `auth/tokens.go:36-41`; device list and revoke `auth/devices.go:346`, AdminDevicesPage; `TestAccessExpiryAndRefreshRotation`, `TestDevicesOwnAndAdmin` | |
| AUTH-07 | ✅ | Append-only triggers + device id `migrations/01100_audit_immutable.sql`; sign-ins audited `routes.go:219`; `audit:TestAuditImmutableAndDevice`, `TestAuditCommitsAtomicallyWithTheTransaction` | |
| AUTH-08 | ✅ | `isActive` toggle, no delete `users/users.go:150,218,294`; UsersPage; `TestUsersCRUD` | |
| AUTH-09 | ✅ | Email OTP on every password sign-in, plus passkeys (`auth/otp.go`, `passkey.go`); `TestOTPAttemptsLockTheChallenge`, `TestPasskeyRegistrationAndDiscoverableLogin` | Mandatory for everyone, which exceeds "optional for finance/admin" |
| AUTH-10 | ✅ | Ed25519-signed offline package `auth/offline.go`, 14-day default; `offline/ui/OfflineUnlockScreen.tsx`; `TestOfflinePackageSignature`; e2e T-07 | |

### PRD — Production
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| PRD-01 | ✅ | `POST /production/reports` (`production/module.go:54`) with site, date, shift, product, `batchRef`, quantity; ProductionFormPage; `srs_test.go:TestBatchesAndAdditionalBatch` | |
| PRD-02 | ✅ | Defects by `defect_reason` refdata code + note; reasons edited in RefDataPage; `TestDefectsByReason` | |
| PRD-03 | ✅ | `materialUsages` per entry → `MaterialMovement` CONSUMPTION (`production/store.go:773`); `TestWorkflowApprovePostsLedgersAtomically` | |
| PRD-04 | ✅ | Posts to stock on submit; edits post deltas; withdrawal reverses (`store.go:768-774`); `TestLedgerFollowsStatus` | |
| PRD-05 | ✅ | `/production/targets`, `/targets/progress`; TargetsPage; `TestTargetsAndProgress` | |
| PRD-06 | ✅ | curl: a duplicate batch got 409 `duplicate_batch`; with `additionalBatch:true` it was accepted as `…-D-2`; `TestBatchesAndAdditionalBatch` | |
| PRD-07 | ✅ | `/days/close`, `/days/reopen`, `/corrections` approve/reject, auto-close job; DaysPage, CorrectionsPage; `TestDayCloseAndCorrections`, `TestAutoCloseJob` | |
| PRD-08 | ✅ | `/summary/periods` by day/week/month/custom, per site and consolidated, with yield and defect rate, PDF/CSV/XLSX; ProductionSummaryPage; `TestPeriodsExportsAndDenials`, `calc_test.go:TestPeriodsAndPlanned` | |
| PRD-09 | ✅ | `/reports/:id/notes`, `/reports/:id/flags` (breakdown, weather, shortage); `TestNotesAndExceptionFlags` | |
| PRD-10 | ✅ | Versioned `/recipes`, expected usage, tolerance setting, variance flag + notification; RecipesPage; `TestRecipesAndVariance` | |

### STK — Stock and warehouse
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| STK-01 | ✅ | `Plant.kind` SITE/HQ/WAREHOUSE (`01302_location_kind.sql`); separate product and material ledgers per location | |
| STK-02 | ✅ | Category, unit, reorder level, plus per-location reorder level (`02101_stock_ledger.sql:42-66`); MasterDataPage; `TestCatalogueCategoriesUnitsAndReorderLevel` | |
| STK-03 | ✅ | Ledger rows immutable (update/delete trigger `02101:37-40`); balances summed from movements; all movement types (`02100`); `TestReceiptsIssuesLotsAndHistory` | |
| STK-04 | ✅ | `/transfers` approve, reject, dispatch, receive, cancel, reverse (`transfers/module.go:37-47`); all 6 states; HQ↔site and site↔site; Transfer pages; `TestTransferFullWorkflowWithShortfall`, `TestTransferStateMachine` | |
| STK-05 | ✅ | curl: an online over-issue got 409 `insufficient_stock`; offline it is accepted and flagged `NEGATIVE_STOCK` (`stock/sync.go:72-79`); `TestStockMovementSyncHandler`, `TestConcurrentWithdrawalsCannotOverdraw` | |
| STK-06 | ✅ | `/stock-counts` (full, category or blind) → variance → approval creates the adjustments; variance PDF; StockCountsPage; `TestBlindCategoryCountVarianceApproval` | |
| STK-07 | ✅ | `stock/alerts.go`: `stock.low_stock` sent in-app and by email by default; `/stock/low-stock` page; `TestLowStockAlertsDedupEmailAndJob` | |
| STK-08 | ✅ | `/stock/history` filters item, location, type, user, period, lot; export; `TestReceiptsIssuesLotsAndHistory` | |
| STK-09 | ✅ | `POST /procurement/orders/:id/receipts` → `stock.ReceiveGoods` with the PO reference; `TestReceiveGoodsAPI` | |
| STK-10 | ✅ | `lotRef` on receipts, issues and transfers; `/stock/lots`, FIFO; `TestAllocateFIFO` | |

### ORD — Clients and orders
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| ORD-01 | ✅ | Client type, status, notes, TIN, contacts and communications tables (`02200_orders_delivery.sql:8-46`); CustomersPage/CustomerPage; `TestClientDatabaseDuplicatesAndHistory` | |
| ORD-02 | ✅ | `POST /sales/customers/duplicates` matches on name, phone or TIN (`sales/dupes.go`); warning in the form; `TestDuplicateScoring` | |
| ORD-03 | ✅ | Line discount, delivery requirements, requested date, payment terms, deposit (`02200:62-146`); SaleFormPage; `TestLineDiscountMaths`, `TestPaymentTermsDepositAndKinds` | |
| ORD-04 | ⚠️ | All Figure 3 states (`sales/lifecycle.go:11-40`); `/quote`, `/confirm`, `/fulfilment`, `/ready`, `/close`, `/cancel`; approval by value and role via `approval_threshold`; partial dispatch keeps the order open; `TestFigure3Transitions`, `TestValueApprovalsAndCancellation` | Cancelling a **Draft** needs a reason but **no approval** (`cancelNeedsApproval`, `lifecycle.go:89`), while the SRS says cancel is "with reason and approval" → add an approval for draft cancels (threshold-driven, so it can be 0), or record BTN's acceptance |
| ORD-05 | ✅ | `GET /sales/:id/availability`; shortfall notification `sales.order_shortfall` to production holders; SaleDetailPage | |
| ORD-06 | ✅ | `POST /:id/payments` (DEPOSIT/PART/BALANCE), mirrored as income (`income:TestIncomeOrderPayments`); ReceivablesPage, statement | |
| ORD-07 | ✅ | `/customers/:id/orders`, `/customers/:id/communications`, search; CustomerPage | |
| ORD-08 | ✅ | `/pdf/quotation`, `/pdf/confirmation`, `/pdf/delivery-note` with branding (`sales/pdf_docs.go`); `sales:TestPDFs` | |
| ORD-09 | ✅ | `/sales/reports/orders` + PDF/CSV/XLSX; SalesReportsPage; `TestReportsExportsAndOverdue` | |
| ORD-10 | ✅ | Terms locked after Quoted; `POST /:id/amendments` → approval, `sale_amendment`/`sale_version` (`02200:234-262`); `TestAmendmentsVersionHistory` | |

### DEL — Delivery
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| DEL-01 | ✅ | `transportMode` CLIENT/BTN on sale and delivery (`02200:66,165`); SaleFormPage | |
| DEL-02 | ✅ | `delivery_charge_rule` FIXED/ZONE/DISTANCE (`sales/charges.go:19`); DeliveryChargesPage; `TestDeliveryChargeRules` | |
| DEL-03 | ✅ | `POST /:id/deliveries` with destination, vehicle, driver and partial quantities; `TestOrderLifecycleChargesDeliveryAndPayments` | |
| DEL-04 | ✅ | Dispatch reduces stock at the dispatching location (asserted at `orders_http_test.go:300`) | |
| DEL-05 | ✅ | `POST /deliveries/:id/delivered` with receiver, note and photo (`delivery_photo`); modals.tsx | |
| DEL-06 | ✅ | `/sales/deliveries` filtered by client, order, date and transport mode; `/deliveries/pending`; DeliveriesPage | |

### WRK — Workforce
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| WRK-01 | ✅ | Worker: ID number, MoMo phone, site, `rateBasis` DAILY/PIECE/MIXED, status; access-controlled (`payroll/workers.go`, `workforce/calc.go:17-35`); WorkersPage; `TestWorkerRecordAndReports` | |
| WRK-02 | ✅ | `/attendance`, `/mark-all` with present, absent, half-day and overtime hours, site-scoped; headcount report; DailyTab; `TestRosterAndRecord`, `TestPermissionAndScope` | |
| WRK-03 | ✅ | `workforce/calc.go:322-452`: daily, half, overtime, piece and effective-dated rules; CalcDetails.tsx per worker; `TestCalculateDailyWithHalfAndOvertime`, `TestCalculatePieceAndMixed` | |
| WRK-04 | ✅ | `wage_payment` (`02300_workforce.sql:197`); `/periods/:id/record-payment`; `/workforce/workers/:id/payments`; WorkerPaymentsPage; `TestLockCorrectionPaymentAndAdjustment` | |
| WRK-05 | ✅ | Attendance day submit/approve lock (`attendance/days.go`); audited manager-approved corrections (`corrections.go`); paid periods locked; `TestSyncDoubleEntryLockAndOvertime` | |
| WRK-06 | ✅ | `/workforce/reports/summary` per site plus a consolidated row, CSV/XLSX/PDF (curl verified); `TestWorkerRecordAndReports` | |
| WRK-07 | ✅ | A second-site mark needs an exception + reason (`02300:38-44`); `TestCrossSiteException` | |

### FIN — Finance
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| FIN-01 | ✅ | Expense: category, amount, date, site/department, supplier, method, source, budget link or unbudgeted; JPEG/PNG/WebP/PDF attachments (`expenses/create.go:68-73`, `attach.go`); ExpenseFormModal; `TestExpenseApprovalPaymentAndReceipts`, `finance:TestExpenseBudgetLinkRules` | |
| FIN-02 | ✅ | Per-custodian floats, top-ups, approvals, entries (as PETTY_CASH expenses), reconciliation (`pettycash/module.go:63-81`); FloatsTab, ReconciliationsTab; `TestFloatsTopUpsAndReconciliation` | |
| FIN-03 | ✅ | `budget` (department + period) and `budget_line` (category); `/budgets/vs-actual` with variance, PDF/CSV; BudgetsPage; `TestBudgetLifecycleAndPermissions` | |
| FIN-04 | ✅ | Income module (`03100`, `income`, `income_link`); order payments become income; receivables ageing; `TestIncomeOrderPayments`, `TestFinanceDashboard` | |
| FIN-05 | ✅ | `/procurement/trace`, `/expenses/trace`: request → approval → PO → receipt → invoice → expense/payment; PurchaseTracePage; `TestOrderReceiveInvoiceTrace` | |
| FIN-06 | ✅ | `/finance/dashboard`: income/revenue, expenses, petty cash, budgets, receivables, procurement spend (curl); FinanceDashboardPage | |
| FIN-07 | ✅ | Every finance report `?format=csv\|xlsx` (curl: 200, correct MIME); shared ExportMenu | |
| FIN-08 | ✅ | `POST /expenses/budget-check` warns in the form; over-budget needs `/:id/overrun/approve`; `TestBudgetOverrunControl` | |
| FIN-09 | ✅ | `expenses.*`, `budgets.*`, `pettycash.*`, `income.*` on every route; curl: sales → 403 on `/expenses` | The SRS gives Sales R on Finance; see §8 |

### PRC — Procurement
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| PRC-01 | ✅ | `POST /procurement/requests` with items, quantity, justification, required date, estimated cost (`procurement/requests.go:81-130`); RequestFormPage | |
| PRC-02 | ✅ | `RouteLevels` by value and requester; self-approval escalates (`workflow.go:109-147`); `TestRouteLevelsByValue`, `TestRouteLevelsByRequester`, `TestSelfApprovalEscalationRejectRevise` | |
| PRC-03 | ✅ | `/suppliers` CRUD + `/suppliers/:id/orders`; SuppliersPage/SupplierDetailPage; `TestSupplierDirectory` | |
| PRC-04 | ✅ | `purchase_order.quotation_ref`; `POST /orders/:id/invoices`; OrderFormPage/OrderDetailPage | |
| PRC-05 | ✅ | All 8 request states plus the PO state machine (`workflow.go:17-36`); `TestStateMachines` | |
| PRC-06 | ✅ | Partial or full receipt into any HQ/site location (`procurement/receiving.go`); `TestUnderReceiptShortCloseAndCancel` | |
| PRC-07 | ✅ | `/requests/low-stock`, `POST /requests/from-low-stock`; LowStockModal; `TestLowStockProposal` | |
| PRC-08 | ✅ | `/reports/spend` by supplier, category, month, location + PDF/exports; procurement ReportsPage | |

### RPT — Reporting, dashboards, notifications
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| RPT-01 | ✅ | 6 boards `dashboard/boards.go:51-76`, `GET /dashboard/boards/:board`, all 200 in 5–30 ms, wrong role 403; `dashboard/boards.tsx`; `boards_test.go`, `boards.test.ts`. Checklist in §10 | |
| RPT-02 | ✅ | `report_schedule` with 9 rows (`02600_report_schedules.sql:49-58`); job `reports.scheduled` (`reports/schedule_run.go`) emails PDF/XLSX; SchedulesPage; `TestScheduleDispatchCreatesOneRunPerSlot`, `TestSendNowEmailsAttachmentsPerRecipient`, `TestScheduledRunRetriesThenFails` | Go-live: every schedule is seeded `enabled=false`, so an admin must switch them on |
| RPT-03 | ✅ | Site/date filters and `?format=csv\|xlsx\|json` on every report, plus PDF (`reports/module.go:30`); `hub_http_test.go:TestEveryCatalogReportServesPDFAndExports` | |
| RPT-04 | ✅ | Notification centre: low stock, approvals, transfers, sync conflicts / negative stock, procurement; NotificationBell/NotificationsPage; `platform:TestNotificationsAPI`, `notify:TestDedupAndReadState` | |
| RPT-05 | ✅ | `GET/PUT /notifications/preferences`, email default per event; PreferencesPage; `TestPreferencesAPI`, `TestPreferenceResolution` | |
| RPT-06 | ⚠️ | `/reports/cutoff` (cut-off + pending sites) shown on screens (`components/DataCutoff.tsx`), dashboards, PDF, XLSX and emails; `TestCutoffStatement` | **CSV exports carry no cut-off or unsynced-sites statement** (`export/export.go:139-142` writes only header + rows) → add a preamble/comment row or an `X-Data-Cutoff` header |
| RPT-07 | ✅ | Boards 5–30 ms; `TestBoardsRenderTwelveMonthsWithinBudget` | |

### ADM — Administration
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| ADM-01 | ✅ | Sites, products, materials (`/admin/sites`, `/admin/master-data`); refdata kinds incl. unit, defect_reason, expense_category (`01303_refdata.sql`); rate rules (`workforce/module.go:39-41`); thresholds (`refdata/thresholds.go`); RefDataPage; `TestCRUDCachingETagAndPermissions` | |
| ADM-02 | ✅ | `/audit` search + facets, CSV/XLSX/JSON export (`adminapi/export_reports.go`, curl 200); AuditPage | |
| ADM-03 | ✅ | `/sync/monitor` per device and site: last sync, pending, rejected, open conflicts, clock skew; SyncMonitorPage | |
| ADM-04 | ✅ | `/ops/monitoring/health`, `/ops/jobs`, error capture; critical alerts to `admin.monitor` holders + `OPS_ALERT_EMAIL`; `TestErrorCaptureAndMonitoringAPI`, `TestJobFailureAlertAndBuiltins` | |
| ADM-05 | ✅ | `/data/export/tables[/:table]`, `/all` as CSV/JSON (`dataexport.go:202-204`); DataExportPage | |

### OFF — Offline operation and synchronisation
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| OFF-01 | ✅ | VitePWA injectManifest (`vite.config.ts`), `src-sw/sw.ts` precache + NavigationRoute; e2e T-02 offline relaunch | |
| OFF-02 | ✅ | IndexedDB/Dexie `offline/core/db.ts`, persistent storage request `core/quota.ts`; e2e T-02 | |
| OFF-03 | ✅ | `core/store.ts` writes the encrypted outbox before the "Saved on this device" toast | |
| OFF-04 | ✅ | `core/connectivity.ts` probes `/api/health` (≤5 s, plus online/visibility events); `connectivity.test.ts` | |
| OFF-05 | ✅ | `offline/ui/ConnectionIndicator.tsx` (Online/Offline/Syncing + pending count) in `AppLayout.tsx:174` | |
| OFF-06 | ✅ | WireRecord: uuid, device, user, capturedAt, seq, schemaVersion (`core/store.ts`); `sync_ledger` (`01201_sync.sql`) | |
| OFF-07 | ✅ | `core/engine.ts` auto-starts in the background on reconnect; `engine.test.ts` | |
| OFF-08 | ✅ | Capture order, batches ≤200, exponential back-off + Retry-After, resumable (`engine.ts`, `backoff.ts`); `backoff.test.ts` | |
| OFF-09 | ✅ | Dedup on (uuid, revision) `syncsvc/push.go:383-399`; `TestPushIdempotencyAndDuplicates`; curl: resent batch → `duplicate` | |
| OFF-10 | ✅ | accepted / duplicate / conflict / rejected + reasonCode (`syncsvc/registry.go:52-64`); curl verified | |
| OFF-11 | ✅ | `GET /sync/pull` with (stamp, version, scope) cursor (`syncsvc/pull.go`). Fixed after the audit (migration `03250_sync_watermark.sql`, `pullUpper`): writers of synced rows take a shared advisory lock, then stamp `greatest(xid, watermark)`; a pull raises the watermark before its snapshot and is held back only by in-flight transactions holding that lock in its own schema — the invariant (no row below the cursor can commit later) is proved in the code comment. A stuck writer is bounded by `idle_in_transaction_session_timeout` = 5 min on every pool connection (`db.Connect`). Tests: `TestPullNotStalledByUnrelatedTransactions` (open write txn on another table, another schema's sync lock and another database: pull not stalled; a txn whose xid predates the cursor and then writes a synced row is still delivered), `TestPullDoesNotSkipSlowTransactions`, `TestPullDeltaScopeAndSnapshots` (polling workaround removed) | |
| OFF-12 | ✅ | Signed offline package, 14 days default, configurable via `PUT /sync/settings` (max 60); revoke in AdminDevicesPage; `TestDeviceRevokeStopsSyncAndRefresh` | |
| OFF-13 | ✅ | AES-GCM-256 key wrapped by PBKDF2 (310k) + device secret (`core/crypto.ts`); wiped on sign-out (`runtime.ts`); `crypto.test.ts` | |
| OFF-14 | ✅ | `runtime.ts:418-441` confirm before sign-out with pending/issues; e2e T-10 | Browser-level "clear site data" cannot be intercepted (platform limit) |
| OFF-15 | ✅ | `captured_at` + `received_at`; skew vs configurable `maxClockSkewMinutes` (`push.go:218-220`); `TestClockSkewFlags`; curl: +2 d → `CLOCK_SKEW` | |
| OFF-16 | ✅ | Capture-time role snapshot plus current rules (`registry.go:56-58,211-219`); `TestRevalidationCaptureRoleAndCurrentRules` | |
| OFF-17 | ✅ | `offline/pages/SyncIssuesPage.tsx`: reason, guidance, correct & resubmit, dismiss; e2e T-06 | |
| OFF-18 | ✅ | `sync_conflict` kind `negative_stock` → review queue + notify (`syncsvc/notify.go`); `TestNegativeStockFlagRoutesToReview`; e2e T-04 | |
| OFF-19 | ✅ | Pulls scoped to site + 60 days / open items; worker PII minimised; quota warnings `core/quota.ts` | |
| OFF-20 | ✅ | "Sync now" in ConnectionIndicator; passphrase-encrypted export/import (`offline/transfer.ts`) | |
| OFF-21 | ✅ | `registerType: prompt`; update applied only with no dirty form (`runtime.ts:497-505`); Dexie upgrade migration (`db.ts:100-120`) | |
| OFF-22 | ✅ | Timeouts, batch halving, back-off, server idempotency; e2e T-03 | |

### NFR — Non-functional
| ID | Status | Evidence | Missing → fix |
|---|---|---|---|
| NFR-P1 | ✅ | Fixed after the audit: signed-in traffic is rate-limited per user, not per IP (`app.go`: per-IP flood guard 30 000/min before the session, then per-user 600/min; `/auth/me`, logout, language, offline key/PIN in the per-user `session` group 120/min; login/OTP/reset/refresh stay per IP 30/min) — `app:TestSharedIPUsersAreLimitedPerUser` (60 users on one IP, default limits). `cmd/loadtest run` with DEFAULT limits on a scratch DB from `devtools volume -scale 4 -sites 7 -perf-users 90` (12 months, 10 sites, 106 users, ~737k rows): 50 users × 3 min, 23 099 reads, 127.6 req/s, p50 2 ms, p95 38 ms, p99 80 ms, 100 % < 2 s, 0 errors, 0 × 429 → PASS | Production hardware not yet measured (run the same command on staging) |
| NFR-P2 | ✅ | `loadtest reports`: 172 boards/reports, slowest 329 ms; `TestBoardsRenderTwelveMonthsWithinBudget` (12-month fixture) | Re-run (post-audit) on the 10-site ~737k-row volume DB: 171/172 within 5 s (boards ≤ 334 ms), but **`pettycash-statement` XLSX took 11.6 s** (12k entries) → optimise that export before claiming P2 at that volume |
| NFR-P3 | ✅ | `syncsvc:TestBatchOf200Under10s`; e2e "NFR" scenario: capture ≤300 ms, 200 records at 1 Mbps ≤10 s (runs in the CI `e2e` job) | |
| NFR-P4 | ✅ | `e2e/load-size.mjs` on a preview build: 1.66 MB initial, repeat visit served from the SW cache | |
| NFR-A1 | env | systemd units (`deploy/`), health endpoint, offline-first client | Hosting SLA |
| NFR-R1 | env | `deploy/backup.sh` nightly encrypted pg_dump + rclone off-site with a timer; `deploy/restore-test.sh` | The quarterly restore drill has no timer or runbook date → schedule it |
| NFR-S1 | ✅ | nginx TLS 1.2/1.3 (`deploy/nginx.conf:19,80`); permission on every route; CSRF header guard (`TestCSRFGuard`); parameterised SQL; security headers | TLS termination is environment |
| NFR-S2 | ✅ | `.env` gitignored, only `*.example` tracked | Production least-privilege is a process matter |
| NFR-S3 | ✅ | Encrypted vault; wiped on sign-out and on revoke after the final upload (`runtime.ts:447-461`); `TestRevokedDeviceFinalUpload`; e2e T-08 | |
| NFR-S4 | ✅ | `auth` rate-limit group + DB lockout; sync push limiter (`syncsvc/module.go:21`); `TestAuthEndpointsAreRateLimited` | |
| NFR-D1 | ✅ | `db.Tx` around every posting; ledger-derived balances; `TestWorkflowApprovePostsLedgersAtomically` | |
| NFR-D2 | ✅ | `/privacy/subjects/:type/:id`: detail, JSON/PDF export, corrections, access log; PrivacyPage/SubjectPage; `TestWorkerExportContainsOnlyTheSubject` | |
| NFR-U1 | env | Guided tour, help centre, keyboard-entry e2e | Measure training time at UAT |
| NFR-C1 | env | All e2e runs in Chromium | No Edge or macOS run → add an `msedge` channel |
| NFR-M1 | ✅ | CI `.github/workflows/ci.yml`: gofmt, vet, `go test -race ./...`, pnpm typecheck/test/build, shellcheck; migrations in git; OpenAPI. Fixed after the audit: (1) `internal/testutil` migrates a template database once per migration set and gives each test process its own clone (`CREATE DATABASE … TEMPLATE`), so parallel `go test -race ./...` no longer exhausts `max_locks_per_transaction` (42 packages pass in parallel; dev note in `backend/README.md`); the sync-pull flakiness is gone with OFF-11. (2) New CI job `e2e`: Postgres 17 service, builds API + web, headless Chrome, runs `frontend/e2e/run-ci.sh` (a11y, keyboard-entry, offline-attendance, offline-acceptance; artifacts on failure) | |
| NFR-M2 | ✅ | slog structured logs, error capture (`monitor/capture.go`), job metrics `/ops/jobs`, health with uptime. Fixed after the audit: external probe `deploy/uptime-check.sh` (curl; `/api/health?db=1` now also pings PostgreSQL → 503) with e-mail/webhook alerts, repeat and recovery notices, run from another host by `deploy/btn-ops-uptime.{service,timer}` (every minute) or the `uptime` docker-compose profile; documented in README → *Uptime monitoring* | |
| NFR-SC1 | env | Sites/users are data; indexes in place; b7e34db reports a 10-site / 100-user / 5.9M-row run | Not reproduced; the volume seeding script is not in the repo → commit it |
| NFR-L1 | ✅ | DD/MM/YYYY + Africa/Kigali (`lib/dates.ts:33-39`), RWF, en/rw/fr (+am) catalogs with parity tests (`catalog.test.ts`) | Minor: `formatDate(Date)` uses the UTC day, so 22:00–24:00 UTC (00:00–02:00 Kigali) shows the previous day → format in the Kigali TZ |

## §5 Offline sections

### §5.1 principles — ✅
Local first, event-based records, server-authoritative "Provisional" balances, safe to repeat, visible connection and
per-record state (SyncBadge in 16 offline-capable screens), and an encrypted, user/device-bound store are all in place
(see OFF-03, 06, 09, 13).

### §5.2 offline capability matrix — ✅
| Row | Status | Evidence | Missing → fix |
|---|---|---|---|
| Sign-in on a used device | ✅ | OfflineUnlockScreen, 14-day window | |
| Production entry | ✅ | `production/offline.ts` + sync handler; provisional stock | |
| Attendance & headcount | ✅ | `attendance/offline.ts`; a locked day becomes a correction request | |
| Site stock receipts / issues / adjustments | ✅ | `stock` offline ops + handler; server recalculates | |
| Transfer requests & receipts | ✅ | `transfers/offline.ts`; approval online | |
| Order capture & dispatch notes | ✅ | `sales/offline.ts`, OfflineOrders.tsx; reservation confirmed on sync | |
| Petty cash & expense entries | ✅ | `pettycash`, `expenses`, `income` offline.ts; approve disabled offline | |
| Client / product / stock look-up | ✅ | refdata, sites, products, materials, customers, workers pulls + balances | |
| Cached reports & last dashboard, "as of" | ✅ | `offline/cache.ts` `offlineQueryFn`/`useReportCache`: role dashboard + boards, reports hub catalog + cut-off, finance dashboard / cash flow / unbudgeted / trace, procurement spend / summary / trace, sales orders report, production period summary, workforce summary / headcount are stored AES-GCM-encrypted in the user's offline store (key = screen + user + pinned site + filters; a screen opened offline with new filters shows its last copy, flagged). `ui/OfflineDataBanner.tsx` shows "Offline — data as of DD/MM/YYYY HH:mm" and re-runs those queries on reconnect. Same store rules: unreadable when locked/expired, erased on sign-out/revocation, 60-day prune. PDF/Excel/CSV buttons are disabled offline with a reason (server-rendered, not cached — decision in `offline/README.md`). Tests: `cache.test.ts`; `e2e/offline-viewing.mjs` (reload with no network → dashboard, reports hub and finance dashboard from cache, banner time checked, cache encrypted at rest, refresh on reconnect, wiped on sign-out) | |
| Approvals, user admin, email, live dashboards disabled with a message | ✅ | Shared guard `offline/onlineOnly.tsx`: `<Button onlineOnly>` / `ConfirmDialog onlineOnly` (aria-disabled, tooltip + described-by reason, click only explains) on every approve/reject/verify/void/authorise/record-payment/submit-for-approval/close-reopen and admin-config action (income verify/void, expense approve/reject/overrun/record payment, budget submit/approve/reject/archive, petty cash approve/reject/reconciliation review, payroll steps/return/pay, production submit/approve/reject/corrections/day close-reopen/settings/targets/recipes, stock adjustment decisions, stock counts, procurement requests/orders, transfers, sales approvals/withdraw, attendance submit/approve/corrections, rate rules, piece rates, delivery charges); `/admin/*`, `/reports/schedules` (email) and `/sync/conflicts` are wrapped in `OnlineOnlyZone` (notice + disabled fieldset). `lib/api` refuses any direct write while offline with the same message and mutations run `networkMode: "always"`, so nothing is paused/replayed; no approval/admin type is registered in the outbox. Tests: `onlineOnly.test.ts`; `e2e/offline-viewing.mjs` (approval step aria-disabled with reason, click → no request, outbox unchanged; admin page notice) | |

### §5.5 conflict rules
| Rule | Status | Evidence | Missing → fix |
|---|---|---|---|
| Stock movements from two devices | ✅ | Both accepted; `negative_stock` review (`syncsvc/push.go:481-490`); T-04 | |
| Production duplicate site/product/batch/date | ⚠️ | `production/sync.go:113-152` holds a `duplicate_batch` Conflict; resolve as additional batch or merge; `TestSyncProductionEntry` | In the UI, **merge is a raw JSON textarea** (`ConflictsPage.tsx:57-71`) that does not prefill `mergeInto`, so the supervisor must hand-type the entry id → add "Merge into existing" and "Keep as additional batch" buttons |
| Attendance by two users | ✅ | `attendance/sync.go:185` Conflict + audit of both values; T-05 | |
| Master data edited offline and centrally | ✅ (N/A) | Master data is read-only offline (no push handler), so no clash can arise | |
| Order status changed centrally while offline edit pending | ✅ | Server wins, offline edit goes to review (`sales/sync.go:93`); `TestOfflineOrderDraftAndDispatchNote` | |
| Offline edit to a locked record | ✅ (deviation) | Becomes a pending correction request (attendance) or a PM-only Conflict (closed production day) instead of Rejected | Meets "correction via approval workflow"; confirm with BTN |
| Offline update to a centrally deleted record | ✅ | Rejected `not_found`; kept locally until dismissed | |
| Both versions of any conflict in the audit trail | ⚠️ | `sync_conflict` stores client_payload + server_data; attendance writes both to AuditLog | `sync.conflict_resolve` audit (`syncsvc/conflicts.go:254`) records only the resolution, and `sync_conflict` is mutable → write both versions into the immutable audit before/after |

## §6.3 Data rules
| Rule | Status | Evidence | Missing → fix |
|---|---|---|---|
| Device UUIDs; readable numbers on acceptance | ✅ | Push keyed by client UUID; `seq` numbering (`seq:TestNextNumberFormatAndConcurrency`) | |
| RWF exact, quantities exact, UTC storage, Kigali display | ✅ | NUMERIC/BIGINT money (no float columns); UTC writes; Kigali formatters | Columns are `timestamp without time zone`, so UTC is a convention only |
| Never hard-delete | ⚠️ | Users, sites, products and clients are deactivated | **Hard DELETEs remain**: draft budgets (`budgets/module.go:1119`), approval thresholds (`refdata/thresholds.go:208`), delivery-charge rules (`sales/charges.go:239`), income attachments (`income/attach.go:183`), sale lines on amendment (`sales/amend.go:250`, versions kept), draft PO/request lines → use status/voided flags (thresholds and charge rules affect audit of past approvals and prices) |
| CSV import validated, with an import report | ✅ | `/data/import/:kind/validate`, templates, history, report (`dataimport.go:51-55`); ImportPage; `TestDryRunCommitIdempotent`, `TestOpeningBalances` | |
| Personal data minimal, access-controlled, exportable | ✅ | Privacy module + access log; worker PII stripped from offline pulls | |

## §7 Business rules
| Rule | Status | Evidence | Missing → fix |
|---|---|---|---|
| 7.2 Transfers | ⚠️ | Six states; rejection needs a reason (400 without one); dispatch posts the source TRANSFER_OUT; a shortfall creates a `transfer_shortfall` adjustment for review; cancel only before In transit, then `reverse`; threshold approval levels; `TestTransferValidationCancelReverseInsufficient`, `TestTransferMultiLevelApproval` | **A request does not require a reason** (`transfers/service.go:74`, `note` optional) → make the reason required in `parseCreate` and the form |
| 7.3 Approvals / petty cash / procurement receipts | ✅ | `approval_threshold` for 7 domains with value bands + role (`approvals.go:25-34`); receipt reference required (`pettycash/validate.go:208`); no self top-up approval (`TestTopUpSelfApprovalBlocked`); periodic reconciliation with differences (`TestReconcileDifference`); over/under receipt needs a reason + exception notification (`procurement/receiving.go:181-298`, `TestEvaluateReceipt`) | |
| 7.4 Wages | ⚠️ | Formula incl. half rate, overtime and piece (`workforce/calc.go`); rate rules admin-only; prepare→check→verify→authorize→pay; locked periods; `wage_adjustment` carried into the next period (`payroll/wages.go:130`) | **The PM review step is not PM-only**: `payroll.check` = PM *and* SUPERVISOR (`permissions.go:133`) → restrict it to PRODUCTION_MANAGER |

## §8 Roles and permissions — ⚠️

The operational roles mostly match. The cells below differ from the SRS (checked against `auth/permissions.go`;
3 cells confirmed with curl).

| Module | Role | SRS | Code |
|---|---|---|---|
| Production | Admin | R | none (curl `/production/reports` → 403) |
| Production | Sales | – | R (`production.view`) |
| Stock | Proc. | R | none (curl `/stock/balances` → 403); `transfers.view` only |
| Stock | Admin | R | none |
| Clients & orders | Prod. Mgr | R A | R only (`sales.approve` = SM, GM, MD) |
| Clients & orders | Admin | R | `customers.manage` only, no `sales.view` |
| Delivery | Stock | C R U | R only, no `deliveries.create` |
| Delivery | Sup., Prod. Mgr | R | C (`deliveries.create`) |
| Delivery | Admin | R | none |
| Attendance & wages | Sup. | C R U (att.) | also `payroll.prepare`, `payroll.check`, `workers.manage` |
| Attendance & wages | Admin | R | none |
| Finance | Prod. Mgr | R | `budgets.view` only |
| Finance | Sales | R | `income.record` only (curl `/expenses`, `/finance/dashboard` → 403) |
| Finance | Proc. | R | `expenses.view` only |
| Finance | Sup., Stock | – | `expenses.record` (Sup. also `pettycash.custodian`) |
| Finance | Admin | R | none |
| Procurement | Admin | R | none |
| Reports | Admin | R | no `reports.view` |
| Users, settings, audit | Top Mgt | R | C R U (`users.manage`, `devices.manage`; MD `settings.manage`) |
| Users, settings, audit | Prod. Mgr | – | `masterdata.manage` |

Fix:
- Give SYSTEM_ADMIN read on each module. It holds `data.export`, which already exposes the raw tables.
- Add PM to `sales.approve`, STOCK_OFFICER to `deliveries.create`, PROCUREMENT_OFFICER to `stock.view`, and Sales/PM
  to the finance views.
- Remove or justify the over-grants.
- Alternatively, record each deviation as approved by BTN.

## §9 Interfaces
| Item | Status | Evidence | Missing → fix |
|---|---|---|---|
| 9.1 UI | ✅ | Responsive layout; rw + fr catalogs cover all backend keys; SyncBadge Pending/Synced/Conflict/Rejected on 16 offline screens; `e2e/a11y.mjs` axe WCAG 2.1 AA at 1280×720 and 768×1024; `e2e/keyboard-entry.mjs` (both in the CI `e2e` job) | |
| 9.2 REST | ✅ | `/api/v1` (`app.go:84`); OpenAPI 3.1 at `/api/v1/openapi.json` (447 paths) + `/docs`; pagination `{items,total,page,pageSize}` (`httpx/json.go:84-98`); errors `{"error":{"code","message"}}`; SMTP templates (`mail/`); CSV/XLSX/PDF | |
| 9.2 Sync API | ✅ | `/sync/push`, `/sync/pull` (cursor delta), `/sync/status` (`syncsvc/module.go:38-41`), rate-limited. Fixed after the audit (`syncsvc/schema.go`): the client sends `X-Sync-Schema` (absent = 1); the server supports min 1 … current 2 (advertised in `/sync/status` → `schema`); below min → 426 `schema_upgrade_required`, above current → 409 `schema_too_new`; each record type declares its supported `schemaVersion` range (`SchemaVersions(min,max)`, default 1..1) and an out-of-range record refuses the whole batch with the same codes, nothing stored. Client: `transport.ts` sends the version, the engine blocks with `blocked: "upgrade"` keeping records pending, and `SchemaUpgradeNotice` shows "Update required" (en/am/rw/fr). `syncsvc:TestSchemaVersionsEnforced`, `engine.test.ts` (upgrade + too-new), `app:TestClientRecordSchemasSupportedByServer` (every client record type's schemaVersion is inside the server's range; attendance accepts 1..2) | |

## §10 Dashboards and scheduled reports

### Dashboards — all ✅ (`dashboard/boards.go`, `widgets.go`, `frontend/src/modules/dashboard/boards.tsx`)
| Dashboard | SRS items → widget |
|---|---|
| Executive / Top Mgt | production vs target `production.vsTarget` · sales & orders `sales.orders` · stock position `stock.position` · revenue `finance.revenue`/`finance.income` · expenses `finance.expenses` · budget status `budgets.status` · site comparison `site.comparison` · alerts `alerts` · sync status of sites `sync.sites` |
| Production Manager | daily production `production.bySite` · defects `production.defects` · material usage & variance `materials.usage` · site stock `stock.position` · pending transfers `transfers.pending` · headcount `workforce.headcount` · open orders `sales.openOrders` · pending deliveries `deliveries.pending` · conflicts `sync.conflicts` |
| Finance | revenue · expenses · petty cash `pettycash.summary` · budget vs actual `budgets.status` · client balances `sales.receivables` · procurement spend `procurement.spend` |
| Procurement | pending requests `procurement.requests` · approved purchases `procurement.approved` · suppliers `procurement.suppliers` · awaiting receipt `procurement.awaitingReceipt` |
| Site Supervisor | own-site production `site.production` · stock `stock.position`/`materials.low` · attendance `workforce.headcount` · pending/unsynced `site.pending` · sync issues `sync.issues` |
| System Admin | users & roles `admin.users` · devices `admin.devices` · sync monitor `sync.sites` · audit log `admin.audit` · job health `admin.jobs`/`admin.errors` |

### Scheduled reports (`migrations/02600_report_schedules.sql:49-58`, `reports/schedule_run.go`, SchedulesPage)
| Report | SRS freq → seeded | SRS recipients → seeded | Status |
|---|---|---|---|
| Daily production | daily → 06:30 | PM, mgmt → PM, GM, MD | ✅ |
| Daily stock | daily → 06:35 | PM, stock officers → PM, STOCK_OFFICER, GM | ✅ |
| Daily headcount/attendance | daily → 06:40 | PM, finance → PM, FM | ✅ |
| Order & sales summary | weekly/monthly → weekly | sales, mgmt → SM, GM, MD, FM | ⚠️ the report key is the PRIMARY KEY, so weekly **and** monthly cannot both run → allow several schedules per report |
| Stock movement & variance | weekly/on demand → weekly + send-now | PM, finance → PM, STOCK_OFFICER, GM | ⚠️ **FINANCE_MANAGER missing** from default recipients → add to the seed |
| Procurement & expense | monthly → monthly | finance, mgmt → FM, PROC, GM, MD | ✅ |
| Wage & workforce | per wage period → `wage_period` | finance, PM → FM, PM, GM | ✅ |
| Budget monitoring | monthly → monthly | finance, mgmt → FM, GM, MD | ✅ |
| Site performance | monthly → monthly | mgmt → GM, MD, OA, PM | ✅ |

All nine are seeded `enabled=false` (go-live switch-on needed).

## §12.2 Offline acceptance scenarios — all ✅
Every scenario is automated in `frontend/e2e/offline-acceptance.mjs` (real Chromium against the API). Backend tests
back them up:

| T | e2e | Go test |
|---|---|---|
| T-01 | `:431` | module sync tests (`TestSyncAttendance`, `TestSyncProductionEntry`, `TestStockMovementSyncHandler`) |
| T-02 | `:521` | browser-only |
| T-03 | `:581` | `TestPushIdempotencyAndDuplicates` |
| T-04 | `:623` | `TestNegativeStockFlagRoutesToReview` |
| T-05 | `:677` | `TestConflictCreationAndResolution`, `TestSyncDoubleEntryLockAndOvertime` |
| T-06 | `:727` | `TestRevalidationCaptureRoleAndCurrentRules` |
| T-07 | `:775` | `TestOfflinePackageSignature` |
| T-08 | `:810` | `TestDeviceRevokeStopsSyncAndRefresh`, `TestRevokedDeviceFinalUpload` |
| T-09 | `:845` | `TestClockSkewFlags`, `TestPushOrderingGapsAndLate` |
| T-10 | `:493` | browser-only |

This audit verified what each scenario asserts and the backend halves. The browser suite was not re-run, and it does
not run in CI.

## Found when running the system
1. ~~**Delta-pull stall** (OFF-11)~~ — fixed: only in-flight writers of synced rows in the same schema hold pull back
   (`TestPullNotStalledByUnrelatedTransactions`).
2. ~~**Parallel backend test run fails**~~ — fixed: per-process clones of a once-migrated template database.
3. ~~**`/auth/me` is rate-limited at 30/min per IP**~~ — fixed: signed-in calls are limited per user; the load test
   passes with the default limits.
4. Monitoring correctly flagged SMTP as critical (expected, because the audit pointed SMTP at a dead port).
