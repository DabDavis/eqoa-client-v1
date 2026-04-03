//go:build !dev

package main

// Stubs for grid runner — stable builds have no GM tools.

type gridRunnerState struct{}

func (c *Client) gridRunnerUpdate() (float32, float32) { return 0, 0 }
func (c *Client) gridRunnerStatus() string             { return "" }
