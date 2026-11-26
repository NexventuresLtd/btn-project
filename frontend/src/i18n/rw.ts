import { NAMESPACES as N } from "./ns";
import type { MessageKey } from "./en";
export const rw = Object.assign({}, ...Object.values(N).map((ns) => ns.rw)) as Record<MessageKey, string>;
