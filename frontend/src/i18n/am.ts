import { NAMESPACES as N } from "./ns";
import type { MessageKey } from "./en";
export const am = Object.assign({}, ...Object.values(N).map((ns) => ns.am)) as Record<MessageKey, string>;
