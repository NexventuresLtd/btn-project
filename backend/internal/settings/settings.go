// Package settings reads and writes the "Setting" key/value rows (company, approvals, branding),
// the branding asset store and the i18n override store — all cached with explicit invalidation.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Company is shown on PDFs and documents.
type Company struct {
	Name      string `json:"name"`
	LegalName string `json:"legalName"`
	Address   string `json:"address"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	TIN       string `json:"tin"`
	Website   string `json:"website"`
}

// Approvals are the workflow thresholds.
type Approvals struct {
	// PettyCashApprovalLimit: expenses at/above this (RWF) need GM/MD approval; below it a Finance entry is auto-approved.
	PettyCashApprovalLimit int64 `json:"pettyCashApprovalLimit"`
	// MaxDiscountPercent: lines priced below default by more than this percent need GM confirmation.
	MaxDiscountPercent float64 `json:"maxDiscountPercent"`
}

var DefaultCompany = Company{
	Name: "BTN Manufacturing", LegalName: "BTN Manufacturing Ltd", Address: "Kigali, Rwanda",
	Phone: "+250 796 484 870", Email: "btn.mfg@gmail.com", TIN: "", Website: "www.btnlimited.com",
}

var DefaultApprovals = Approvals{PettyCashApprovalLimit: 0, MaxDiscountPercent: 10}

// Service is the settings facade. Create once (app wiring) and share.
type Service struct {
	pool   *pgxpool.Pool
	kv     *cache.Cache[map[string]json.RawMessage]
	assets *cache.Cache[*Asset]
	ovr    *cache.Cache[*Overrides]
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:   pool,
		kv:     cache.New[map[string]json.RawMessage](5*time.Minute, 4),
		assets: cache.New[*Asset](time.Hour, 64),
		ovr:    cache.New[*Overrides](5*time.Minute, 2),
	}
}

type setRow struct {
	Key   string
	Value json.RawMessage
	At    time.Time
}

func (s *Service) all(ctx context.Context) (map[string]json.RawMessage, error) {
	return s.kv.GetOrLoad(ctx, "all", func(ctx context.Context) (map[string]json.RawMessage, error) {
		rows, err := s.pool.Query(ctx, `SELECT key, value, "updatedAt" FROM "Setting"`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]json.RawMessage{}
		for rows.Next() {
			var r setRow
			if err := rows.Scan(&r.Key, &r.Value, &r.At); err != nil {
				return nil, err
			}
			out[r.Key] = r.Value
			out["__at:"+r.Key] = json.RawMessage(strconv.FormatInt(r.At.UnixMilli(), 10))
		}
		return out, rows.Err()
	})
}

func (s *Service) get(ctx context.Context, key string, dst any) error {
	m, err := s.all(ctx)
	if err != nil {
		return err
	}
	if raw, ok := m[key]; ok {
		return json.Unmarshal(raw, dst) // merges over the defaults already in dst
	}
	return nil
}

// Company returns the company block (defaults merged with stored values).
func (s *Service) Company(ctx context.Context) (Company, error) {
	c := DefaultCompany
	return c, s.get(ctx, "company", &c)
}

// Approvals returns the approval thresholds.
func (s *Service) Approvals(ctx context.Context) (Approvals, error) {
	a := DefaultApprovals
	return a, s.get(ctx, "approvals", &a)
}

// Put upserts a setting inside q (a tx) and invalidates the cache. Call Invalidate after commit too if
// the surrounding transaction may still roll back (the cache is also dropped here, harmlessly).
func (s *Service) Put(ctx context.Context, q db.DBTX, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO "Setting" (key, value, "updatedAt") VALUES ($1, $2::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, "updatedAt" = now()`, key, b)
	s.Invalidate()
	return err
}

// Invalidate drops cached settings; call after any write to "Setting".
func (s *Service) Invalidate() { s.kv.Clear() }

// updatedAtMs is the last-update stamp of a key (0 when absent).
func (s *Service) updatedAtMs(ctx context.Context, key string) int64 {
	m, err := s.all(ctx)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(string(m["__at:"+key]), 10, 64)
	return n
}

// ── Branding ─────────────────────────────────────────────────────────────────

// Localized holds one string per supported language.
type Localized struct {
	EN string `json:"en"`
	AM string `json:"am"`
	RW string `json:"rw"`
}

// Get returns the text for lang, falling back to English.
func (l Localized) Get(lang i18n.Lang) string {
	switch lang {
	case i18n.AM:
		if l.AM != "" {
			return l.AM
		}
	case i18n.RW:
		if l.RW != "" {
			return l.RW
		}
	}
	return l.EN
}

// Branding is the stored configuration (asset ids, not URLs).
type Branding struct {
	Name            string    `json:"name"`
	LegalName       string    `json:"legalName"`
	PrimaryColor    string    `json:"primaryColor"`
	AccentColor     string    `json:"accentColor"`
	FontFamily      string    `json:"fontFamily"`
	BaseFontSize    int       `json:"baseFontSize"`
	LoginHeadline   Localized `json:"loginHeadline"`
	LoginSubtitle   Localized `json:"loginSubtitle"`
	FooterText      Localized `json:"footerText"`
	DefaultLanguage string    `json:"defaultLanguage"`
	LogoLightID     string    `json:"logoLightId,omitempty"`
	LogoDarkID      string    `json:"logoDarkId,omitempty"`
	FaviconID       string    `json:"faviconId,omitempty"`
}

