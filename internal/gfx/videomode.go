package gfx

import rl "github.com/gen2brain/raylib-go/raylib"

// VideoMode represents a supported display resolution.
type VideoMode = rl.VideoMode

// GetVideoModes returns all display modes supported by the primary monitor.
func GetVideoModes() []VideoMode {
	return rl.GetVideoModes()
}
