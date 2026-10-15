package main

import (
	"testing"

	"github.com/eqoa/client/internal/game"
)

// Before a model loads, an entity's sex comes from the eqoa.live model list,
// and from 0x00B3's byte only when neither knows.
func TestDeriveSexFallbacks(t *testing.T) {
	for _, c := range []struct {
		model uint32
		sex   byte
		want  byte
	}{
		{0x0D537335, 0, 1}, // human female on the state channel: no sex byte
		{0x70D898C6, 0, 0}, // human male
		{0x12345678, 1, 1}, // unknown model, 0x00B3 said female
	} {
		if got := deriveSex(&game.Entity{ModelID: c.model, Sex: c.sex}, nil); got != c.want {
			t.Errorf("model %#x sex byte %d: %d, want %d", c.model, c.sex, got, c.want)
		}
	}
}
