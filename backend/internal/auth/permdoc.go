package auth

import (
	"sync"
	"unsafe"

	"github.com/gofiber/fiber/v2"
)

// Route-permission documentation (F2, OpenAPI): every handler built by RequirePermission is remembered
// with its permission list, keyed by the identity of the closure, so the OpenAPI generator can read the
// permissions straight from Fiber's route table (no hand-maintained list that could drift).

var permDocs sync.Map // closure pointer -> permDoc

// permDoc keeps the handler itself alive, so its address can never be reused by another closure.
type permDoc struct {
	h     fiber.Handler
	perms []string
}

func closureID(h fiber.Handler) uintptr {
	return uintptr(*(*unsafe.Pointer)(unsafe.Pointer(&h)))
}

func documentPermissions(perms []Permission, h fiber.Handler) fiber.Handler {
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = string(p)
	}
	permDocs.Store(closureID(h), permDoc{h: h, perms: names})
	return h
}

// HandlerPermissions returns the permissions (any-of) checked by h when h was built by RequirePermission.
func HandlerPermissions(h fiber.Handler) ([]string, bool) {
	v, ok := permDocs.Load(closureID(h))
	if !ok {
		return nil, false
	}
	return v.(permDoc).perms, true
}
