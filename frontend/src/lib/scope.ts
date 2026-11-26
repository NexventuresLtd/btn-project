/** Site scoping, mirroring backend auth.UserPlantScoped (docs/ARCHITECTURE.md "Roles & access"). */

/** Roles that must have a site and only ever see that site. */
export const PLANT_SCOPED_ROLES = ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"];
/** Roles that are pinned to a location only when one is assigned (otherwise they work across all locations). */
export const OPTIONALLY_SCOPED_ROLES = ["STOCK_OFFICER"];

/** The plant a signed-in user is pinned to, or null when they may choose any plant. */
export function pinnedPlantId(user: { role: string; plant?: { id: string } | null } | null | undefined): string | null {
  if (!user?.plant) return null;
  return PLANT_SCOPED_ROLES.includes(user.role) || OPTIONALLY_SCOPED_ROLES.includes(user.role) ? user.plant.id : null;
}
