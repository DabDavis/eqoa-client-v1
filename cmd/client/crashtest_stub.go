//go:build !dev

package main

// Release builds have no crash test (crashtest_dev.go).
func (c *Client) crashTestTick() {}
