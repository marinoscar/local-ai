# Drill test media

Files the drill embeds with `go:embed` so that it needs no network (drill.md section 3.2).

| File | Used by | What it is |
|---|---|---|
| `seven.png` | check 6 `drl.vlm.photo.<id>` | 512 x 512 PNG: white background, a large black digit **7**, a red circle in the top-left corner |

## Licence

`seven.png` is self-made and released under **CC0 1.0** (public domain dedication). It is drawn
by `gen_seven.go` (standard library only: polygons and a circle), so no third-party artwork, font or
photograph is in it. Regenerate it from this directory with:

    go run gen_seven.go

`media_test.go` checks the size, the white background, the black digit and the red circle, so a change
to the generator that breaks the description fails `go test`.
