# SRS requirement text — NXV-BTN-SRS-001 v1.0 (19 Oct 2026)

Transcribed from the SRS (BTN Management & Operations System, Nexventures Ltd) for traceability.
Priority: M = Must, S = Should, C = Could. "Shall" = mandatory.

## 4.1 Access control and users (AUTH)
- AUTH-01 (M) Authenticate users by email or username and password, with passwords hashed using a modern adaptive algorithm (Argon2id or bcrypt) and never stored or logged in clear text.
- AUTH-02 (M) Enforce role-based access control so that each user sees only the functions and data permitted by their role(s).
- AUTH-03 (M) Restrict site supervisors to the data of the site(s) assigned to them.
- AUTH-04 (M) Support password reset by email with single-use, time-limited links, and forced password change on first login.
- AUTH-05 (M) Lock an account for a configurable period after repeated failed logins and record the attempts.
- AUTH-06 (M) Issue short-lived access tokens and refresh tokens, and allow an administrator to revoke a user's sessions and devices.
- AUTH-07 (M) Record an immutable audit log of sign-ins, permission changes and all create/update/delete/approve actions, with user, time, device and before/after values.
- AUTH-08 (M) Allow administrators to create, deactivate and reactivate users without deleting their history.
- AUTH-09 (S) Support optional two-factor authentication for finance and administrator roles.
- AUTH-10 (M) Support an offline unlock method (see OFF-12) for users who have previously signed in on that device.

## 4.2 Production management (PRD)
- PRD-01 (M) Let a supervisor record daily production per site, date, product and batch: quantity of finished units produced.
- PRD-02 (M) Record defective or rejected units with a configurable reason code and optional note.
- PRD-03 (M) Record raw-material consumption per production entry (e.g. cement, sand, aggregate, water) using defined units of measure.
- PRD-04 (M) Automatically update site finished-product stock and raw-material stock when a production entry is submitted.
- PRD-05 (S) Allow planned production targets per site, product and period and display actual against planned.
- PRD-06 (M) Prevent duplicate entries for the same site, product, batch and date unless explicitly marked as an additional batch.
- PRD-07 (M) Allow a supervisor to edit own entries until the day is closed; later corrections shall require manager approval and be audited.
- PRD-08 (M) Provide site and consolidated production summaries by day, week, month and custom period, with yield and defect rate.
- PRD-09 (S) Support free-text production notes and exception flags (machine breakdown, weather, shortage).
- PRD-10 (S) Calculate expected raw-material usage from configured recipes (material per unit) and flag variances above a configurable tolerance.

## 4.3 Stock and warehouse management (STK)
- STK-01 (M) Maintain separate stock records for HQ and each site for raw materials and finished products.
- STK-02 (M) Maintain a catalogue of materials and products with category, unit of measure and reorder level.
- STK-03 (M) Every stock change shall be an immutable stock movement (receipt, issue, consumption, production, transfer out/in, adjustment, dispatch) with user, date, quantity, location and reference; balances shall be derived from movements.
- STK-04 (M) Support transfer requests between HQ and sites and between sites, with states Requested, Approved, In transit, Received, Rejected and Cancelled.
- STK-05 (M) Prevent a movement that would make a balance negative, except for offline movements which shall be accepted and flagged under OFF-18.
- STK-06 (M) Support periodic stock counts and reconciliation, producing a variance report and requiring approval of adjustments.
- STK-07 (M) Raise low-stock and reorder alerts in-app and by email when balances fall below the reorder level.
- STK-08 (M) Show stock movement history filterable by item, location, type, user and period.
- STK-09 (M) Create stock receipts from approved procurement orders (see PRC-06).
- STK-10 (C) Record batch or lot references for raw materials where BTN requires traceability.

