import common from "./common";
import enums from "./enums";
import layout from "./layout";
import auth from "./auth";
import superadmin from "./superadmin";
import security from "./security";
import dashboard from "./dashboard";
import reports from "./reports";
import production from "./production";
import stock from "./stock";
import materials from "./materials";
import sales from "./sales";
import pettycash from "./pettycash";
import payroll from "./payroll";
import attendance from "./attendance";
import admin from "./admin";
import guide from "./guide";

/** name → file; every key in a file must start with "<name>." (enforced by catalog.test.ts) */
export const NAMESPACES = { common, enums, layout, auth, superadmin, security, dashboard, reports, production, stock, materials, sales, pettycash, payroll, attendance, admin, guide } as const;
export type Namespaces = typeof NAMESPACES;
