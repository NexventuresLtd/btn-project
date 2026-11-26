package stock

// Multi-level approvals for P2 documents (transfers, stock-count variances, reviewed adjustments), driven by
// the admin-configurable thresholds (approvals.Levels). The required levels are snapshotted on the document when
// it enters review; each approval is appended to the document's "approvals" JSON in level order.

import (
	"context"
	"encoding/json"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
)

// Approval is one recorded approval step.
type Approval struct {
	Level  int       `json:"level"`
	Label  string    `json:"label"`
	UserID string    `json:"userId"`
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
}

// RequiredLevels returns the thresholds' levels for amount; when none is configured a single level requiring
// basePerm applies (the SRS demands an approval for every transfer / count adjustment).
func RequiredLevels(ctx context.Context, q db.DBTX, domain string, amount int64, basePerm auth.Permission, label string) ([]approvals.Level, error) {
	lv, err := approvals.LevelsWith(ctx, q, domain, amount)
	if err != nil {
		return nil, err
	}
	if len(lv) == 0 {
		lv = []approvals.Level{{Level: 1, Permission: string(basePerm), Label: label}}
	}
	return lv, nil
}

// NextLevel is the first required level not yet approved (nil = fully approved).
func NextLevel(required []approvals.Level, done []Approval) *approvals.Level {
	have := map[int]bool{}
	for _, a := range done {
		have[a.Level] = true
	}
	for i := range required {
		if !have[required[i].Level] {
			l := required[i]
			return &l
		}
	}
	return nil
}

// ApproveFrom records one person's approval decision starting at level next: it covers next and every following
// pending level the same role may also approve (consecutive levels held by one role, e.g. two value bands that both
// name the Production Manager). One decision per person: with BTN's org chart (one holder per role) a chain whose
// levels all belong to one role is decided in one step instead of deadlocking on "a different person per level",
// while a level that needs another role still waits for that role. The requester never approves (callers check).
func ApproveFrom(required []approvals.Level, done []Approval, next approvals.Level, role auth.Role, basePerm auth.Permission, ap Approval) []Approval {
	ap.Level, ap.Label = next.Level, next.Label
	done = append(done, ap)
	for {
		n := NextLevel(required, done)
		if n == nil || !CanApproveLevel(role, *n, basePerm) {
			return done
		}
		ap.Level, ap.Label = n.Level, n.Label
		done = append(done, ap)
	}
}

// CanApproveLevel: the user's role satisfies the level and holds basePerm (the module's approve permission).
func CanApproveLevel(role auth.Role, l approvals.Level, basePerm auth.Permission) bool {
	return auth.Can(role, basePerm) && l.Satisfies(role)
}

// DecodeLevels / DecodeApprovals read the JSON columns.
func DecodeLevels(raw []byte) []approvals.Level {
	var out []approvals.Level
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []approvals.Level{}
	}
	return out
}

func DecodeApprovals(raw []byte) []Approval {
	var out []Approval
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []Approval{}
	}
	return out
}

// MustJSON marshals v (never fails for the plain types used here).
func MustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