## 4.4 Clients and order management (ORD)
- ORD-01 (M) Maintain a central client database: name, type, contacts, address, TIN where applicable, notes and status.
- ORD-02 (S) Prevent likely duplicate clients by warning on matching name, phone or TIN.
- ORD-03 (M) Create orders with line items (product, quantity, unit price, discount), delivery requirements, dates and payment terms.
- ORD-04 (M) Move orders through the lifecycle in Figure 3, with approval rules configurable by value and role. Figure 3: Draft → Quoted → Confirmed → In production / from stock → Ready for dispatch → Dispatched → Delivered → Closed. Cancelled is reachable from Draft, Quoted or Confirmed (with reason and approval). Partial dispatch keeps the order open until all quantities are delivered.
- ORD-05 (M) Check available finished stock against order lines and show shortfalls to the Production Manager.
- ORD-06 (M) Record payments and balances per order (deposit, part-payment, balance) and show client payment status.
- ORD-07 (M) Keep complete, searchable order and communication history per client.
- ORD-08 (S) Generate printable quotations, order confirmations and delivery notes with BTN branding as PDF.
- ORD-09 (M) Report orders by client, product, status and period.
- ORD-10 (M) Prevent editing of a confirmed order's commercial terms except through an approved amendment that is audited.

## 4.5 Delivery and fulfilment (DEL)
- DEL-01 (M) Record, per order, whether transport is provided by the client (own truck) or by BTN.
- DEL-02 (M) Where BTN provides delivery, apply a delivery charge (fixed or by zone/distance rule configured by BTN) to the order.
- DEL-03 (M) Record dispatches (date, vehicle/driver, quantities, destination) linked to the order, supporting partial dispatches.
- DEL-04 (M) Dispatch shall reduce finished stock at the dispatching location automatically.
- DEL-05 (S) Record delivery confirmation (date, receiver name, optional photo or note).
- DEL-06 (M) Report deliveries by client, order, date and delivery arrangement and list pending deliveries.

## 4.6 Workforce attendance and wages (WRK)
- WRK-01 (M) Maintain worker records (name, ID number, phone, site, rate basis, status) for non-permanent wage workers, protected as personal data.
- WRK-02 (M) Let a supervisor record daily attendance per worker (present, absent, half-day, overtime hours) for the supervisor's site, and a daily headcount.
- WRK-03 (M) Calculate wages for a period from attendance and BTN-approved rate rules (daily rate, piece rate, overtime), showing the calculation per worker.
- WRK-04 (M) Record wage payments (date, method, amount, reference, paid by) and keep payment history per worker.
- WRK-05 (M) Lock approved attendance and paid wage periods; corrections shall need manager approval and be audited.
- WRK-06 (M) Provide site and consolidated workforce reports (headcount, attendance, wage cost per site and period).
- WRK-07 (S) Prevent a worker from being recorded present at two sites on the same day without a flagged exception.

## 4.7 Finance management (FIN)
- FIN-01 (M) Record expenses with category, amount, date, site/department, supplier, payment method and attachment (receipt image or PDF).
- FIN-02 (M) Manage petty cash: float per custodian, requests, approvals, expenditure entries and reconciliation, with full history.
- FIN-03 (M) Support budgets by category and department for a period, and report actual versus budget with variance.
- FIN-04 (M) Track client revenue from orders and payments, including outstanding balances.
- FIN-05 (S) Link procurement, stock receipts and expenses so that a purchase can be traced to its approval, receipt and payment.
- FIN-06 (M) Provide a finance dashboard with revenue, expenses, petty cash position, budget status and outstanding balances.
- FIN-07 (M) Export financial reports to CSV and Excel for use in BTN's accounting.
- FIN-08 (S) Warn when an expense would exceed the remaining budget and require approval to proceed.
- FIN-09 (M) Restrict financial data to roles with finance permissions.

## 4.8 Procurement management (PRC)
- PRC-01 (M) Let authorised users create procurement requests with items, quantities, justification, required date and estimated cost.
- PRC-02 (M) Route requests through a configurable approval workflow based on value and requester.
- PRC-03 (M) Maintain a supplier directory with contacts, categories and purchase history.
- PRC-04 (M) Record purchase orders and supporting references (quotation, invoice number).
- PRC-05 (M) Track procurement status: Draft, Submitted, Approved, Ordered, Partially received, Received, Closed, Rejected.
- PRC-06 (M) Support receiving purchased items into HQ or site stock, partially or fully, creating stock receipts.
- PRC-07 (C) Propose procurement requests automatically from low-stock alerts.
- PRC-08 (M) Report procurement and expenditure by supplier, category and period.

