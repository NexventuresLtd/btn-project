// Package procurement — purchase requests with multi-level approvals, supplier directory, purchase orders,
// goods receipts into HQ/site stock, supplier invoices and the request → payment trace (P6: SRS PRC-01…PRC-08,
// STK-09, FIN-05). Mounted at /api/procurement (and /api/v1/procurement) by internal/app/app.go.
//
// Rules live in workflow.go (pure, unit-tested); HTTP handlers in requests.go, orders.go, receiving.go,
// suppliers.go, invoices.go, attachments.go, reports.go; PDFs in pdf.go; offline sync in sync.go.
package procurement

import (
	"context"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/jobs"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

type handler struct{ d *deps.Deps }

// Permissions.
const (
	permView    auth.Permission = "procurement.view"
	permRequest auth.Permission = "procurement.request"
	permApprove auth.Permission = "procurement.approve"
	permManage  auth.Permission = "procurement.manage"
	permReceive auth.Permission = "procurement.receive"
)

// Register mounts the module on r (already behind LoadSession + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	anyProc := auth.RequirePermission(permView, permRequest, permApprove, permManage, permReceive)
	viewOrManage := auth.RequirePermission(permView, permManage)
	orderReaders := auth.RequirePermission(permView, permManage, permReceive)
	pdfLimit := ratelimit.Group("pdf")

	r.Get("/summary", anyProc, h.summary)

	r.Get("/suppliers/options", anyProc, h.supplierOptions)
	r.Get("/suppliers", viewOrManage, h.listSuppliers)
	r.Get("/suppliers/:id", viewOrManage, h.getSupplier)
	r.Get("/suppliers/:id/orders", viewOrManage, h.supplierOrders)
	r.Post("/suppliers", auth.RequirePermission(permManage), h.createSupplier)
	r.Patch("/suppliers/:id", auth.RequirePermission(permManage), h.updateSupplier)

	r.Get("/requests", anyProc, h.listRequests)
	r.Get("/requests/low-stock", auth.RequirePermission(permRequest), h.lowStock)
	r.Post("/requests/from-low-stock", auth.RequirePermission(permRequest), h.fromLowStock)
	r.Get("/requests/:id", anyProc, h.getRequest)
	r.Post("/requests", auth.RequirePermission(permRequest), h.createRequest)
	r.Patch("/requests/:id", auth.RequirePermission(permRequest), h.updateRequest)
	r.Post("/requests/:id/submit", auth.RequirePermission(permRequest), h.submitRequest)
	r.Post("/requests/:id/recall", auth.RequirePermission(permRequest), h.recallRequest)
	r.Post("/requests/:id/revise", auth.RequirePermission(permRequest), h.reviseRequest)
	r.Post("/requests/:id/approve", auth.RequirePermission(permApprove), h.approveRequest)
	r.Post("/requests/:id/reject", auth.RequirePermission(permApprove), h.rejectRequest)
	r.Post("/requests/:id/close", auth.RequirePermission(permRequest, permManage), h.closeRequest)

	r.Get("/orders", orderReaders, h.listOrders)
	r.Get("/orders/approved-lines", auth.RequirePermission(permManage), h.approvedLines)
	r.Get("/orders/:id", orderReaders, h.getOrder)
	r.Post("/orders", auth.RequirePermission(permManage), h.createOrder)
	r.Patch("/orders/:id", auth.RequirePermission(permManage), h.updateOrder)
	r.Post("/orders/:id/send", auth.RequirePermission(permManage), h.sendOrder)
	r.Post("/orders/:id/cancel", auth.RequirePermission(permManage), h.cancelOrder)
	r.Post("/orders/:id/close", auth.RequirePermission(permManage), h.closeOrder)
	r.Post("/orders/:id/receipts", auth.RequirePermission(permReceive), h.receive)
	r.Post("/orders/:id/invoices", auth.RequirePermission(permManage), h.addInvoice)

	r.Post("/attachments", anyProc, ratelimit.Group("upload"), h.uploadAttachment)
	r.Get("/attachments/:id", anyProc, h.downloadAttachment)

	r.Get("/trace", auth.RequirePermission(permView, "expenses.view"), h.trace)
	r.Get("/reports/spend", auth.RequirePermission(permView), h.spendReport)

	r.Get("/pdf/spend", auth.RequirePermission(permView), pdfLimit, h.pdfSpend)
	r.Get("/pdf/orders/:id", orderReaders, pdfLimit, h.pdfOrder)
	r.Get("/pdf/requests/:id", anyProc, pdfLimit, h.pdfRequest)

	registerSync()
	registerTracer(d)
	jobs.Register(jobs.Job{Name: JobLowStock, Schedule: "15 6 * * *", Timeout: 2 * time.Minute, Retries: 2, Backoff: time.Minute,
		DescriptionKey: "procurement.job.low_stock", Run: func(ctx context.Context) error { _, err := runLowStockJob(ctx, d.Pool); return err }})
}
