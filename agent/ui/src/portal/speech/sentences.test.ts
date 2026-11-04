import { describe, expect, it } from "vitest";
import { MAX_SENTENCE, SentenceSplitter, splitSentences } from "./sentences";

describe("splitSentences", () => {
  it("splits English on . ! ?", () => {
    expect(splitSentences("Boil it. Is it clear? Yes! Drink.")).toEqual([
      "Boil it.",
      "Is it clear?",
      "Yes!",
      "Drink.",
    ]);
  });

  it("splits Spanish with inverted marks", () => {
    expect(
      splitSentences("¿Tiene fiebre? ¡Beba agua! Descanse mucho."),
    ).toEqual(["¿Tiene fiebre?", "¡Beba agua!", "Descanse mucho."]);
  });

  it("splits at an ellipsis followed by a space", () => {
    expect(splitSentences("Espere… Ya casi.")).toEqual(["Espere…", "Ya casi."]);
  });

  it("splits at a newline", () => {
    expect(splitSentences("One.\nTwo.")).toEqual(["One.", "Two."]);
  });

  it("keeps closing quotes with the sentence", () => {
    expect(splitSentences('He said "stop." Then left.')).toEqual([
      'He said "stop."',
      "Then left.",
    ]);
  });

  it("does not split after abbreviations", () => {
    expect(splitSentences("Ask Dr. Smith today. Thanks.")).toEqual([
      "Ask Dr. Smith today.",
      "Thanks.",
    ]);
    expect(splitSentences("Pregunte a la Sra. Lopez ahora.")).toEqual([
      "Pregunte a la Sra. Lopez ahora.",
    ]);
    expect(splitSentences("Use agua limpia, p. ej. hervida. Gracias.")).toEqual([
      "Use agua limpia, p. ej. hervida.",
      "Gracias.",
    ]);
    expect(splitSentences("Use e.g. soap and water. Done.")).toEqual([
      "Use e.g. soap and water.",
      "Done.",
    ]);
    expect(splitSentences("Salt, sugar, etc. are fine. Ok.")).toEqual([
      "Salt, sugar, etc. are fine.",
      "Ok.",
    ]);
  });

  it("does not split inside numbers", () => {
    expect(splitSentences("Use 1.5 liters of water. Stir.")).toEqual([
      "Use 1.5 liters of water.",
      "Stir.",
    ]);
  });

  it("cuts a long run without punctuation at the last space before 220 chars", () => {
    const text = "word ".repeat(100).trim(); // 499 chars
    const out = splitSentences(text);
    expect(out.length).toBeGreaterThan(2);
    for (const s of out) expect(s.length).toBeLessThanOrEqual(MAX_SENTENCE);
    expect(out.join(" ")).toBe(text);
  });

  it("cuts a single word longer than 220 chars at 220", () => {
    const out = splitSentences("x".repeat(500));
    expect(out[0]).toHaveLength(MAX_SENTENCE);
    expect(out.join("")).toBe("x".repeat(500));
  });
});

describe("SentenceSplitter streaming", () => {
  it("waits for the character after a period", () => {
    const sp = new SentenceSplitter();
    expect(sp.push("Boil it.")).toEqual([]);
    expect(sp.push(" Then")).toEqual(["Boil it."]);
    expect(sp.push(" cool it.")).toEqual([]);
    expect(sp.flush()).toEqual(["Then cool it."]);
  });

  it("gives the same result however the text is chunked", () => {
    const text = "Dr. Smith says 1.5 liters. ¿Entiende? Sí…  Listo.";
    const whole = splitSentences(text);
    const sp = new SentenceSplitter();
    const out: string[] = [];
    for (const ch of text) out.push(...sp.push(ch));
    out.push(...sp.flush());
    expect(out).toEqual(whole);
  });

  it("flush on an empty buffer gives nothing", () => {
    expect(new SentenceSplitter().flush()).toEqual([]);
  });
});
