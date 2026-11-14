package app

import (
	"fmt"
	"io"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/music"
)

// buildMusic builds the music API (E11, #510) over the loopback shim of localai-music.service. The
// engine state comes from the shim's /health until the acestep engine adapter of the router (#508)
// provides a music.Engine of its own; the port is ports.music's default (8082) until node.yaml carries
// ports.music. A service that cannot be built leaves the /music routes out (404) and logs why; nothing
// else depends on it.
func (d *Daemon) buildMusic() {
	c := d.c
	shim := &music.HTTPShim{BaseURL: func() string { return fmt.Sprintf("http://127.0.0.1:%d", music.DefaultPort) }}
	svc, err := music.New(music.Deps{
		Shim:     shim,
		SongsDir: c.Paths.Join(music.DefaultSongsDir),
		WorkDir:  c.Paths.Join(music.DefaultWorkDir),
		Installed: func() bool {
			_, err := os.Stat(c.Paths.Join(music.RuntimeDir))
			return err == nil
		},
		MinFreeBytes: func() int64 {
			if p := d.profile(); p != nil {
				return p.Storage.MinFreeBytes
			}
			return 0
		},
		Publisher: c.Bus,
		Audit:     d.audit,
		Now:       d.env.Clock.Now,
		Logger:    d.log,
	})
	if err != nil {
		d.log.Error("the music API is not available", "error", err.Error())
		return
	}
	d.music = svc
	d.closers = append(d.closers, closeFunc(func() error { svc.Close(); return nil }))
}

// closeFunc adapts a function to io.Closer.
type closeFunc func() error

func (f closeFunc) Close() error { return f() }

var _ io.Closer = closeFunc(nil)
