import { useEffect, useRef, useState, type FormEvent } from "react";
import { apiFetch, ApiError } from "../api";
import { getLang, t, useLang } from "../i18n/i18n";
import {
  bookURL,
  htmlSearchURL,
  parseOpenSearch,
  searchURL,
  type SearchItem,
} from "../library/opensearch";
import { readerPath } from "../library/reader";
import { navigatePath } from "../router";
import type { PortalStatus } from "../status";

type Phase =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "done"; total: number; items: SearchItem[] }
  | { kind: "failed"; code: string };

function queryOf(): string {
  return new URLSearchParams(window.location.search).get("q") ?? "";
}

function backFor(q: string): string {
  return q ? `/portal/library?q=${encodeURIComponent(q)}` : "/portal/library";
}

function openInReader(src: string, q: string) {
  navigatePath(readerPath(src, backFor(q)));
}

/** Library (button 4): search all books and a list of books; articles open in the reader frame. */
export function Library({ status }: { status: PortalStatus | null }) {
  const lang = useLang();
  const [q, setQ] = useState(queryOf);
  const [all, setAll] = useState(false);
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const seq = useRef(0);

  async function run(query: string, allLanguages: boolean) {
    const mine = ++seq.current;
    if (!query.trim()) {
      setPhase({ kind: "idle" });
      return;
    }
    setPhase({ kind: "loading" });
    try {
      const res = await apiFetch(searchURL(query, getLang(), allLanguages), {
        headers: { Accept: "application/rss+xml, text/xml, text/html" },
      });
      const type = res.headers.get("Content-Type") ?? "";
      const body = type.includes("text/html") ? "" : await res.text();
      if (mine !== seq.current) return;
      const parsed = body ? parseOpenSearch(body) : { kind: "html" as const };
      if (parsed.kind === "html") {
        // Kiwix answered with its own results page: show that page in the reader.
        openInReader(htmlSearchURL(query, getLang(), allLanguages), query);
        return;
      }
      setPhase({ kind: "done", total: parsed.total, items: parsed.items });
    } catch (e) {
      if (mine !== seq.current) return;
      setPhase({ kind: "failed", code: e instanceof ApiError ? e.code : "" });
    }
  }

  // Coming back from the reader restores the query and its results.
  useEffect(() => {
    const initial = queryOf();
    if (initial) void run(initial, false);
    return () => {
      seq.current++;
    };
  }, []);

  function submit(e: FormEvent) {
    e.preventDefault();
    const query = q.trim();
    window.history.replaceState(
      null,
      "",
      query ? backFor(query) : "/portal/library",
    );
    void run(query, all);
  }

  const books = status?.capabilities.library?.books ?? [];
  return (
    <section className="screen library" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("library.title")}
      </h1>
      <form className="lib-search" role="search" onSubmit={submit}>
        <label htmlFor="lib-q" className="lib-label">
          {t("library.search")}
        </label>
        <input
          id="lib-q"
          className="lib-input"
          type="search"
          autoComplete="off"
          lang={lang}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <button type="submit" className="btn btn-primary">
          {t("library.searchButton")}
        </button>
        <label className="lib-all">
          <input
            type="checkbox"
            checked={all}
            onChange={(e) => {
              setAll(e.target.checked);
              if (q.trim()) void run(q.trim(), e.target.checked);
            }}
          />
          {t("library.allLanguages")}
        </label>
      </form>
      <div className="lib-cols">
        <div className="lib-results" aria-live="polite">
          {phase.kind === "loading" ? (
            <p className="lead">{t("common.loading")}</p>
          ) : null}
          {phase.kind === "failed" ? (
            <>
              <p className="lead">{t("library.failed")}</p>
              {phase.code ? (
                <p className="code">{t("common.code", { code: phase.code })}</p>
              ) : null}
            </>
          ) : null}
          {phase.kind === "done" ? (
            <>
              <h2 className="lib-h">
                {t("library.results", { n: phase.total })}
              </h2>
              {phase.items.length === 0 ? (
                <p className="lead">{t("library.noResults")}</p>
              ) : (
                <ul className="lib-list">
                  {phase.items.map((it) => (
                    <li key={it.url}>
                      <a
                        className="lib-link"
                        href={readerPath(it.url, backFor(q.trim()))}
                        onClick={(e) => {
                          e.preventDefault();
                          openInReader(it.url, q.trim());
                        }}
                      >
                        {it.book ? `${it.title} — ${it.book}` : it.title}
                      </a>
                      {it.snippet ? (
                        <span className="lib-snippet">{it.snippet}</span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              )}
            </>
          ) : null}
        </div>
        <div className="lib-books">
          <h2 className="lib-h">{t("library.books")}</h2>
          {books.length === 0 ? (
            <p className="lead">{t("library.none")}</p>
          ) : (
            <ul className="lib-booklist">
              {books.map((b) => (
                <li key={b.content_id}>
                  <button
                    type="button"
                    className="btn"
                    onClick={() => openInReader(bookURL(b.book), q.trim())}
                  >
                    {b.title}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </section>
  );
}