## 4.9 Reporting, dashboards and notifications (RPT)
- RPT-01 (M) Provide role-specific dashboards as listed in Section 10.
- RPT-02 (M) Generate daily production, stock and headcount reports automatically and email them to configured recipients.
- RPT-03 (M) All reports shall be filterable by site, date range and relevant dimensions, and exportable to PDF and CSV/Excel.
- RPT-04 (M) Provide an in-app notification centre for alerts such as low stock, pending approvals, transfer requests, sync conflicts and procurement events.
- RPT-05 (M) Send email notifications for configured events and let users choose which they receive.
- RPT-06 (M) Reports shall state the data cut-off time and, where data from offline devices is pending, the number of sites with unsynchronised data.
- RPT-07 (M) Dashboards shall load within the time in NFR-P2 for typical data volumes.

## 4.10 Administration (ADM)
- ADM-01 (M) Allow administrators to manage reference data: sites, products, materials, units, reason codes, expense categories, rate rules and approval thresholds.
- ADM-02 (M) Provide an audit-log viewer with search and export.
- ADM-03 (M) Provide a sync monitor showing, per device and site, last successful sync, pending records, rejected records and open conflicts.
- ADM-04 (M) Provide health, error and job monitoring visible to administrators and alerting Nexventures on critical failures.
- ADM-05 (M) Support data export by authorised users in open formats (CSV, JSON).

## 5 Offline operation and synchronisation
Principles (5.1): local first (saved to the device before the screen confirms; network never blocks capture); event-based
records; server authority for shared totals (device shows a clearly labelled provisional balance); safe to repeat
(client UUID, re-upload has no extra effect); visible connection status and per-record state (Pending, Synced, Conflict,
Rejected); data on the device encrypted and bound to user and device.

Offline capability matrix (5.2): sign-in on a previously used device — yes (offline PIN / cached credential within the
allowed window); production entry — create/edit, saved locally, site stock shown as provisional; attendance and headcount
— create/edit, saved locally, locked on approval after sync; site stock receipts, issues, adjustments — create, saved as
movements, balance recalculated on sync; transfer requests and receipts — create/receive, queued, approval requires
online; order capture and dispatch notes — create/update, saved locally, stock reservation confirmed on sync; petty cash
and expense entries — create, queued, approval requires online; client/product/stock look-up — read, cached as of last
sync; cached reports and last dashboard — read, marked with "as of" time; approvals, user admin, email sending, live
dashboards — not offline, disabled with an explanatory message, queued where applicable.

- OFF-01 (M) Installable as a PWA; its application shell shall load with no network after the first successful load.
- OFF-02 (M) Keep working through loss of internet or power: on restart after power loss the user resumes with all previously saved records intact.
- OFF-03 (M) Save each offline entry to local storage before confirming it to the user and persist the outbox across browser and device restarts.
- OFF-04 (M) Detect connectivity changes within 5 seconds, and test real reachability of the server and not only network-interface state.
- OFF-05 (M) Show a persistent connection indicator (Online, Offline, Syncing) and the count of pending records.
- OFF-06 (M) Each locally stored record shall carry a client UUID, device ID, user ID, local creation time, monotonic sequence number and schema version.
- OFF-07 (M) On reconnection start synchronisation automatically, in the background, without user action and without interrupting work in progress.
- OFF-08 (M) Upload outbox records in capture order, in batches, with retry using exponential back-off, resumable after interruption at any point.
- OFF-09 (M) The server shall process uploads idempotently: a record whose UUID was already applied shall be acknowledged and not re-applied.
- OFF-10 (M) Return a per-record result (Accepted, Duplicate, Conflict, Rejected with reason) and update local status accordingly.
- OFF-11 (M) After upload, download changes made centrally since the last sync (delta sync) so the device holds current reference data, balances, and records for the user's scope.
- OFF-12 (M) A user who has signed in on a device may unlock it offline with a PIN or password for a configurable period (default 14 days), after which online sign-in is required. Administrators may revoke a device, effective at the next sync.
- OFF-13 (M) Locally stored data encrypted at rest using a key derived from the user's credentials and device, and removed on sign-out when the outbox is empty.
- OFF-14 (M) Block sign-out and data clearing while unsynchronised records exist unless the user explicitly confirms, and warn about the risk of data loss.
- OFF-15 (M) Record both the capture time (device clock) and the server receipt time; detect device clock skew greater than a configurable threshold and flag affected records.
- OFF-16 (M) Apply server-side validation and permission checks again at sync, using the user's role at the time of capture and the current rules; records failing validation are Rejected with a reason and retained locally for correction.
- OFF-17 (M) Provide a "Sync issues" screen listing rejected and conflicting records, with guidance and the ability to correct and resubmit.
- OFF-18 (M) Offline stock issues that would cause a negative balance shall be accepted, flagged "negative stock" and sent to the Production Manager for review, not silently dropped.
- OFF-19 (S) Limit the offline dataset to the user's scope (site, recent period, active items) and manage storage quota, warning users before the quota is reached.
- OFF-20 (S) Support a manual "Sync now" action and a forced export of pending records to an encrypted file as a last-resort backup.
- OFF-21 (M) Application updates downloaded in the background and applied only when no unsynchronised edit forms are open; the local schema migrates without loss.
- OFF-22 (M) Keep working with intermittent or slow connections without duplicating or losing records.

