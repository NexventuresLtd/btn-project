package payroll

// Notifications for the wage-sheet workflow (SRS RPT-04/05): each approval step tells the holders of the next
// step's permission at the sheet's site; payment tells wage viewers and the preparer.

import (
	"context"
	"fmt"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/notify"
)

// Notification events.
const (
	EventStepPending = "payroll.step_pending"
	EventPaid        = "payroll.paid"
	// WRK-01: a site supervisor registered a casual worker (to Finance) / Finance approved it (to the supervisor).
	EventWorkerRegistered = "payroll.worker_registered"
	EventWorkerApproved   = "payroll.worker_approved"
)

// nextStep is the step that follows each step.
var nextStep = map[Step]Step{StepPrepare: StepCheck, StepCheck: StepVerify, StepVerify: StepAuthorize, StepAuthorize: StepPay}

func init() {
	notify.RegisterEvent(notify.Event{Key: EventStepPending, Category: notify.CatApprovals, DefaultEmail: true,
		Permissions: []auth.Permission{"payroll.check", "payroll.verify", "payroll.authorize", "payroll.pay"}})
	notify.RegisterEvent(notify.Event{Key: EventWorkerRegistered, Category: notify.CatApprovals, DefaultEmail: true,
		Permissions: []auth.Permission{"workers.manage"}})
	notify.RegisterEvent(notify.Event{Key: EventWorkerApproved, Category: notify.CatWorkforce, Permissions: []auth.Permission{"workers.register"}})
	notify.RegisterEvent(notify.Event{Key: EventPaid, Category: notify.CatWorkforce, Permissions: []auth.Permission{"wages.view", "payroll.prepare"}})
}

func notifyStep(ctx context.Context, q db.DBTX, p *periodHead, step Step, u *auth.User, totals Totals) error {
	next, ok := nextStep[step]
	if !ok {
		return nil
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventStepPending, Severity: notify.Info,
		TitleKey: "payroll.notify.step_title." + string(next), BodyKey: "payroll.notify.step_body",
		Params: map[string]any{"number": p.Number, "plant": p.Plant.Code, "by": u.Name, "net": format.RWF(float64(totals.Net)), "workers": totals.Workers},
		Link:   "/payroll/" + p.ID, DedupKey: p.ID + ":" + string(next),
		To: notify.ToPermissionAtSite(auth.Permission(StepPermission[next]), p.PlantID).Without(u.ID)})
}

func notifyPaid(ctx context.Context, q db.DBTX, p *periodHead, u *auth.User, workers, net int) error {
	aud := notify.ToPermissionAtSite("wages.view", p.PlantID)
	if p.PreparedBy != nil {
		aud.UserIDs = append(aud.UserIDs, *p.PreparedBy)
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventPaid, Severity: notify.Info,
		TitleKey: "payroll.notify.paid_title", BodyKey: "payroll.notify.paid_body",
		Params: map[string]any{"number": p.Number, "plant": p.Plant.Code, "by": u.Name, "net": format.RWF(float64(net)), "workers": fmt.Sprint(workers)},
		Link:   "/payroll/" + p.ID, To: aud.Without(u.ID)})
}

// notifyRegistered tells Finance (workers.manage) that a worker registration waits for approval.
func notifyRegistered(ctx context.Context, q db.DBTX, w workerOut, u *auth.User) error {
	return notify.Send(ctx, q, notify.Notification{Event: EventWorkerRegistered, Severity: notify.Warning,
		TitleKey: "payroll.notify.worker_registered_title", BodyKey: "payroll.notify.worker_registered_body",
		Params: map[string]any{"name": w.FullName, "plant": w.Plant.Code, "by": u.Name}, Link: "/workforce?tab=workers&registration=PENDING",
		To: notify.ToPermission("workers.manage").Without(u.ID), DedupKey: "worker-reg:" + w.ID})
}
