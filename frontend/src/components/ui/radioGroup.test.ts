import { describe, expect, it } from "vitest";
import { nextIndex } from "./radioGroup";

describe("radio group keys", () => {
  it("moves with arrows, wraps, and jumps with Home/End", () => {
    expect(nextIndex("ArrowRight", 0, 3)).toBe(1);
    expect(nextIndex("ArrowDown", 2, 3)).toBe(0);
    expect(nextIndex("ArrowLeft", 0, 3)).toBe(2);
    expect(nextIndex("ArrowUp", 1, 3)).toBe(0);
    expect(nextIndex("Home", 2, 3)).toBe(0);
    expect(nextIndex("End", 0, 3)).toBe(2);
  });
  it("ignores other keys and empty groups", () => {
    expect(nextIndex("Tab", 0, 3)).toBeNull();
    expect(nextIndex(" ", 0, 3)).toBeNull();
    expect(nextIndex("ArrowRight", 0, 0)).toBeNull();
  });
});