Conflict rules (5.5): stock movements from two devices — both accepted as events, balance recomputed, negative flagged
for manager review; production entry duplicate site/product/batch/date — second held as Conflict, supervisor chooses
merge or additional batch; attendance — same worker recorded by two users held as Conflict, site supervisor resolves,
one kept, both in audit; master data edited offline and centrally — field-level merge, same-field clash needs a manager
decision; order status changed centrally while offline edit pending — server status wins, offline change shown for
review; offline edit to a locked record — rejected with reason, correction via approval workflow; offline update to a
record deleted centrally — rejected, user informed, local copy kept until dismissed. Both versions of any conflict are
kept in the audit trail.

Failure modes (5.6) and sync API (9.2): `/sync/push`, `/sync/pull` (delta by server cursor), `/sync/status`;
idempotent; backward-compatible for at least one client release.

## 6.3 Data rules
- Offline records use device-generated UUIDs; human-readable numbers assigned by the server on acceptance.
- Money in RWF as exact values; quantities exact; timestamps stored in UTC, displayed in Africa/Kigali time.
- Records never hard-deleted; deletion is a status change retained for audit. Retention per policy (default 7 years for financial records).
- Initial load of reference data and opening stock balances by CSV import validated against the schema, with an import report.
- Personal data collected only as needed, access-controlled and exportable on request.

## 7 Business rules
- 7.2 Transfers: Requested (by site or HQ officer, quantity + reason) → Approved/Rejected (Production Manager or per threshold; rejection needs reason) → In transit (source issue movement) → Received (receiving location confirms quantity; shortfall creates an adjustment for review); Cancelled allowed before In transit, afterwards only by reversal movement.
- 7.3 Approval levels configured by value; petty cash entries need a receipt reference; a custodian cannot approve their own top-up; the float is reconciled periodically and differences reported; a procurement receipt above/below ordered quantity needs a reason and creates an exception notification.
- 7.4 Wage = days present × daily rate + half-days × half rate + overtime hours × overtime rate, or piece-rate output; rules configured by an administrator; calculation shown per worker, reviewed by the Production Manager and approved before payment; approved periods locked, later corrections recorded as adjustments in the next period.

## 8 Roles and permissions (C = create, R = read, U = update, A = approve; supervisors limited to their own site)
| Module | Sup. | Prod. Mgr | Stock | Sales | Fin. | Proc. | Top Mgt | Admin |
|---|---|---|---|---|---|---|---|---|
| Production | C R U | R U A | R | – | R | – | R | R |
| Stock | C R (site) | R A | C R U | R | R | R | R | R |
| Clients & orders | R | R A | R | C R U A | R | – | R | R |
| Delivery | R | R | C R U | C R U | R | – | R | R |
| Attendance & wages | C R U (att.) | R A | – | – | R U | – | R | R |
| Finance | – | R | – | R | C R U A | R | R A | R |
| Procurement | C R | R A | R | – | R A | C R U | R A | R |
| Reports | R (site) | R | R | R | R | R | R | R |
| Users, settings, audit | – | – | – | – | – | – | R | C R U |

## 9.1 User interface
Responsive for laptop screens (min 1280×720) and usable on tablets; keyboard-friendly data entry; clear labelling of
units, sites and dates; text externalised for translation (Kinyarwanda, French); offline forms with large targets,
inline validation and auto-save, status badges Pending/Synced/Conflict/Rejected on every offline-capable record;
WCAG 2.1 AA colour contrast and keyboard navigation for core screens.

## 9.2 Software interfaces
REST API: JSON over HTTPS, versioned under /api/v1, OpenAPI 3 documentation, token authentication, pagination,
consistent error format. Sync API as above. Email: SMTP, templates for alerts and scheduled reports. Export: CSV, Excel
and PDF generation for reports.

