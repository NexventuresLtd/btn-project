/** Open the offline layer's global dialogs from anywhere (they are rendered once by <OfflineProvider>). */
export type OfflineDialog = "unlock" | "pin" | "export" | "import" | null;

let current: OfflineDialog = null;
const listeners = new Set<() => void>();

export const getOfflineDialog = () => current;
export const subscribeOfflineDialog = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};
export function openOfflineDialog(d: OfflineDialog) {
  current = d;
  listeners.forEach((l) => l());
}
export const closeOfflineDialog = () => openOfflineDialog(null);
