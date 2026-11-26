import { NAMESPACES as N, type Namespaces } from "./ns";

type UnionToIntersection<U> = (U extends unknown ? (x: U) => void : never) extends (x: infer I) => void ? I : never;
/** Every namespace's English block merged (built generically: registering a namespace in ns/index.ts is enough). */
type AllEn = UnionToIntersection<Namespaces[keyof Namespaces]["en"]>;

export const en = Object.assign({}, ...Object.values(N).map((ns) => ns.en)) as AllEn;
export type MessageKey = keyof AllEn & string;
