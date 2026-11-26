# SRS gap analysis — NXV-BTN-SRS-001 v1.0 vs. what is built

Legend: ✅ done · 🟡 partly done (what is missing is stated) · ❌ not built.
Checked against the code on branch `feat/btn-operations-system` (keyword/route/schema audit plus the
modules built in this project). 122 numbered requirements: **31 ✅ · 41 🟡 · 50 ❌**.

## Summary by module

| Module | Reqs | ✅ | 🟡 | ❌ | Biggest gap |
|---|---|---|---|---|---|
| Access control (AUTH) | 10 | 6 | 3 | 1 | Offline unlock (OFF-12 dependency) |
| Production (PRD) | 10 | 3 | 6 | 1 | Batches, reason codes, recipes/variance |
| Stock (STK) | 10 | 3 | 4 | 3 | Transfer request/approval flow, stock counts, lots |
| Clients & orders (ORD) | 10 | 3 | 6 | 1 | Full order lifecycle, discounts, payment terms |
| Delivery (DEL) | 6 | 3 | 1 | 2 | Own-truck vs BTN, delivery charges |
| Workforce (WRK) | 7 | 1 | 6 | 0 | Overtime, piece/half rates, locking |
| Finance (FIN) | 9 | 2 | 2 | 5 | Expenses, budgets, CSV/Excel export |
| Procurement (PRC) | 8 | 0 | 0 | 8 | Entire module |
| Reporting & notifications (RPT) | 7 | 1 | 2 | 4 | Notifications, scheduled emails, CSV/Excel |
| Administration (ADM) | 5 | 0 | 4 | 1 | Sync monitor, exports, reason codes |
| Offline & sync (OFF) | 22 | 0 | 0 | 22 | Entire offline layer (PWA, outbox, sync) |
| Non-functional (NFR) | 18 | 9 | 7 | 2 | Offline-related targets, CI, error tracking |

## Per requirement

### AUTH
| ID | Status | Notes |
|---|---|---|
| AUTH-01 | ✅ | Argon2id, never logged |
| AUTH-02 | ✅ | Role/permission matrix, Super Admin |
| AUTH-03 | ✅ | Site-scoped roles |
| AUTH-04 | 🟡 | Reset uses a 6-digit emailed code, not a single-use link; forced first-login password change ✅ |
| AUTH-05 | ✅ | Lockout + attempt ledger |
| AUTH-06 | 🟡 | Server sessions (hashed cookie) with admin revoke; no refresh tokens, no per-device list |
| AUTH-07 | 🟡 | Audit log with user, time, IP, before/after; no device id, not DB-enforced immutable |
| AUTH-08 | ✅ | Deactivate/reactivate, history kept |
| AUTH-09 | ✅ | Email OTP for all users + passkeys (exceeds) |
| AUTH-10 | ❌ | No offline unlock |

### PRD
| ID | Status | Notes |
|---|---|---|
| PRD-01 | 🟡 | Per site/date/shift/product; no batch reference |
| PRD-02 | 🟡 | Damage counts (production vs handling), no configurable reason codes |
| PRD-03 | ✅ | Material usage per report |
| PRD-04 | ✅ | Stock posts on approval (SRS says on submit) |
| PRD-05 | 🟡 | Target per report line; no planned targets per period |
| PRD-06 | 🟡 | One report per site/date/shift; no batch / "additional batch" |
| PRD-07 | 🟡 | Edit until submitted/approved; no "day closed" concept |
| PRD-08 | ✅ | Site + consolidated summaries, yield and defect rates (dashboard + PDF) |
| PRD-09 | 🟡 | Downtime + notes; no exception flags (breakdown/weather/shortage) |
| PRD-10 | ❌ | No recipes or variance flags |

### STK
| ID | Status | Notes |
|---|---|---|
| STK-01 | 🟡 | Per site; HQ / warehouses not modelled as locations |
| STK-02 | 🟡 | Catalogue + unit; reorder level on materials only; no categories |
| STK-03 | ✅ | Immutable movement ledger, balances derived |
| STK-05 | ✅ | Negative balances blocked (offline exception N/A) |
| STK-04 | 🟡 | Material transfers are instant OUT/IN pairs; no Requested → Approved → In transit → Received; no finished-goods transfers |
| STK-06 | ❌ | No stock counts / variance approval |
| STK-07 | 🟡 | In-app low-stock alerts; no email alerts |
| STK-08 | ✅ | Ledger filters by item, site, type, period |
| STK-09 | ❌ | Needs procurement; receipts are entered manually |
| STK-10 | ❌ | No batch/lot references |

