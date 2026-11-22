import { createContext, useContext } from "react";

export interface LayoutCtx {
  openMobileNav: () => void;
  closeMobileNav: () => void;
  isMobile: () => boolean;
}
export const LayoutContext = createContext<LayoutCtx>({ openMobileNav: () => {}, closeMobileNav: () => {}, isMobile: () => false });
export const useLayout = () => useContext(LayoutContext);
