package edge

import (
	"context"
	"errors"
	"strings"
)

type Manual struct {
	PublicURL string
	running   bool
}

func NewManual(publicURL string) *Manual { return &Manual{PublicURL: publicURL} }

func (e *Manual) Start(context.Context, string) (string, error) {
	if strings.TrimSpace(e.PublicURL) == "" {
		return "", errors.New("manual exposure requires public URL")
	}
	e.running = true
	return strings.TrimSpace(e.PublicURL), nil
}
func (e *Manual) Stop(context.Context) error { e.running = false; return nil }
func (e *Manual) Close() error               { e.running = false; return nil }
func (e *Manual) Running() bool              { return e != nil && e.running }