### ORD
| ID | Status | Notes |
|---|---|---|
| ORD-01 | 🟡 | Name, TIN, phone, email, address; no type, multiple contacts, notes |
| ORD-02 | ❌ | Only exact-name uniqueness; no phone/TIN duplicate warning |
| ORD-03 | 🟡 | Lines, qty, price; no per-line discount, delivery requirements, payment terms |
| ORD-04 | 🟡 | Draft/Confirmed/Cancelled + delivery & payment status; no Quoted / In production / Ready / Closed; approval by discount % only, not by value/role |
| ORD-05 | ✅ | Availability check on confirm |
| ORD-06 | ✅ | Payments, balances, status |
| ORD-07 | 🟡 | Customer statement; no communication log |
| ORD-08 | 🟡 | Order summary + delivery note PDFs; no quotation, no confirmation document |
| ORD-09 | ✅ | Sales report by customer/product/status/period |
| ORD-10 | 🟡 | Confirmed orders are locked; no approved-amendment workflow |

### DEL
| ID | Status | Notes |
|---|---|---|
| DEL-01 | ❌ | No client-truck vs BTN-transport flag |
| DEL-02 | ❌ | No delivery charge rules |
| DEL-03 | ✅ | Partial dispatches with vehicle/driver (no destination field) |
| DEL-04 | ✅ | Dispatch reduces stock |
| DEL-05 | 🟡 | Receiver name + date; no photo/note |
| DEL-06 | ✅ | Deliveries list, pending filter |

### WRK
| ID | Status | Notes |
|---|---|---|
| WRK-01 | 🟡 | Name, ID, MoMo phone, site, daily wage; no other rate bases |
| WRK-02 | 🟡 | Present/half/absent/leave + headcount; no overtime hours |
| WRK-03 | 🟡 | Daily rate × days, filled from attendance; no half-rate, overtime or piece-rate rules |
| WRK-04 | 🟡 | Pay step with MoMo refs → petty cash; no per-worker payment history screen |
| WRK-05 | 🟡 | Wage sheets lock once paid; attendance has no approval/lock |
| WRK-06 | 🟡 | Attendance summary + labour cost PDF; no consolidated headcount report |
| WRK-07 | ✅ | One attendance row per worker per day; worker belongs to one site |

### FIN
| ID | Status | Notes |
|---|---|---|
| FIN-01 | ❌ | No general expense module (categories, supplier, attachments); petty cash only |
| FIN-02 | 🟡 | Petty cash ledger, approvals, vouchers; no per-custodian floats or reconciliation |
| FIN-03 | ❌ | No budgets |
| FIN-04 | ✅ | Revenue, receivables, ageing |
| FIN-05 | ❌ | Depends on procurement |
| FIN-06 | 🟡 | Revenue, petty cash, receivables; no budget status |
| FIN-07 | ❌ | No CSV/Excel export |
| FIN-08 | ❌ | Depends on budgets |
| FIN-09 | ✅ | Finance data behind permissions |

### PRC — all ❌
PRC-01 requests · PRC-02 approval workflow · PRC-03 supplier directory · PRC-04 purchase orders · PRC-05 status tracking ·
PRC-06 receiving into stock · PRC-07 auto-propose from low stock · PRC-08 procurement/expense reports.
(Only a free-text supplier field exists on raw-material receipts.)

### RPT
| ID | Status | Notes |
|---|---|---|
| RPT-01 | 🟡 | One dashboard that adapts to permissions, not distinct per-role dashboards |
| RPT-02 | ❌ | No scheduled/automatic emailed reports (no job runner) |
| RPT-03 | 🟡 | Filters + PDF ✅; no CSV/Excel |
| RPT-04 | ❌ | No notification centre ("Needs your action" card only) |
| RPT-05 | ❌ | No event emails or preferences |
| RPT-06 | ❌ | Reports show generated time, but no data cut-off / pending-sync note |
| RPT-07 | ✅ | Dashboard in milliseconds |

### ADM
| ID | Status | Notes |
|---|---|---|
| ADM-01 | 🟡 | Sites, products, materials, customers, approval limits ✅; no reason codes, expense categories (fixed list), rate rules, free units |
| ADM-02 | 🟡 | Audit viewer with search; no export |
| ADM-03 | ❌ | No sync monitor (no sync) |
| ADM-04 | 🟡 | Health endpoint + structured logs; no admin monitoring page or failure alerting |
| ADM-05 | 🟡 | PDF only; no CSV/JSON export |

