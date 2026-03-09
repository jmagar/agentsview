import {
  describe,
  it,
  expect,
  vi,
  beforeEach,
  afterEach,
} from "vitest";
import {
  parseHash,
  RouterStore,
} from "./router.svelte.js";

describe("parseHash", () => {
  let originalHash: string;

  beforeEach(() => {
    originalHash = window.location.hash;
  });

  afterEach(() => {
    window.location.hash = originalHash;
  });

  it("returns default route for empty hash", () => {
    window.location.hash = "";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({});
  });

  it("returns default route for bare slash", () => {
    window.location.hash = "#/";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({});
  });

  it("parses #/sessions with query params", () => {
    window.location.hash = "#/sessions?x=1&y=hello";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({ x: "1", y: "hello" });
  });

  it("parses #/sessions without query params", () => {
    window.location.hash = "#/sessions";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({});
  });

  it("falls back to default route for unknown path", () => {
    window.location.hash = "#/unknown";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({});
  });

  it("falls back to default route for unknown path with params", () => {
    window.location.hash = "#/foo?bar=baz";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({ bar: "baz" });
  });

  it("handles path without leading slash", () => {
    window.location.hash = "#sessions?a=1";
    const result = parseHash();
    expect(result.route).toBe("sessions");
    expect(result.params).toEqual({ a: "1" });
  });
});

describe("RouterStore", () => {
  let store: RouterStore;

  afterEach(() => {
    store?.destroy();
    window.location.hash = "";
  });

  it("initializes with parsed hash", () => {
    window.location.hash = "#/sessions?project=test";
    store = new RouterStore();
    expect(store.route).toBe("sessions");
    expect(store.params).toEqual({ project: "test" });
  });

  it("falls back to default on invalid route", () => {
    window.location.hash = "#/bogus";
    store = new RouterStore();
    expect(store.route).toBe("sessions");
  });

  it("destroy removes the hashchange listener", () => {
    window.location.hash = "";
    const addSpy = vi.spyOn(window, "addEventListener");
    store = new RouterStore();

    const registeredCb = addSpy.mock.calls.find(
      ([event]) => event === "hashchange",
    )?.[1];
    addSpy.mockRestore();

    const removeSpy = vi.spyOn(window, "removeEventListener");
    store.destroy();
    expect(removeSpy).toHaveBeenCalledWith(
      "hashchange",
      registeredCb,
    );
    removeSpy.mockRestore();
  });

  it("navigate returns true on hash change", () => {
    window.location.hash = "";
    store = new RouterStore();
    const result = store.navigate("sessions", {
      project: "foo",
    });
    expect(result).toBe(true);
    expect(store.route).toBe("sessions");
    expect(store.params).toEqual({ project: "foo" });
  });

  it("navigate returns false on same hash (no-op)", () => {
    window.location.hash = "#/sessions";
    store = new RouterStore();
    const result = store.navigate("sessions");
    expect(result).toBe(false);
  });

  it("navigate returns false when params match", () => {
    window.location.hash =
      "#/sessions?include_one_shot=true";
    store = new RouterStore();
    const result = store.navigate("sessions", {
      include_one_shot: "true",
    });
    expect(result).toBe(false);
  });

  it("does not accumulate listeners across instances", () => {
    const addSpy = vi.spyOn(window, "addEventListener");
    const removeSpy = vi.spyOn(
      window,
      "removeEventListener",
    );

    const store1 = new RouterStore();
    const store2 = new RouterStore();

    const addCalls = addSpy.mock.calls.filter(
      ([event]) => event === "hashchange",
    );
    expect(addCalls).toHaveLength(2);

    store1.destroy();

    const removeCalls = removeSpy.mock.calls.filter(
      ([event]) => event === "hashchange",
    );
    expect(removeCalls).toHaveLength(1);
    expect(removeCalls[0]![1]).toBe(addCalls[0]![1]);

    // Destroyed store should not react to hashchange
    window.location.hash = "#/sessions?after=destroy1";
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    expect(store1.params).not.toHaveProperty("after");
    expect(store2.params).toEqual({ after: "destroy1" });

    store2.destroy();
    addSpy.mockRestore();
    removeSpy.mockRestore();
  });
});
