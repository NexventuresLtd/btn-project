package sales_test

import (
	"testing"

	"btn-ops/internal/testutil"
)

// TestMain drops this test process's database clone afterwards (NFR-M1).
func TestMain(m *testing.M) { testutil.Main(m) }
