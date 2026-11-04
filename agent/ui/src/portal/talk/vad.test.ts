import { describe, expect, it } from "vitest";
import {
  EndpointDetector,
  formatClock,
  levelDb,
  meterFraction,
} from "./vad";

describe("levelDb", () => {
  it("is -Infinity for silence and 0 for a full-scale square wave", () => {
    expect(levelDb(new Float32Array(100))).toBe(-Infinity);
    expect(levelDb(new Float32Array(100).fill(1))).toBeCloseTo(0, 6);
    expect(levelDb(new Float32Array(100).fill(0.1))).toBeCloseTo(-20, 5);
    expect(levelDb(new Float32Array(0))).toBe(-Infinity);
  });
  it("maps to the meter", () => {
    expect(meterFraction(-Infinity)).toBe(0);
    expect(meterFraction(-60)).toBe(0);
    expect(meterFraction(-30)).toBe(0.5);
    expect(meterFraction(0)).toBe(1);
    expect(meterFraction(10)).toBe(1);
  });
});

describe("formatClock", () => {
  it("shows m:ss", () => {
    expect(formatClock(0)).toBe("0:00");
    expect(formatClock(4200)).toBe("0:04");
    expect(formatClock(60000)).toBe("1:00");
  });
});

describe("EndpointDetector", () => {
  it("does not stop on silence before any speech", () => {
    const d = new EndpointDetector(0);
    for (let t = 0; t < 10000; t += 100) expect(d.push(-80, t)).toBeNull();
  });

  it("stops 1.5 s after speech ends", () => {
    const d = new EndpointDetector(0);
    expect(d.push(-20, 100)).toBeNull();
    expect(d.push(-60, 200)).toBeNull();
    expect(d.push(-60, 1699)).toBeNull();
    expect(d.push(-60, 1700)).toBe("silence");
  });

  it("restarts the silence clock when speech resumes", () => {
    const d = new EndpointDetector(0);
    d.push(-20, 100);
    d.push(-60, 200);
    expect(d.push(-20, 1500)).toBeNull();
    expect(d.push(-60, 1600)).toBeNull();
    expect(d.push(-60, 3099)).toBeNull();
    expect(d.push(-60, 3100)).toBe("silence");
  });

  it("stops at 60 s even while talking", () => {
    const d = new EndpointDetector(1000);
    expect(d.push(-10, 60999)).toBeNull();
    expect(d.push(-10, 61000)).toBe("limit");
  });
});