### OFF — all ❌ (the largest gap: 22 requirements, almost all Must)
OFF-01 installable PWA / offline shell · OFF-02 survive power loss · OFF-03 local save before confirm · OFF-04 connectivity detection ·
OFF-05 online/offline/syncing indicator · OFF-06 client UUID, device id, sequence per record · OFF-07 automatic background sync ·
OFF-08 ordered, resumable batch upload with back-off · OFF-09 idempotent server processing · OFF-10 per-record results ·
OFF-11 delta download · OFF-12 offline PIN unlock (14 days) + device revoke · OFF-13 encrypted local data ·
OFF-14 block sign-out with unsynced data · OFF-15 clock-skew detection · OFF-16 re-validation at sync ·
OFF-17 "Sync issues" screen · OFF-18 offline negative-stock flagging · OFF-19 scoped dataset / quota · OFF-20 manual sync + encrypted export ·
OFF-21 safe app updates / local schema migration · OFF-22 works on slow/intermittent links.
Also missing: `/sync/push`, `/sync/pull`, `/sync/status` API, Device and Sync Ledger/Conflict tables, conflict rules (5.5), acceptance tests T-01…T-10.

### NFR
| ID | Status | Notes |
|---|---|---|
| NFR-P1 | ✅ | Reads ≈ 1–25 ms |
| NFR-P2 | ✅ | Dashboards/reports well under 5 s |
| NFR-P3 | ❌ | Offline capture and sync batch — no offline layer |
| NFR-P4 | ✅ | ≈ 136 kB initial JS (gzip) |
| NFR-A1 | 🟡 | Depends on hosting; not yet deployed |
| NFR-R1 | 🟡 | Nightly backup script tested; off-site copy and quarterly restore test not set up |
| NFR-S1 | ✅ | TLS at proxy, server-side authorisation, CSRF, injection-safe SQL, security headers |
| NFR-S2 | ✅ | Secrets in env, not in git (note: SMTP password was pasted in chat — rotate) |
| NFR-S3 | ❌ | Offline encryption — no offline layer |
| NFR-S4 | ✅ | Rate limiting on auth, PDFs, uploads (no sync endpoint yet) |
| NFR-D1 | ✅ | Atomic transactions; ledger-derived balances |
| NFR-D2 | 🟡 | Access-controlled personal data; no data-subject export/correction workflow |
| NFR-U1 | 🟡 | Guided tour + help centre; training time to be measured at UAT |
| NFR-C1 | ✅ | Chrome/Edge |
| NFR-M1 | 🟡 | 200+ automated tests, migrations in git; no CI pipeline |
| NFR-M2 | 🟡 | Structured logs; no error tracking or uptime monitoring |
| NFR-SC1 | ✅ | Sites are data, not code |
| NFR-L1 | 🟡 | RWF and Africa/Kigali ✅, text externalised ✅; dates show as "14 Nov 2026", SRS asks DD/MM/YYYY; French not provided |

## Data rules (6.3) and roles (2.3, 8)

| Item | Status | Notes |
|---|---|---|
| Client-generated UUIDs for offline records | ❌ | IDs are server-generated |
| Server-assigned readable numbers | ✅ | SO-, PC-, WG-, PR- |
| RWF exact; UTC timestamps; Kigali display | ✅ | |
| Never hard-delete | 🟡 | Users, sites, products, customers are deactivated, not deleted. Draft lines are replaced on edit and sessions are deleted |
| CSV import of reference data and opening balances | 🟡 | Opening-balance screens ✅; the Excel importer is specific to BTN's old sheets, no generic CSV import |
| Stock/Warehouse Officer, Procurement Officer roles | ❌ | Roles don't exist yet (Procurement needs the module first) |
| Top Management, Sales, Finance, Supervisor, Prod. Manager, Admin | ✅ | Present (plus Super Admin, Operations Advisor, Lead Operator, Operator) |

## Beyond the SRS (built, not requested)

Passkeys · Super Admin · editable branding (logo, colours, font, text size) · Super-Admin-editable system text ·
Amharic as a third language · guided tour and help centre · portrait/landscape choice for every PDF ·
site management page · Excel importer with reconciliation.

## Decisions for the team

1. **Stack differs from SRS §3.2** (Python/FastAPI, Next.js, Celery + Redis). Built on Go + React/Vite. The SRS says the stack may be adjusted in discovery — confirm with BTN.
2. **Offline-first is the SRS's core driver** and is not built. It is the largest remaining piece of work and needs a design decision on scope (see below).
3. **Languages:** SRS risk #9 lists English, Kinyarwanda, French. French is not built; Amharic was added instead.
