import { describe, expect, it } from "vitest";
import {
  bookURL,
  htmlSearchURL,
  langFilter,
  parseOpenSearch,
  searchURL,
} from "./opensearch";
import {
  isLibraryBack,
  parseReaderSearch,
  readerPath,
  safeBack,
  safeReaderSrc,
} from "./reader";

const RSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">
<channel><title>Search</title>
<totalResults>12</totalResults><startIndex>0</startIndex><itemsPerPage>25</itemsPerPage>
<item><title>Water &amp; purification</title><link>/library/content/wikipedia_en_all/Water_purification</link>
<description>&lt;b&gt;Boil&lt;/b&gt; water for one minute</description><book><title>Wikipedia</title></book></item>
<item><title>Boiling</title><link>/library/content/survival/Boiling</link><description>x</description><book><title>Survival library</title></book></item>
<item><title>Evil</title><link>https://example.invalid/x</link><description>x</description></item>
</channel></rss>`;

describe("parseOpenSearch", () => {
  it("parses a normal result and drops links outside the allowlist", () => {
    const r = parseOpenSearch(RSS);
    expect(r.kind).toBe("results");
    if (r.kind !== "results") return;
    expect(r.total).toBe(12);
    expect(r.items).toHaveLength(2);
    expect(r.items[0]).toEqual({
      title: "Water & purification",
      url: "/library/content/wikipedia_en_all/Water_purification",
      book: "Wikipedia",
      snippet: "Boil water for one minute",
    });
  });
  it("handles an empty result", () => {
    const r = parseOpenSearch(
      "<rss><channel><totalResults>0</totalResults></channel></rss>",
    );
    expect(r).toEqual({ kind: "results", total: 0, items: [] });
  });
  it("counts items when totalResults is missing", () => {
    const r = parseOpenSearch(
      "<rss><channel><item><title>A</title><link>/library/content/b/A</link></item></channel></rss>",
    );
    expect(r.kind === "results" && r.total).toBe(1);
  });
  it("reports HTML as html", () => {
    expect(
      parseOpenSearch("<!DOCTYPE html><html><body>x</body></html>"),
    ).toEqual({ kind: "html" });
  });
});

describe("langFilter and URLs", () => {
  it("maps the UI language", () => {
    expect(langFilter("en")).toBe("eng");
    expect(langFilter("es")).toBe("spa");
  });
  it("builds the search request", () => {
    expect(searchURL("water purification", "es", false)).toBe(
      "/library/search?pattern=water+purification&books.filter.lang=spa&format=xml&pageLength=25",
    );
  });
  it("omits the language filter for All languages", () => {
    expect(searchURL("x", "en", true)).toBe(
      "/library/search?pattern=x&format=xml&pageLength=25",
    );
    expect(htmlSearchURL("x", "en", true)).toBe(
      "/library/search?pattern=x&pageLength=25",
    );
  });
  it("uses the undated book alias", () => {
    expect(bookURL("mdwiki_en_all")).toBe("/library/content/mdwiki_en_all/");
  });
});

describe("reader src allowlist", () => {
  it.each([
    "/library/content/x/A",
    "/maps/",
    "/docs/runbooks/01.html",
    "/library/content/x/A%20(b)?x=1#h",
  ])("accepts %s", (s) => expect(safeReaderSrc(s)).toBe(s));
  it.each([
    "",
    "/portal",
    "/api/v1/portal/status",
    "//evil.invalid/library/",
    "https://evil.invalid/library/",
    "javascript:alert(1)",
    "/library/../api/v1/x",
    "/library/%2e%2e/api",
    "/library/\\evil",
    "/library/a\nb",
    "library/content",
  ])("refuses %j", (s) => expect(safeReaderSrc(s)).toBeNull());
  it("refuses a null", () => expect(safeReaderSrc(null)).toBeNull());
});

describe("reader route", () => {
  it("round-trips src and back", () => {
    const p = readerPath("/library/content/b/A b", "/portal/library?q=a b");
    const r = parseReaderSearch(p.slice(p.indexOf("?")));
    expect(r).toEqual({
      src: "/library/content/b/A b",
      back: "/portal/library?q=a b",
    });
  });
  it("refuses a bad src and falls back for a bad back", () => {
    expect(parseReaderSearch("?src=https://evil.invalid/")).toBeNull();
    expect(safeBack("https://evil.invalid/")).toBe("/portal");
    expect(safeBack("//evil.invalid")).toBe("/portal");
    expect(safeBack(null)).toBe("/portal");
    expect(safeBack("/portal/library?q=x")).toBe("/portal/library?q=x");
  });
  it("knows a library back route", () => {
    expect(isLibraryBack("/portal/library?q=x")).toBe(true);
    expect(isLibraryBack("/portal")).toBe(false);
  });
});
