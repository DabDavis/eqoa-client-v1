// Package gfx provides thin wrappers for GL calls not covered by raylib's rlgl.
// Works with both desktop GL and GLES2 — no dependency on system GL headers at runtime.
package gfx

/*
#ifdef __APPLE__
  #include <OpenGL/gl.h>
#elif defined(__ANDROID__)
  #include <GLES2/gl2.h>
#else
  #include <GL/gl.h>
#endif

#ifndef GL_DEPTH_BUFFER_BIT
#define GL_DEPTH_BUFFER_BIT 0x00000100
#endif
#ifndef GL_LEQUAL
#define GL_LEQUAL 0x0203
#endif
#ifndef GL_LESS
#define GL_LESS 0x0201
#endif
#ifndef GL_POLYGON_OFFSET_FILL
#define GL_POLYGON_OFFSET_FILL 0x8037
#endif
*/
import "C"

// ClearDepth clears only the depth buffer (not color).
func ClearDepth() { C.glClear(C.GL_DEPTH_BUFFER_BIT) }

// DepthFuncLEqual sets depth test to GL_LEQUAL (allows drawing at same depth).
func DepthFuncLEqual() { C.glDepthFunc(C.GL_LEQUAL) }

// DepthFuncLess restores default depth test (GL_LESS).
func DepthFuncLess() { C.glDepthFunc(C.GL_LESS) }

// EnablePolygonOffset enables polygon offset fill with bias (-1, -1).
func EnablePolygonOffset() {
	C.glEnable(C.GL_POLYGON_OFFSET_FILL)
	C.glPolygonOffset(-1.0, -1.0)
}

// DisablePolygonOffset disables polygon offset fill.
func DisablePolygonOffset() { C.glDisable(C.GL_POLYGON_OFFSET_FILL) }
