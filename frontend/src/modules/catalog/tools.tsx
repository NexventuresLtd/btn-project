import { useState } from "react";
import { Archive, FileUp, Ruler, Tags } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import type { MoreMenuItem } from "@/components/ui";
import { useT } from "@/i18n";
import { IMPORT_PERMISSIONS } from "@/modules/dataimport/api";
import { RefKindModal, RemovedCatalogModal, type CatalogEntity, type CatalogRefKind } from "./forms";

/** Who may add / edit / remove products and materials, their categories, units and recipes. */
export const CATALOG_MANAGE = "catalog.manage";

/**
 * The secondary actions of the Products / Materials pages, for the ⋯ menu: manage categories, manage units,
 * import from a file, show removed. Returns the menu items and the dialogs they open.
 */
export function useCatalogTools(entity: CatalogEntity) {
  const { t } = useT();
  const { can } = useAuth();
  const manage = can(CATALOG_MANAGE);
  const [ref, setRef] = useState<CatalogRefKind | null>(null);
  const [showRemoved, setShowRemoved] = useState(false);
  const category: CatalogRefKind = entity === "products" ? "product_category" : "material_category";

  const items: MoreMenuItem[] = [
    { label: t("catalog.menu.categories"), icon: <Tags />, onClick: () => setRef(category), hidden: !manage },
    { label: t("catalog.menu.units"), icon: <Ruler />, onClick: () => setRef("unit"), hidden: !manage },
    { label: t("catalog.menu.import"), icon: <FileUp />, to: `/admin/import?kind=${entity}`, hidden: !IMPORT_PERMISSIONS.some(can) },
    { label: t(`catalog.menu.removed.${entity}`), icon: <Archive />, onClick: () => setShowRemoved(true), hidden: !manage },
  ];

  const dialogs = (
    <>
      {ref ? <RefKindModal kind={ref} open onClose={() => setRef(null)} /> : null}
      {showRemoved ? <RemovedCatalogModal entity={entity} open onClose={() => setShowRemoved(false)} /> : null}
    </>
  );
  return { manage, items, dialogs };
}