## 10 Dashboards and scheduled reports
Dashboards — Executive/Top Management: production vs target, sales and orders, stock position, revenue, expenses, budget
status, site comparison, alerts, sync status of sites. Production Manager: daily production and defects by site, material
usage and variance, site stock, pending transfers, headcount, open orders, pending deliveries, conflicts to resolve.
Finance: revenue, expenses, petty cash, budget vs actual, client balances, procurement spend. Procurement: pending
requests, approved purchases, suppliers, items awaiting receipt. Site Supervisor: own site production, stock,
attendance, pending/unsynced records, sync issues. System Administrator: users, roles, devices, sync monitor, audit log,
job health.
Scheduled reports — daily production (Production Manager, management); daily stock (Production Manager, stock officers);
daily headcount/attendance (Production Manager, finance); order and sales summary weekly/monthly (sales, management);
stock movement and variance weekly/on demand (Production Manager, finance); procurement and expense monthly (finance,
management); wage and workforce per wage period (finance, production manager); budget monitoring monthly (finance,
management); site performance monthly (management).

## 11 Non-functional requirements
- NFR-P1 (M) 95% of standard pages and API reads respond within 2 s under up to 50 concurrent users on the production configuration.
- NFR-P2 (M) Dashboards and standard reports render within 5 s for up to 12 months of data.
- NFR-P3 (M) Offline capture actions complete locally within 300 ms; a sync batch of 200 records processed within 10 s on a 1 Mbps connection.
- NFR-P4 (S) Initial application load ≤ 3 MB transferred; subsequent loads use the cache.
- NFR-A1 (M) Availability target 99% monthly excluding announced maintenance; offline operation ensures sites are not blocked during outages.
- NFR-R1 (M) Daily backups, RPO ≤ 24 h, RTO ≤ 8 h, quarterly restore test.
- NFR-S1 (M) All traffic over TLS 1.2+; server-side authorisation on every endpoint; protection against OWASP Top 10 (injection, XSS, CSRF, broken access control).
- NFR-S2 (M) Secrets stored outside source control; production access least-privilege and logged.
- NFR-S3 (M) Locally stored offline data encrypted and wiped on revocation or sign-out as in OFF-13.
- NFR-S4 (M) Rate limiting and brute-force protection on authentication and sync endpoints.
- NFR-D1 (M) Financial and stock transactions atomic; stock balances reconcile to the movement ledger at all times.
- NFR-D2 (M) Comply with Law No. 058/2021 for worker and customer data, supporting access, correction and export requests.
- NFR-U1 (S) A new supervisor completes a daily production and attendance entry after no more than two hours of training.
- NFR-C1 (M) Latest two stable versions of Chrome and Edge on Windows 10/11 and macOS.
- NFR-M1 (M) Modular code, automated unit and integration tests, documented API, migrations under version control, CI checks on every change.
- NFR-M2 (S) Structured logs, error tracking, uptime monitoring and job-queue metrics.
- NFR-SC1 (S) Support at least 10 sites, 100 users and 5 million stock/audit records without architectural change.
- NFR-L1 (M) RWF currency, Africa/Kigali time zone, DD/MM/YYYY date format, text externalised for translation.

## 12.2 Offline acceptance scenarios
- T-01 Supervisor signed in, goes offline, records production, attendance and a stock issue, then reconnects → all records reach the server once; balances correct; statuses Synced.
- T-02 Browser closed and laptop restarted (power cut) with pending records → records intact after restart; sync completes later.
- T-03 Connection drops during a batch upload → unacknowledged records re-sent; no duplicates.
- T-04 Two sites issue the last 100 units of the same item at HQ offline → both accepted as events; negative balance flagged to manager.
- T-05 Same worker's attendance entered twice offline by different users → conflict raised; supervisor resolves; audit shows both.
- T-06 Offline record fails validation after rules changed → rejected with reason; remains local; user corrects and resubmits.
- T-07 Device offline beyond unlock window → re-authentication required online; pending data retained.
- T-08 Device revoked by administrator while offline → on next sync device is locked out; local data purged after upload.
- T-09 Device clock set 2 days ahead → skew flagged; ordering by sequence number; server time recorded.
- T-10 User tries to sign out with pending records → warning shown; sign-out blocked unless confirmed.
