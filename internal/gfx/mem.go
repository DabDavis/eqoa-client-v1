package gfx

/*
#include <stdlib.h>
*/
import "C"

import (
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// FreeMaterialMaps releases the maps array rl.LoadMaterialDefault allocates,
// without unloading the shader or the textures the material points at, as
// rl.UnloadMaterial would. raylib allocates it with RL_CALLOC (calloc).
func FreeMaterialMaps(m *rl.Material) {
	if m.Maps != nil {
		C.free(unsafe.Pointer(m.Maps))
		m.Maps = nil
	}
}
