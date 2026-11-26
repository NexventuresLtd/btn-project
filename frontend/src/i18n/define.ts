/** A namespace file ships the same keys in every language. `am`, `rw` and `fr` must have exactly the keys of `en`
 * (checked by the compiler here and again by catalog.test.ts). Plural keys end in _one / _other. */
export function defineNs<const E extends Record<string, string>>(ns: { en: E; am: Record<keyof E, string>; rw: Record<keyof E, string>; fr: Record<keyof E, string> }) {
  return ns;
}
