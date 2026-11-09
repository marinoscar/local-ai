# Drill test media

Two short recordings of speech, embedded in the agent (`go:embed`, see `../media.go`) and played by the
speech checks of the monthly drill (`drl.stt.en`, `drl.stt.es`, `drl.speech.roundtrip`; drill.md
sections 3.2 and 4.2). The expected text sits next to each recording and is what the normalised word
error rate (`../wer.go`) compares the transcript with, so a result is comparable from month to month.

| File | Format | Length | Says |
|---|---|---|---|
| `en.wav` | 16 kHz, mono, 16-bit PCM | about 3.9 s | The quick brown fox jumps over the lazy dog. (`en.txt`) |
| `es.wav` | 16 kHz, mono, 16-bit PCM | about 4.7 s | El veloz murciélago hindú comía feliz cardillo y kiwi. (`es.txt`) |

## Where they come from

Self-made, with no recording of a person. Each sentence is a pangram (a sentence with no author) that
was synthesised offline with [Piper](https://github.com/rhasspy/piper) (MIT licence), resampled to
16 kHz mono and padded with a short silence at both ends:

- English: the `en_US-ljspeech-medium` voice (trained on the LJ Speech recordings, which are in the
  public domain).
- Spanish: the `es_MX-ald-medium` voice (CC0 1.0, the same voice the catalog installs as `piper-es-medium`).

The recordings therefore carry no personal data and no third-party licence beyond the two above, which
permit this use. If they are ever replaced, keep the length, the format and the sentences: the checks
and their thresholds (drill.md section 4.2) depend on them.
