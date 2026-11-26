package expenses

import (
	"bytes"
	"strings"
	"testing"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
)

func TestSniffReceipt(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{1}, 20)...)
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{1}, 20)...)
	webp := []byte("RIFF\x10\x00\x00\x00WEBPVP8 ....")
	pdf := []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n%%EOF")
	cases := []struct {
		name   string
		b      []byte
		mime   string
		reason string
	}{
		{"jpeg", jpeg, "image/jpeg", ""},
		{"png", png, "image/png", ""},
		{"webp", webp, "image/webp", ""},
		{"pdf", pdf, "application/pdf", ""},
		{"empty", nil, "", "expenses.err.file_empty"},
		{"html renamed", []byte("<html><script>alert(1)</script></html>"), "", "expenses.err.file_type"},
		{"exe", []byte("MZ\x90\x00\x03"), "", "expenses.err.file_type"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), "", "expenses.err.file_type"},
		{"gif", []byte("GIF89a......"), "", "expenses.err.file_type"},
		{"pdf js", []byte("%PDF-1.7\n<< /OpenAction << /S /JavaScript /JS (app.alert(1)) >> >>"), "", "expenses.err.pdf_active"},
		{"pdf launch", []byte("%PDF-1.7\n<< /Launch /F (cmd.exe) >>"), "", "expenses.err.pdf_active"},
		{"pdf embedded", []byte("%PDF-1.7\n<< /EmbeddedFile 3 0 R >>"), "", "expenses.err.pdf_active"},
		{"too large", append([]byte("%PDF-"), make([]byte, MaxAttachmentBytes)...), "", "expenses.err.file_too_large"},
	}
	for _, c := range cases {
		mime, ok, reason := SniffReceipt(c.b)
		if mime != c.mime || ok != (c.reason == "") || reason != c.reason {
			t.Errorf("%s: %q %v %q", c.name, mime, ok, reason)
		}
	}
	// a PDF mentioning "JSON" is not active content
	if _, ok, _ := SniffReceipt([]byte("%PDF-1.4 /JSON data")); !ok {
		t.Error("/JSON must not be treated as /JS")
	}
}

func TestCleanFileName(t *testing.T) {
	cases := map[string][2]string{
		"../../etc/passwd":       {"application/pdf", "passwd.pdf"},
		`C:\Users\x\receipt.PDF`: {"application/pdf", "receipt.PDF"},
		"photo.jpeg":             {"image/jpeg", "photo.jpeg"},
		"photo.png":              {"image/jpeg", "photo.jpg"},
		"":                       {"image/png", "receipt.png"},
		"a\"b;c<d>.pdf":          {"application/pdf", "a_b_c_d_.pdf"},
		"ደረሰኝ.pdf":               {"application/pdf", "ደረሰኝ.pdf"},
	}
	for in, w := range cases {
		if got := cleanFileName(in, w[0]); got != w[1] {
			t.Errorf("%q: %q want %q", in, got, w[1])
		}
	}
	if n := cleanFileName(strings.Repeat("x", 300)+".pdf", "application/pdf"); len([]rune(n)) > 124 {
		t.Errorf("long name kept: %d", len(n))
	}
}

func TestNextApprovalSeparationOfDuties(t *testing.T) {
	fm, gm := "FINANCE_MANAGER", "GENERAL_MANAGER"
	levels := []approvals.Level{{Level: 1, Permission: "expenses.approve", Role: &fm, Label: "Finance"}, {Level: 2, Permission: "expenses.approve", Role: &gm, Label: "GM"}}
	// creator never approves
	if d := NextApproval(levels, nil, "expense", "u1", auth.RoleFinanceManager, "u1"); d.OK || d.Reason != "expenses.err.self_approve" {
		t.Errorf("self: %+v", d)
	}
	// FM approves level 1
	d := NextApproval(levels, nil, "expense", "fm", auth.RoleFinanceManager, "u1")
	if !d.OK || d.Level.Level != 1 {
		t.Fatalf("fm level 1: %+v", d)
	}
	dec := []approvalRow{{Kind: "expense", Level: 1, Decision: "APPROVED", UserID: "fm"}}
	// the same person cannot sign level 2 too
	if d := NextApproval(levels, dec, "expense", "fm", auth.RoleSuperAdmin, "u1"); d.OK || d.Reason != "expenses.err.already_signed" {
		t.Errorf("same signer twice: %+v", d)
	}
	// level 2 needs a GM
	if d := NextApproval(levels, dec, "expense", "fm2", auth.RoleFinanceManager, "u1"); d.OK || d.Reason != "expenses.err.level_role" {
		t.Errorf("fm on gm level: %+v", d)
	}
	if d := NextApproval(levels, dec, "expense", "gm", auth.RoleGeneralManager, "u1"); !d.OK || d.Level.Level != 2 {
		t.Errorf("gm level 2: %+v", d)
	}
	// escalation: a GM may sign the finance level when the FM recorded the expense
	if d := NextApproval(levels, nil, "expense", "gm", auth.RoleGeneralManager, "fm"); !d.OK || d.Level.Level != 1 {
		t.Errorf("gm signs level 1 for fm: %+v", d)
	}
	// a role without the permission never approves
	if d := NextApproval(levels, nil, "expense", "sup", auth.RoleSupervisor, "u1"); d.OK {
		t.Errorf("supervisor: %+v", d)
	}
	// all levels done
	done := append(dec, approvalRow{Kind: "expense", Level: 2, Decision: "APPROVED", UserID: "gm"})
	if d := NextApproval(levels, done, "expense", "md", auth.RoleManagingDirector, "u1"); d.OK || d.Reason != "expenses.err.no_level" {
		t.Errorf("done: %+v", d)
	}
	// overrun decisions are a separate track
	if d := NextApproval(levels, dec, "overrun", "fm", auth.RoleFinanceManager, "u1"); !d.OK {
		t.Errorf("overrun track independent: %+v", d)
	}
}
