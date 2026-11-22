/** Where "has this user already seen the welcome tour?" is remembered. Swap `setGuideStorage` for a server-backed
 * implementation (e.g. PUT /api/auth/preferences) without touching the UI. */
export interface GuideStorage {
  hasSeen(userId: string): boolean;
  markSeen(userId: string): void;
  reset?(userId: string): void;
}

const key = (id: string) => `btn.guide.seen.${id}`;

export const localGuideStorage: GuideStorage = {
  hasSeen(userId) {
    try {
      return localStorage.getItem(key(userId)) === "1";
    } catch {
      return true; // storage blocked: never nag
    }
  },
  markSeen(userId) {
    try {
      localStorage.setItem(key(userId), "1");
    } catch { /* ignore */ }
  },
  reset(userId) {
    try {
      localStorage.removeItem(key(userId));
    } catch { /* ignore */ }
  },
};

let storage: GuideStorage = localGuideStorage;
export const setGuideStorage = (s: GuideStorage) => {
  storage = s;
};
export const getGuideStorage = () => storage;
