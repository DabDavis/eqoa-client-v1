//go:build !dev

package main

// Stubs for GM menu methods — stable builds have no GM tools.

func (c *Client) drawGMMenu()       {}
func (c *Client) drawGMDisplayPos() {}
func (c *Client) drawGMStats()      {}
func (c *Client) handleGMMenuInput() {}
func loadGMLocations()               {}
