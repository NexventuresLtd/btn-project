import { NAMESPACES as N } from "./ns";
import type { MessageKey } from "./en";
export const fr = Object.assign({}, ...Object.values(N).map((ns) => ns.fr)) as Record<MessageKey, string>;
