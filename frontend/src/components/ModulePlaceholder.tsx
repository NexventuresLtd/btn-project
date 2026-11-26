import type { ReactNode } from "react";
import { Construction } from "lucide-react";
import { Card, EmptyState, PageHeader } from "@/components/ui";

/** Placeholder page for phase-2 modules that are registered (route, nav, permission) but not built yet. */
export function ModulePlaceholder({ title, description, text, icon }: { title: string; description: string; text: string; icon?: ReactNode }) {
  return (
    <>
      <PageHeader title={title} description={description} />
      <Card>
        <EmptyState icon={icon ?? <Construction />} title={title} description={text} />
      </Card>
    </>
  );
}
