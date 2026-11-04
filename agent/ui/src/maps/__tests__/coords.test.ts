import { describe, expect, it } from "vitest";
import {
  containingMap,
  formatDecimal,
  inBounds,
  parseCoordinates,
  toDMS,
  type Bounds,
} from "../coords";

describe("toDMS", () => {
  it("formats the spec example", () => {
    expect(toDMS(9.93, -84.08)).toBe(`9°55'48"N 84°4'48"W`);
  });
  it("uses the right hemisphere letters", () => {
    expect(toDMS(-33.5, 151.25)).toBe(`33°30'0"S 151°15'0"E`);
  });
  it("carries a rounded-up 60 seconds", () => {
    expect(toDMS(9.999999, 0)).toBe(`10°0'0"N 0°0'0"E`);
  });
});

describe("formatDecimal", () => {
  it("uses five decimals", () => {
    expect(formatDecimal(9.93241, -84.07912)).toBe("9.93241, -84.07912");
    expect(formatDecimal(9.9, -84)).toBe("9.90000, -84.00000");
  });
});

describe("parseCoordinates", () => {
  it("reads decimal degrees with comma, semicolon or space", () => {
    expect(parseCoordinates("9.93, -84.08")).toEqual({ lat: 9.93, lon: -84.08 });
    expect(parseCoordinates("9.93;-84.08")).toEqual({ lat: 9.93, lon: -84.08 });
    expect(parseCoordinates(" 9.93 -84.08 ")).toEqual({ lat: 9.93, lon: -84.08 });
  });
  it("reads DMS", () => {
    const c = parseCoordinates(`9°55'48"N 84°4'48"W`);
    expect(c?.lat).toBeCloseTo(9.93, 6);
    expect(c?.lon).toBeCloseTo(-84.08, 6);
  });
  it("reads DMS with the longitude first", () => {
    const c = parseCoordinates(`84°4'48"W 9°55'48"N`);
    expect(c?.lat).toBeCloseTo(9.93, 6);
    expect(c?.lon).toBeCloseTo(-84.08, 6);
  });
  it("round-trips through DMS", () => {
    for (const [lat, lon] of [
      [9.93, -84.08],
      [-33.5, 151.25],
      [0, 0],
      [89.9999, 179.5],
    ] as const) {
      const back = parseCoordinates(toDMS(lat, lon));
      expect(back?.lat).toBeCloseTo(lat, 3);
      expect(back?.lon).toBeCloseTo(lon, 3);
    }
  });
  it("rejects nonsense and out-of-range values", () => {
    for (const bad of [
      "",
      "hello",
      "91, 0",
      "0, 181",
      `9°N`,
      `9°55'48"N 9°55'48"N`,
      `9°61'0"N 84°0'0"W`,
      "9.93",
    ])
      expect(parseCoordinates(bad), bad).toBeNull();
  });
});

describe("bounds", () => {
  const cr: Bounds = [-86, 8, -82, 11.5];
  const pa: Bounds = [-83.1, 7, -77, 9.7];
  const maps = [
    { id: "cr", bounds: cr },
    { id: "pa", bounds: pa },
  ];
  it("contains edges and rejects outside", () => {
    expect(inBounds(9.93, -84.08, cr)).toBe(true);
    expect(inBounds(8, -86, cr)).toBe(true);
    expect(inBounds(12, -84, cr)).toBe(false);
    expect(inBounds(9.9, -80, cr)).toBe(false);
  });
  it("finds the other map that holds the view", () => {
    expect(containingMap(maps, 8.98, -79.5, "cr")?.id).toBe("pa");
    expect(containingMap(maps, 9.93, -84.08, "cr")).toBeUndefined();
    expect(containingMap(maps, 40, 10, "cr")).toBeUndefined();
  });
});
