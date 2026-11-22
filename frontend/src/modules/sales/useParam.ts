import { useSearchParams } from "react-router";

/** URL search-param helper that resets ?page when other params change. */
export function useParam() {
  const [params, setParams] = useSearchParams();
  const set = (key: string, value: string | null | undefined) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };
  return { params, set, page: Math.max(1, Number(params.get("page")) || 1) };
}
