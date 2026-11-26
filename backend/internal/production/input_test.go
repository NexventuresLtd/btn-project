package production

import (
	"testing"

	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
)

func TestParseReportBatchesAndDefects(t *testing.T) {
	v := httpx.NewV(map[string]any{"plantId": "p", "date": "2026-01-02", "shift": "NIGHT", "lines": []any{
		map[string]any{"productId": "a", "producedQty": 10.0, "defects": []any{map[string]any{"reasonCode": "cracked", "qty": 2.0}}},
		map[string]any{"productId": "a", "producedQty": 5.0, "batchRef": "B2", "additionalBatch": true},
	}, "exceptionFlags": []any{map[string]any{"code": "weather"}}}, i18n.EN)
	in, err := parseReport(v, i18n.EN)
	if err != nil {
		t.Fatal(err)
	}
	if in.Lines[0].BatchRef != "NIGHT-1" || in.Lines[0].Defects[0].ReasonCode != "CRACKED" || !in.Lines[0].DefectsSet || !in.Lines[1].Additional || !in.FlagsSet || in.Flags[0].Code != "WEATHER" {
		t.Fatalf("%+v", in)
	}
	// derive after stages are known
	in.Lines[0].Defects[0].Stage = StageHandling
	in.deriveDamage(func(f, m string) { t.Fatal(f, m) }, i18n.EN)
	if in.Lines[0].DamageHandQty != 2 || in.Lines[0].DamageProdQty != 0 {
		t.Fatalf("%+v", in.Lines[0])
	}
	// flags absent → keep stored flags
	v = httpx.NewV(map[string]any{"plantId": "p", "date": "2026-01-02", "shift": "DAY", "lines": []any{map[string]any{"productId": "a", "producedQty": 1.0}}}, i18n.EN)
	if in, err = parseReport(v, i18n.EN); err != nil || in.FlagsSet {
		t.Fatalf("%v %+v", err, in)
	}
}
