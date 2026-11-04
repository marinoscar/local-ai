import { describe, expect, it } from "vitest";
import {
  cardHtmlUrl,
  cardPdfUrl,
  cardTarget,
  manualsDegraded,
  orderItems,
} from "./manuals";

const item = (id: string) => ({ id, title: id, url: `/docs/${id}.html` });

describe("orderItems", () => {
  it("puts survival, rebuild offline and troubleshooting first, others after in their order", () => {
    const sent = [
      item("03-chat"),
      item("07-troubleshooting"),
      item("01-survival-mode"),
      item("02-network"),
      item("06-rebuild-offline"),
    ];
    expect(orderItems(sent).map((i) => i.id)).toEqual([
      "01-survival-mode",
      "06-rebuild-offline",
      "07-troubleshooting",
      "03-chat",
      "02-network",
    ]);
  });
  it("keeps items without a number in the order sent, after the first three", () => {
    expect(
      orderItems([item("extra"), item("01-survival-mode"), item("another")]).map(
        (i) => i.id,
      ),
    ).toEqual(["01-survival-mode", "extra", "another"]);
  });
  it("does not change the input", () => {
    const sent = [item("07-x"), item("01-y")];
    orderItems(sent);
    expect(sent.map((i) => i.id)).toEqual(["07-x", "01-y"]);
  });
});

describe("card URLs", () => {
  it("builds the PDF and HTML addresses from the node id", () => {
    expect(cardPdfUrl("ai-n3")).toBe("/docs/survival-card-ai-n3.pdf");
    expect(cardHtmlUrl("ai-n3")).toBe("/docs/survival-card-ai-n3.html");
  });
  it("refuses a missing or odd node id", () => {
    for (const bad of [undefined, null, "", "../x", "a/b", "a b", "-x", "x?y"]) {
      expect(cardPdfUrl(bad), String(bad)).toBeNull();
      expect(cardHtmlUrl(bad), String(bad)).toBeNull();
    }
  });
});

describe("cardTarget", () => {
  const answer = (ok: boolean) =>
    (async () => ({ ok }) as Response) as unknown as typeof fetch;
  it("opens the PDF when it exists", async () => {
    expect(await cardTarget("ai-n3", answer(true))).toBe(
      "/docs/survival-card-ai-n3.pdf",
    );
  });
  it("falls back to the HTML card when the PDF is a 404", async () => {
    expect(await cardTarget("ai-n3", answer(false))).toBe(
      "/docs/survival-card-ai-n3.html",
    );
  });
  it("falls back to the HTML card when the agent does not answer", async () => {
    const down = (async () => {
      throw new Error("down");
    }) as unknown as typeof fetch;
    expect(await cardTarget("ai-n3", down)).toBe(
      "/docs/survival-card-ai-n3.html",
    );
  });
  it("opens the list of manuals when there is no usable node id", async () => {
    expect(await cardTarget(undefined, answer(true))).toBe("/docs/");
  });
});

describe("manualsDegraded", () => {
  it("is true only for degraded with LAI-SYN-062", () => {
    expect(manualsDegraded({ state: "degraded", code: "LAI-SYN-062" })).toBe(true);
    expect(manualsDegraded({ state: "degraded" })).toBe(false);
    expect(manualsDegraded({ state: "ready", code: "LAI-SYN-062" })).toBe(false);
    expect(manualsDegraded(undefined)).toBe(false);
  });
});