var DefaultBranding = Branding{
	Name: "BTN Manufacturing", LegalName: "BTN Manufacturing Ltd",
	PrimaryColor: "#0C234B", AccentColor: "#F59E0B", FontFamily: "Inter", BaseFontSize: 16,
	LoginHeadline: Localized{EN: "Operations, in one place.", AM: "ሥራዎች ሁሉ በአንድ ቦታ።", RW: "Imikorere yose ahantu hamwe."},
	LoginSubtitle: Localized{
		EN: "Production, stock, sales, petty cash and payroll for BTN Manufacturing.",
		AM: "የቢቲኤን ማኑፋክቸሪንግ ምርት፣ ክምችት፣ ሽያጭ፣ አነስተኛ ገንዘብ እና ደመወዝ።",
		RW: "Umusaruro, ububiko, ibicuruzwa, amafaranga mato n'imishahara bya BTN Manufacturing."},
	FooterText:      Localized{EN: "BTN Manufacturing Ltd · Kigali, Rwanda", AM: "ቢቲኤን ማኑፋክቸሪንግ ኃ.የተ.የግ.ማ. · ኪጋሊ፣ ሩዋንዳ", RW: "BTN Manufacturing Ltd · Kigali, u Rwanda"},
	DefaultLanguage: "en",
}

// Branding returns the stored branding merged over defaults.
func (s *Service) Branding(ctx context.Context) (Branding, error) {
	b := DefaultBranding
	return b, s.get(ctx, "branding", &b)
}

// SaveBranding stores branding (use inside a tx).
func (s *Service) SaveBranding(ctx context.Context, q db.DBTX, b Branding) error {
	return s.Put(ctx, q, "branding", b)
}

// BrandingVersion changes whenever branding or overrides change (cache-busting token for clients).
func (s *Service) BrandingVersion(ctx context.Context) string {
	return strconv.FormatInt(s.updatedAtMs(ctx, "branding"), 36)
}

// ── Assets ───────────────────────────────────────────────────────────────────

// Asset is an uploaded image.
type Asset struct {
	ID     string
	Kind   string
	Mime   string
	Data   []byte
	SHA256 string
}

// Asset loads an uploaded asset (cached; ids are immutable).
func (s *Service) Asset(ctx context.Context, id string) (*Asset, error) {
	return s.assets.GetOrLoad(ctx, id, func(ctx context.Context) (*Asset, error) {
		var a Asset
		err := s.pool.QueryRow(ctx, `SELECT id, kind, mime, data, sha256 FROM asset WHERE id=$1`, id).Scan(&a.ID, &a.Kind, &a.Mime, &a.Data, &a.SHA256)
		if err != nil {
			return nil, err
		}
		return &a, nil
	})
}

// PutAsset stores a new asset row and returns its id.
func (s *Service) PutAsset(ctx context.Context, q db.DBTX, kind, mime string, data []byte, sha string) (string, error) {
	id := db.NewID()
	_, err := q.Exec(ctx, `INSERT INTO asset (id, kind, mime, data, sha256) VALUES ($1,$2,$3,$4,$5)`, id, kind, mime, data, sha)
	return id, err
}

// DeleteAsset removes an asset and evicts it from the cache.
func (s *Service) DeleteAsset(ctx context.Context, q db.DBTX, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM asset WHERE id=$1`, id)
	s.assets.Delete(id)
	return err
}

// ── i18n overrides ───────────────────────────────────────────────────────────

// Overrides is the snapshot of all super-admin edited strings.
type Overrides struct {
	Version  string
	ByLang   map[i18n.Lang]map[string]string
	Entities []Override
}

// Override is one row.
type Override struct {
	Lang      string    `json:"lang"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedBy *string   `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Overrides returns the cached override snapshot.
func (s *Service) Overrides(ctx context.Context) (*Overrides, error) {
	return s.ovr.GetOrLoad(ctx, "all", func(ctx context.Context) (*Overrides, error) {
		rows, err := s.pool.Query(ctx, `SELECT lang, key, value, updated_by, updated_at FROM i18n_override ORDER BY lang, key`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		o := &Overrides{ByLang: map[i18n.Lang]map[string]string{}, Entities: []Override{}}
		var latest time.Time
		for rows.Next() {
			var r Override
			if err := rows.Scan(&r.Lang, &r.Key, &r.Value, &r.UpdatedBy, &r.UpdatedAt); err != nil {
				return nil, err
			}
			o.Entities = append(o.Entities, r)
			l := i18n.Lang(r.Lang)
			if o.ByLang[l] == nil {
				o.ByLang[l] = map[string]string{}
			}
			o.ByLang[l][r.Key] = r.Value
			if r.UpdatedAt.After(latest) {
				latest = r.UpdatedAt
			}
		}
		o.Version = fmt.Sprintf("%s-%d", strconv.FormatInt(latest.UnixMilli(), 36), len(o.Entities))
		return o, rows.Err()
	})
}

// RefreshOverrides reloads the snapshot and installs it into the i18n package (call at boot and after writes).
func (s *Service) RefreshOverrides(ctx context.Context) error {
	s.ovr.Clear()
	o, err := s.Overrides(ctx)
	if err != nil {
		return err
	}
	i18n.SetOverrides(o.ByLang)
	return nil
}
