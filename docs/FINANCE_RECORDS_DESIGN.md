# Finance records — design (income, expenses, budgets)

**Principle: the system keeps records; it never moves money.** There is no payment gateway and none is planned.
Every payment (cash, MTN MoMo, Airtel Money, bank transfer, cheque) happens outside the system and is *recorded*
here with its date, method, amount, reference and who recorded it. Every report is built from these records.
No endpoint, screen, job or document may initiate, authorise or confirm a transfer with a bank or mobile-money
provider. Wording follows this everywhere: "Record payment", "Record receipt", "Mark as paid (recorded)" — never "Pay".

## 1. Two record types cover all money

| Record | Meaning | Examples |
|---|---|---|
| **Income** (money in) | Any money received by BTN | customer payments on orders, deposits, delivery charges collected, sale of scrap/waste, rental, refunds received, grants, interest, other |
| **Expense** (money out) | Any money spent by BTN | supplier invoices (procurement), petty-cash spending, wages paid, transport, fuel, internet, maintenance, utilities, other |

Petty cash is **not a separate kind of money**: it is a *source of funds* (a float held by a custodian). Spending from a
float is an **expense** with `source = PETTY_CASH` and the float it came from. Float top-ups are internal transfers
(they move cash into a float, they are not income or expense). The float ledger is therefore a view: top-ups in,
petty-cash expenses out, reconciliation counts.

## 2. Expense
Fields: number, date, amount, category (refdata `expense_category`), description, site/location, department,
**source** (DIRECT | PETTY_CASH | PROCUREMENT | PAYROLL | OTHER), payment method as recorded (cash, MoMo, bank, cheque,
other) + reference, payee (supplier from the directory or free text), receipt attachment(s), status
(DRAFT → SUBMITTED → APPROVED → PAID-recorded | REJECTED | CANCELLED), approvals by value.
**Budget link (explicit):** `budget_line_id` (nullable). On entry the system *suggests* the matching line
(same department + category + period); the user can pick another line of an approved budget or mark the expense
**Unbudgeted** (with a reason). The budget check (FIN-08) uses the chosen line; unbudgeted expenses above the approval
threshold need budget-overrun approval. Budget vs actual and an "Unbudgeted spending" report use the explicit link.
**Links to everything relevant** (`expense_link`: entity type + id, many per expense) — purchase request, purchase
order, goods receipt, supplier invoice, payroll period / worker wage payment, production report or day (e.g. fuel,
breakdown repair), transfer (transport), delivery/order (delivery costs), stock count/adjustment, worker, customer,
site/location, asset/vehicle (free reference). Typed foreign keys for the common ones (purchase order, payroll period,
float) plus the generic link table; every linked record shows its expenses, and the expense page shows its links.
Automatic creation: supplier invoice on a PO → PROCUREMENT expense (already built); recording a wage-sheet payment →
PAYROLL expense(s) linked to the period and workers; petty-cash spending → PETTY_CASH expense linked to the float.

## 3. Income
Fields: number, date, amount, category (refdata `income_category`: SALES, DEPOSIT, DELIVERY_CHARGE, SCRAP_SALES,
RENTAL, REFUND_RECEIVED, GRANT, INTEREST, OTHER), description, site/location, department, **source**
(ORDER_PAYMENT | OTHER), payer (customer from the client database or free text), method as recorded + reference,
attachment (receipt/slip), status (RECORDED → VERIFIED by finance | VOID with reason — never deleted),
budget line (revenue line, optional) and links (`income_link`: order, customer, delivery, invoice/EBM ref, site, other).
Recording a payment on a sales order **is** recording an income (ORDER_PAYMENT, linked to order + customer); existing
order payments are migrated to income records. Other income is entered directly on the Income page.

## 4. Budgets
Budget lines get a **type**: EXPENSE or INCOME (revenue target). Budget vs actual shows both: expenses against expense
lines (explicit links, plus suggested matches for legacy records) and income against revenue lines; net position.

## 5. Reports (screen + PDF + CSV/Excel/JSON, cut-off note)
Income report (by category, source, customer, site, period); Expense report (by category, source, supplier, site,
department, budget line, linked entity); Cash flow / income vs expense by site and period; Budget vs actual (income +
expense) ; Unbudgeted spending; Petty-cash float statement (top-ups, petty-cash expenses, counts); Linked-records
trace (any entity → its income/expenses). Finance dashboard: income, expenses, net, budget status, receivables,
floats. Scheduled monthly report updated accordingly.

## 6. Naming changes (no payment processing)
`POST /api/expenses/:id/pay` → `POST /api/expenses/:id/record-payment`; `POST /api/payroll/periods/:id/pay` →
`.../record-payment`; order payments stay `POST /api/sales/:id/payments` (records an income). Old paths are removed
from the UI and kept one release as aliases. UI labels, notifications, help guides, PDFs and docs say "recorded".
ARCHITECTURE.md states the record-keeping principle; SRS future gateway integrations are out of scope.
