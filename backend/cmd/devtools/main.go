// Command devtools: DEVELOPMENT ONLY helpers (refuses to run when APP_ENV=production).
//
//	go run ./cmd/devtools users          one test account per role (password DevPass1234!)
//	go run ./cmd/devtools session <email> print a session cookie token (skips password + OTP)
package main

import (
	"context"
	"fmt"
	"os"

	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/seed"
)

const devPassword = "DevPass1234!"

type devUser struct {
	email, name string
	role        auth.Role
	plant       string
}

var users = []devUser{
	{"super@btn.test", "Test Super Administrator", auth.RoleSuperAdmin, ""},
	{"sysadmin@btn.test", "Test System Administrator", auth.RoleSystemAdmin, ""},
	{"md@btn.test", "Test Managing Director", auth.RoleManagingDirector, ""},
	{"gm@btn.test", "Test General Manager", auth.RoleGeneralManager, ""},
	{"advisor@btn.test", "Test Operations Advisor", auth.RoleOperationsAdvisor, ""},
	{"pm@btn.test", "Test Production Manager", auth.RoleProductionManager, ""},
	{"sales@btn.test", "Test Sales Manager", auth.RoleSalesManager, ""},
	{"finance@btn.test", "Test Finance Manager", auth.RoleFinanceManager, ""},
	{"procurement@btn.test", "Test Procurement Officer", auth.RoleProcurementOfficer, ""},
	{"stock@btn.test", "Test Stock Officer (all locations)", auth.RoleStockOfficer, ""},
	{"stock2@btn.test", "Test Stock Officer BTN-2", auth.RoleStockOfficer, "BTN-2"},
	{"sup1@btn.test", "Test Supervisor BTN-1", auth.RoleSupervisor, "BTN-1"},
	{"sup2@btn.test", "Test Supervisor BTN-2", auth.RoleSupervisor, "BTN-2"},
	{"sup3@btn.test", "Test Supervisor BTN-3", auth.RoleSupervisor, "BTN-3"},
	{"lead1@btn.test", "Test Lead Operator BTN-1", auth.RoleLeadOperator, "BTN-1"},
	{"op1@btn.test", "Test Operator BTN-1", auth.RoleOperator, "BTN-1"},
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.IsProd() {
		fmt.Fprintln(os.Stderr, "devtools must never run in production")
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: devtools users | session <email>")
		os.Exit(2)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()

	switch os.Args[1] {
	case "users":
		if err := seed.Catalog(ctx, pool); err != nil { // plants must exist
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		hash := auth.HashPassword(devPassword)
		emails := ""
		for _, u := range users {
			var plantID any
			if u.plant != "" {
				var id string
				if err := pool.QueryRow(ctx, `SELECT id FROM "Plant" WHERE code=$1`, u.plant).Scan(&id); err != nil {
					fmt.Fprintln(os.Stderr, "plant", u.plant, err)
					os.Exit(1)
				}
				plantID = id
			}
			_, err := pool.Exec(ctx, `INSERT INTO "User" (id, email, name, role, "plantId", "passwordHash", "mustChangePassword")
				VALUES ($1,$2,$3,$4::"Role",$5,$6,false)
				ON CONFLICT (email) DO UPDATE SET role=EXCLUDED.role, "plantId"=EXCLUDED."plantId", "isActive"=true, "updatedAt"=now()`,
				db.NewID(), u.email, u.name, string(u.role), plantID, hash)
			if err != nil {
				fmt.Fprintln(os.Stderr, u.email, err)
				os.Exit(1)
			}
			emails += " " + u.email
		}
		fmt.Printf("Dev users ready (password %s):%s\n", devPassword, emails)
	case "session":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: devtools session <email>")
			os.Exit(2)
		}
		var id string
		if err := pool.QueryRow(ctx, `SELECT id FROM "User" WHERE email=$1`, os.Args[2]).Scan(&id); err != nil {
			fmt.Fprintln(os.Stderr, "user not found:", os.Args[2])
			os.Exit(1)
		}
		svc := &auth.Service{Pool: pool}
		tok, err := svc.CreateSessionToken(ctx, id, "dev-script", "devtools")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(tok)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
