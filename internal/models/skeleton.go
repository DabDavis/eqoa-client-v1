package models

import (
	"math"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// Skeleton holds pre-computed bind-pose matrices for a bone hierarchy.
type Skeleton struct {
	Nodes         []esf.BoneNode
	BindLocal     []mat4   // per-bone local transform (quat+pos+scale)
	BindWorld     []mat4   // per-bone world transform (accumulated parent chain)
	InvBindWorld  []mat4   // inverse bind-world (for skinning)
	BoneRefMap    *esf.BoneRefMap
	BoneLOD       []int32  // per-bone LOD channel (0-3) from ESF hierarchy
	StepBounce    float32  // root bone Y delta from animation (current frame)
	IdleBounce    float32  // root bone Y delta captured on first frame (idle baseline)
	idleCaptured  bool     // true once IdleBounce has been set

	// Pre-allocated work buffers for ComputeAnimPose* methods.
	// Reused every frame to avoid per-call allocation (3-4 × N-bone × 64 bytes).
	// Rendering is single-threaded, so no concurrency concern.
	workLocal  []mat4 // animLocal buffer
	workWorld  []mat4 // animWorld buffer
	workResult []mat4 // result buffer
	workPrev   []mat4 // prevLocal buffer (blend path only)

	// Cached bone index resolution per animation. Eliminates map lookups in
	// applyAnimNodes (called ~40 times per entity per frame).
	boneIndexCache map[*esf.HSpriteAnim][]int32

	// Animation LOD level. Set per-frame by applySkeletal from the model's own
	// LOD distances. This is ProcessPlayList's arg1 on the PS2, and it is
	// VIHSprite::CalcLODLevelIndex's return value: -1 nearest, then 0, 1, 2 for
	// the three distance bands. -1 is full detail, NOT 0.
	ChannelFilter int32

	// LODDistances is the model's own distance table, straight out of the
	// hierarchy. Empty means the model shipped without one, in which case
	// everything animates.
	LODDistances []float32
}

// NewSkeleton computes bind-pose matrices from a hierarchy.
// EQOA hierarchy stores ABSOLUTE world-space positions per bone (not parent-relative).
// Evidence: Human Male head bone stored Y=1.800 matches mesh top (1.777), but
// parent-chain accumulation gives Y=8.400 — proving positions are absolute.
func NewSkeleton(hier *esf.HSpriteHierarchy, refMap *esf.BoneRefMap) *Skeleton {
	if hier == nil || len(hier.Nodes) == 0 {
		return nil
	}
	n := len(hier.Nodes)
	boneLOD := make([]int32, n)
	for i, bone := range hier.Nodes {
		boneLOD[i] = bone.LODChannel
	}
	s := &Skeleton{
		Nodes:          hier.Nodes,
		LODDistances:   hier.LODDistances,
		// -1, not 0: on the PS2 the nearest band is -1, and 0 already drops
		// every channel-0 bone (16% of all bones in CHAR.ESF). A zero value
		// here would silently stop animating them.
		ChannelFilter: -1,
		BindLocal:      make([]mat4, n),
		BindWorld:      make([]mat4, n),
		InvBindWorld:   make([]mat4, n),
		BoneRefMap:     refMap,
		BoneLOD:        boneLOD,
		workLocal:      make([]mat4, n),
		workWorld:      make([]mat4, n),
		workResult:     make([]mat4, n),
		workPrev:       make([]mat4, n),
		boneIndexCache: make(map[*esf.HSpriteAnim][]int32),
	}

	// BindWorld directly from each bone's absolute position/rotation/scale.
	// PS2 ProcessUpHierarchy applies FixScale * uniformScale per-axis in the
	// local bone transform before parent chain multiplication. We bake FixScale
	// into BindWorld so that InvBindWorld is consistent with the animated chain.
	for i, bone := range hier.Nodes {
		s.BindWorld[i] = composeTransform(
			bone.Quat[0], bone.Quat[1], bone.Quat[2], bone.Quat[3],
			bone.Pos.X, bone.Pos.Y, bone.Pos.Z,
			bone.Scale,
		)
	}

	// Derive local transforms: L[i] = inv(W[parent]) * W[i].
	// BindLocal does NOT include FixScale — animation overrides replace it entirely,
	// and applyAllFixScale adds it uniformly in every ComputeAnimPose* method.
	for i, bone := range hier.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			s.BindLocal[i] = s.BindWorld[i]
		} else {
			invParent := invertMat4(s.BindWorld[bone.ParentID])
			s.BindLocal[i] = mulMat4(invParent, s.BindWorld[i])
		}
	}

	// InvBindWorld does NOT include FixScale — matching PS2's invBindPose
	// (bone+0xA0), which is computed from SetBindPose using uniform scale only.
	// The animated chain (animWorld) DOES include FixScale via applyAllFixScale.
	// This means skinMatrix = animWorld(+FS) * invBind(-FS) preserves FixScale
	// in the body mesh skinning, so head mesh and helm scale proportionally.
	for i := range s.BindWorld {
		s.InvBindWorld[i] = invertMat4(s.BindWorld[i])
	}

	return s
}

// ComputeAnimPose computes skinning matrices for a given animation frame.
// Returns per-bone: animWorld[i] * invBindWorld[i].
func (s *Skeleton) ComputeAnimPose(anim *esf.HSpriteAnim, frameIdx int) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]

	// Start with bind-pose local transforms.
	copy(animLocal, s.BindLocal)

	// Override bones that have animation data.
	if anim != nil && frameIdx >= 0 && frameIdx < int(anim.NumFrames) {
		boneIndices := s.resolveBoneIndices(anim)
		for i, node := range anim.Nodes {
			boneIdx := boneIndices[i]
			if boneIdx < 0 || boneIdx >= int32(n) {
				continue
			}
			// Same LOD gate as applyAnimNodes, and for the same reason: a bone
			// with LODChannel < 0 is never animated, and feeding it keyframes
			// mis-positions helms and weapons on every race. This path used to
			// omit it, which made the two entry points disagree.
			if !s.animatesBone(boneIdx) {
				continue
			}
			animLocal[boneIdx] = unpackFrame(node.Frames[frameIdx])
		}
	}

	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	// Recompute world transforms with animation overrides.
	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}

	// Skinning matrix = animWorld * invBindWorld.
	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}

	return result
}

// ComputeAnimPoseLerp computes interpolated skinning matrices between two frames.
func (s *Skeleton) ComputeAnimPoseLerp(anim *esf.HSpriteAnim, t float32) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]

	copy(animLocal, s.BindLocal)

	// Shared with the Lerp2 paths rather than reimplemented: the two used to
	// differ, and only one of them applied the LOD gate.
	applyAnimNodes(s, anim, t, animLocal)

	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}

	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}

	return result
}

// ComputeAnimPoseLerp2 computes interpolated skinning matrices using both upper
// and lower body animations simultaneously. EQOA splits animations into upper body
// (even index, ~28 bones) and lower body (odd index, ~12 bones). Both halves are
// applied to produce a full-skeleton pose. Either anim can be nil.
func (s *Skeleton) ComputeAnimPoseLerp2(upper, lower *esf.HSpriteAnim, t float32) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]

	copy(animLocal, s.BindLocal)

	// Apply upper body animation nodes.
	applyAnimNodes(s, upper, t, animLocal)
	// Apply lower body animation nodes (overrides different bones).
	applyAnimNodes(s, lower, t, animLocal)

	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}

	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}

	return result
}

// ComputeAnimPoseLerp2Split computes skinning matrices using both upper and lower
// body animations with independent playback times. Used for dual-layer animation
// where upper body plays action (attack) while lower body plays locomotion (walk/run).
func (s *Skeleton) ComputeAnimPoseLerp2Split(upper, lower *esf.HSpriteAnim, upperT, lowerT float32) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]

	copy(animLocal, s.BindLocal)

	// Apply upper body animation nodes at upper time.
	applyAnimNodes(s, upper, upperT, animLocal)
	// Apply lower body animation nodes at lower time (overrides different bones).
	applyAnimNodes(s, lower, lowerT, animLocal)

	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}

	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}

	return result
}

// ComputeAnimPoseLerp2Blend computes a blended pose between previous and current
// animations. Used for cross-fade transitions (PS2: AddInterpolatedPlay).
// blend=0 → full previous pose, blend=1 → full current pose.
func (s *Skeleton) ComputeAnimPoseLerp2Blend(
	upper, lower *esf.HSpriteAnim, t float32,
	prevUpper, prevLower *esf.HSpriteAnim, prevT float32,
	blend float32,
) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]
	prevLocal := s.workPrev[:n]

	// Compute previous pose local transforms.
	copy(prevLocal, s.BindLocal)
	applyAnimNodes(s, prevUpper, prevT, prevLocal)
	applyAnimNodes(s, prevLower, prevT, prevLocal)
	s.lockRootPosition(prevLocal)
	s.applyAllFixScale(prevLocal)

	// Compute current pose local transforms (reuse animLocal as currLocal).
	copy(animLocal, s.BindLocal)
	applyAnimNodes(s, upper, t, animLocal)
	applyAnimNodes(s, lower, t, animLocal)
	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	// Blend per-bone local transforms (in-place into animLocal).
	for i := range animLocal {
		animLocal[i] = lerpMat4(prevLocal[i], animLocal[i], blend)
	}

	// Build world transforms from blended locals.
	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}
	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}
	return result
}

// ComputeAnimPoseLerp2BlendSplit blends between a previous single-time pose and
// a current split-time pose (upper body at upperT, lower body at lowerT).
// Used for cross-fade into dual-pair split (e.g. walk → attack+walk).
func (s *Skeleton) ComputeAnimPoseLerp2BlendSplit(
	upper, lower *esf.HSpriteAnim, upperT, lowerT float32,
	prevUpper, prevLower *esf.HSpriteAnim, prevT float32,
	blend float32,
) []mat4 {
	n := len(s.Nodes)
	animLocal := s.workLocal[:n]
	animWorld := s.workWorld[:n]
	result := s.workResult[:n]
	prevLocal := s.workPrev[:n]

	// Previous pose: single time for both upper and lower.
	copy(prevLocal, s.BindLocal)
	applyAnimNodes(s, prevUpper, prevT, prevLocal)
	applyAnimNodes(s, prevLower, prevT, prevLocal)
	s.lockRootPosition(prevLocal)
	s.applyAllFixScale(prevLocal)

	// Current pose: upper at upperT, lower at lowerT.
	copy(animLocal, s.BindLocal)
	applyAnimNodes(s, upper, upperT, animLocal)
	applyAnimNodes(s, lower, lowerT, animLocal)
	s.lockRootPosition(animLocal)
	s.applyAllFixScale(animLocal)

	// Blend per-bone local transforms.
	for i := range animLocal {
		animLocal[i] = lerpMat4(prevLocal[i], animLocal[i], blend)
	}

	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			animWorld[i] = animLocal[i]
		} else {
			animWorld[i] = mulMat4(animWorld[bone.ParentID], animLocal[i])
		}
	}
	for i := range animWorld {
		result[i] = mulMat4(animWorld[i], s.InvBindWorld[i])
	}
	return result
}

// applyFixScale multiplies a bone's 3x3 rotation+scale columns by the per-axis
// FixScale. PS2 ProcessUpHierarchy does this before parent chain multiplication,
// which causes child bone translations to shift correctly per-race.
func applyFixScale(m *mat4, fs [3]float32) {
	if fs[0] == 1 && fs[1] == 1 && fs[2] == 1 {
		return
	}
	m[0] *= fs[0]; m[1] *= fs[1]; m[2] *= fs[2]
	m[4] *= fs[0]; m[5] *= fs[1]; m[6] *= fs[2]
	m[8] *= fs[0]; m[9] *= fs[1]; m[10] *= fs[2]
}

// applyAllFixScale applies FixScale to every bone's local transform.
func (s *Skeleton) applyAllFixScale(animLocal []mat4) {
	for i, bone := range s.Nodes {
		applyFixScale(&animLocal[i], bone.FixScale)
	}
}

// absf returns the absolute value of a float32 without float64 promotion.
func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// lerpMat4 interpolates two local bone transforms for cross-fade blending.
//
// It blends the ROTATION as a rotation. Each source is split into (quaternion,
// per-axis scale, translation) — an exact split, because a bone local is
// R·diag(s) with R orthonormal, so normalising each column recovers R and the
// column magnitudes are the scale. The quaternions are then NLERPed exactly the
// way lerpFrame does one level down, and the scale and translation are lerped
// componentwise.
//
// The previous implementation lerped the sixteen matrix ELEMENTS and repaired
// the result with Gram-Schmidt. That is structurally sound — the output is
// orthogonal, carries the lerped source scales, and never flips handedness —
// but a rotation is not a linear space, so the rotation it landed on was not the
// rotation between the two poses. Measured over 227,040 blended bone transforms
// from 1,892 real CHAR.ESF slot transitions (esfviewer/posetest.html), the error
// grew with how far apart the poses were, which is exactly when a cross-fade is
// doing visible work:
//
//	  0- 15 deg apart: mean 0.02 deg, worst  0.27
//	 45- 90 deg apart: mean 2.05 deg, worst 10.17
//	135-180 deg apart: mean 26.6 deg, worst 48.42
//	worst overall 77.13 deg (slot 26 2H_slash -> 30 shield_bash, bone 11, blend 0.5)
//
// Mirrored frames (negative determinant) are carried in the scale rather than
// silently unmirrored: the sign is pulled out onto the third axis before the
// quaternion is taken and put back on recomposition. No bone on the disc is
// mirrored today, but the old cross-product step would have unmirrored one.
func lerpMat4(a, b mat4, t float32) mat4 {
	qa, sa, ta, oka := decomposeMat4(a)
	qb, sb, tb, okb := decomposeMat4(b)
	if !oka || !okb {
		// A degenerate source has no rotation to blend. Fall back to the raw
		// element lerp, which is what the old code did here too.
		var r mat4
		t1 := 1.0 - t
		for i := range r {
			r[i] = a[i]*t1 + b[i]*t
		}
		return r
	}

	t1 := 1.0 - t

	// Shortest path: q and -q are the same rotation, and interpolating between
	// them without this takes the long way round.
	if qa[0]*qb[0]+qa[1]*qb[1]+qa[2]*qb[2]+qa[3]*qb[3] < 0 {
		qb[0], qb[1], qb[2], qb[3] = -qb[0], -qb[1], -qb[2], -qb[3]
	}
	qx := t1*qa[0] + t*qb[0]
	qy := t1*qa[1] + t*qb[1]
	qz := t1*qa[2] + t*qb[2]
	qw := t1*qa[3] + t*qb[3]
	n := float32(math.Sqrt(float64(qx*qx + qy*qy + qz*qz + qw*qw)))
	if n < 1e-9 {
		// Exactly antipodal after the flip: no shortest path is defined. Keep
		// the source rotation rather than emitting a zero quaternion.
		qx, qy, qz, qw = qa[0], qa[1], qa[2], qa[3]
	} else {
		inv := 1.0 / n
		qx, qy, qz, qw = qx*inv, qy*inv, qz*inv, qw*inv
	}

	s := [3]float32{
		t1*sa[0] + t*sb[0],
		t1*sa[1] + t*sb[1],
		t1*sa[2] + t*sb[2],
	}
	// Uniform scale of 1 here, with the real per-axis scale applied below, so
	// that composeTransform stays the single place a quaternion becomes a matrix.
	m := composeTransform(qx, qy, qz, qw,
		t1*ta[0]+t*tb[0], t1*ta[1]+t*tb[1], t1*ta[2]+t*tb[2], 1.0)
	applyFixScale(&m, s)
	return m
}

// decomposeMat4 splits a bone local into rotation, per-axis scale and
// translation. Exact for the matrices this is used on: every bone local is
// composeTransform's R times a uniform scale, optionally times FixScale's
// per-axis diagonal, so the 3x3 is R·diag(s) with orthogonal columns.
//
// ok is false when an axis has collapsed, which leaves no rotation to recover.
func decomposeMat4(m mat4) (q [4]float32, s [3]float32, t [3]float32, ok bool) {
	s[0] = float32(math.Sqrt(float64(m[0]*m[0] + m[4]*m[4] + m[8]*m[8])))
	s[1] = float32(math.Sqrt(float64(m[1]*m[1] + m[5]*m[5] + m[9]*m[9])))
	s[2] = float32(math.Sqrt(float64(m[2]*m[2] + m[6]*m[6] + m[10]*m[10])))
	t = [3]float32{m[3], m[7], m[11]}
	if s[0] < 1e-9 || s[1] < 1e-9 || s[2] < 1e-9 {
		return q, s, t, false
	}

	i0, i1, i2 := 1.0/s[0], 1.0/s[1], 1.0/s[2]
	// r[row][col], so column c is (m[c], m[4+c], m[8+c]).
	r00, r10, r20 := m[0]*i0, m[4]*i0, m[8]*i0
	r01, r11, r21 := m[1]*i1, m[5]*i1, m[9]*i1
	r02, r12, r22 := m[2]*i2, m[6]*i2, m[10]*i2

	// A mirrored frame is not a rotation; carry the flip in the scale so the
	// quaternion below describes a genuine rotation and recomposition restores
	// the mirror exactly.
	det := r00*(r11*r22-r12*r21) - r01*(r10*r22-r12*r20) + r02*(r10*r21-r11*r20)
	if det < 0 {
		s[2] = -s[2]
		r02, r12, r22 = -r02, -r12, -r22
	}

	if tr := r00 + r11 + r22; tr > 0 {
		f := float32(math.Sqrt(float64(tr+1))) * 2
		q = [4]float32{(r21 - r12) / f, (r02 - r20) / f, (r10 - r01) / f, 0.25 * f}
	} else if r00 > r11 && r00 > r22 {
		f := float32(math.Sqrt(float64(1+r00-r11-r22))) * 2
		q = [4]float32{0.25 * f, (r01 + r10) / f, (r02 + r20) / f, (r21 - r12) / f}
	} else if r11 > r22 {
		f := float32(math.Sqrt(float64(1+r11-r00-r22))) * 2
		q = [4]float32{(r01 + r10) / f, 0.25 * f, (r12 + r21) / f, (r02 - r20) / f}
	} else {
		f := float32(math.Sqrt(float64(1+r22-r00-r11))) * 2
		q = [4]float32{(r02 + r20) / f, (r12 + r21) / f, 0.25 * f, (r10 - r01) / f}
	}
	return q, s, t, true
}

// resolveBoneIndices returns cached per-node bone indices for an animation.
// On first call, resolves all RefIDs via BoneRefMap and caches the result.
func (s *Skeleton) resolveBoneIndices(anim *esf.HSpriteAnim) []int32 {
	if cached, ok := s.boneIndexCache[anim]; ok {
		return cached
	}
	indices := make([]int32, len(anim.Nodes))
	for i, node := range anim.Nodes {
		indices[i] = s.resolveBoneIndex(node.RefID)
	}
	s.boneIndexCache[anim] = indices
	return indices
}

// lockRootPosition captures the root bone Y delta (StepBounce) without locking.
// PS2 ProcessHierarchy does NOT lock root position. The root bone animates freely,
// and VIWalkControl adjusts the entity Y to maintain ground contact.
// We replicate this: StepBounce is applied as a NEGATIVE entity Y offset in
// DrawEntity/drawHelm (subtracting the root drop to keep feet on ground).
// Locking root Y was wrong — it shifts head Z through the tilted spine chain,
// causing a 0.041 forehead exposure gap.
// CaptureIdleBounce sets the idle baseline deterministically, from the idle
// animation's first frame, and should be called once when the model is loaded.
//
// It exists because the fallback in lockRootPosition below latches on the first
// pose ever computed, and a Skeleton lives on the CharModel in the manager's
// `cache map[int32]*CharModel` — one per ModelID, shared by every entity using
// it. So the ground offset for every human male was taken from whatever
// animation the first human male happened to be playing on his first frame.
// Right when that was idle, and wrong for good when it was not: a corpse or a
// runner spawning first would set the baseline for the whole race.
//
// Either animation may be nil; a model with no idle slot keeps the fallback.
func (s *Skeleton) CaptureIdleBounce(upper, lower *esf.HSpriteAnim) {
	if upper == nil && lower == nil {
		return
	}
	n := len(s.Nodes)
	animLocal := make([]mat4, n)
	copy(animLocal, s.BindLocal)
	applyAnimNodes(s, upper, 0, animLocal)
	applyAnimNodes(s, lower, 0, animLocal)
	for i, bone := range s.Nodes {
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			s.IdleBounce = animLocal[i][7] - s.BindLocal[i][7]
			s.idleCaptured = true
			return
		}
	}
}

func (s *Skeleton) lockRootPosition(animLocal []mat4) {
	n := len(s.Nodes)
	for i, bone := range s.Nodes {
		if i >= n {
			break
		}
		if bone.ParentID < 0 || int(bone.ParentID) >= n {
			// Capture root Y delta from bind pose.
			s.StepBounce = animLocal[i][7] - s.BindLocal[i][7]
			// Fallback idle baseline, for models CaptureIdleBounce could not
			// resolve an idle slot for. groundAdj uses IdleBounce (constant) so
			// the idle drop is compensated but walk bob still shows through.
			if !s.idleCaptured {
				s.IdleBounce = s.StepBounce
				s.idleCaptured = true
			}
		}
	}
}

// applyAnimNodes interpolates an animation's nodes into the local transform array.
// PS2 ProcessPlayList: for each bone, checks LOD channel — bones with
// LODChannel <= s.ChannelFilter are skipped (left at bind pose).
func applyAnimNodes(s *Skeleton, anim *esf.HSpriteAnim, t float32, animLocal []mat4) {
	if anim == nil || anim.NumFrames <= 0 {
		return
	}
	n := int32(len(s.Nodes))
	totalFrames := float32(anim.NumFrames)
	pos := t * totalFrames
	f0 := int(pos) % int(anim.NumFrames)
	f1 := (f0 + 1) % int(anim.NumFrames)
	frac := pos - float32(int(pos))

	boneIndices := s.resolveBoneIndices(anim)
	for i, node := range anim.Nodes {
		boneIdx := boneIndices[i]
		if boneIdx < 0 || boneIdx >= n {
			continue
		}
		if !s.animatesBone(boneIdx) {
			continue
		}
		frame0 := node.Frames[f0]
		frame1 := node.Frames[f1]
		animLocal[boneIdx] = lerpFrame(frame0, frame1, frac)
	}
}

// animatesBone reports whether an animation may drive this bone at the current
// LOD level.
//
// This is ProcessPlayList's own comparison, verbatim. The PS2 does, per bone:
//
//	if (lodLevel < bone->LODChannel)   // 0x00449000, bone struct +0xC
//	    ...apply the animation...
//
// ONE comparison covers both jobs. "Never animate" is not a special case: a
// bone with LODChannel -1 fails `-1 < -1` even at the nearest level, which is
// what keeps helms and weapons positioned (62 such bones across CHAR.ESF). And
// a bone with LODChannel 0 fails `0 < 0` at level 0, which is where the old
// rule diverged — it had a `ChannelFilter > 0` guard that animated all 3,541
// channel-0 bones (16% of every bone in the game) one band longer than retail.
//
// It lives here, as one function, because the two callers used to carry their
// own copies of the rule and only one of them had it.
func (s *Skeleton) animatesBone(boneIdx int32) bool {
	if int(boneIdx) >= len(s.BoneLOD) {
		return true
	}
	return s.ChannelFilter < s.BoneLOD[boneIdx]
}

// CalcLODLevelIndex is VIHSprite::CalcLODLevelIndex (0x0044A440), which is the
// only thing that may set ChannelFilter.
//
// The distances are stored PLAIN and squared here at comparison time, exactly
// as the PS2 does — it holds distSq and compares against `d*d` per entry rather
// than pre-squaring the table. Returns -1 when nearer than the first distance,
// otherwise the index of the last distance passed: with three entries the range
// is -1, 0, 1, 2, which is four bands and not three.
func (s *Skeleton) CalcLODLevelIndex(distSq float32) int32 {
	n := len(s.LODDistances)
	if n == 0 {
		return -1
	}
	if distSq < s.LODDistances[0]*s.LODDistances[0] {
		return -1
	}
	level := int32(-1)
	for i := 1; ; i++ {
		level++
		if i >= n || distSq < s.LODDistances[i]*s.LODDistances[i] {
			break
		}
	}
	return level
}

func (s *Skeleton) resolveBoneIndex(refID int32) int32 {
	if s.BoneRefMap != nil {
		if idx := s.BoneRefMap.ResolveBoneIndex(refID); idx >= 0 {
			return idx
		}
	}
	// Fallback: if refID is a valid bone index, use it directly.
	if refID >= 0 && int(refID) < len(s.Nodes) {
		return refID
	}
	return -1
}

// unpackFrame converts packed int16 animation data to a transform matrix.
func unpackFrame(f esf.AnimPackFrame) mat4 {
	const qScale = 1.0 / 32767.0
	const sScale = 1.0 / 512.0
	const pScale = 1.0 / 512.0 // position scale TBD — may need adjustment

	qx := float32(f.Quat[0]) * qScale
	qy := float32(f.Quat[1]) * qScale
	qz := float32(f.Quat[2]) * qScale
	qw := float32(f.Quat[3]) * qScale

	scale := float32(f.Scale) * sScale
	px := float32(f.Pos[0]) * pScale
	py := float32(f.Pos[1]) * pScale
	pz := float32(f.Pos[2]) * pScale

	return composeTransform(qx, qy, qz, qw, px, py, pz, scale)
}

// lerpFrame interpolates between two packed frames.
func lerpFrame(a, b esf.AnimPackFrame, t float32) mat4 {
	const qScale = 1.0 / 32767.0
	const sScale = 1.0 / 512.0
	const pScale = 1.0 / 512.0

	t1 := 1.0 - t

	// Quaternion SLERP (approximated with NLERP for speed).
	ax := float32(a.Quat[0]) * qScale
	ay := float32(a.Quat[1]) * qScale
	az := float32(a.Quat[2]) * qScale
	aw := float32(a.Quat[3]) * qScale
	bx := float32(b.Quat[0]) * qScale
	by := float32(b.Quat[1]) * qScale
	bz := float32(b.Quat[2]) * qScale
	bw := float32(b.Quat[3]) * qScale

	// Ensure shortest path.
	dot := ax*bx + ay*by + az*bz + aw*bw
	if dot < 0 {
		bx, by, bz, bw = -bx, -by, -bz, -bw
	}

	qx := t1*ax + t*bx
	qy := t1*ay + t*by
	qz := t1*az + t*bz
	qw := t1*aw + t*bw

	// Normalize.
	invLen := 1.0 / float32(math.Sqrt(float64(qx*qx+qy*qy+qz*qz+qw*qw)))
	qx *= invLen
	qy *= invLen
	qz *= invLen
	qw *= invLen

	// Linear interpolation for scale and position.
	scale := t1*float32(a.Scale)*sScale + t*float32(b.Scale)*sScale
	px := t1*float32(a.Pos[0])*pScale + t*float32(b.Pos[0])*pScale
	py := t1*float32(a.Pos[1])*pScale + t*float32(b.Pos[1])*pScale
	pz := t1*float32(a.Pos[2])*pScale + t*float32(b.Pos[2])*pScale

	return composeTransform(qx, qy, qz, qw, px, py, pz, scale)
}

// --- 4x4 matrix math (row-major) ---

type mat4 [16]float32

var mat4Identity = mat4{
	1, 0, 0, 0,
	0, 1, 0, 0,
	0, 0, 1, 0,
	0, 0, 0, 1,
}

// composeTransform builds a TRS matrix from quaternion rotation, position, and uniform scale.
// Column-vector convention: rotation*scale in upper-left 3x3, translation in last column.
// Transform a point: p' = M * p (use transformPoint).
// Chain transforms: W_child = mulMat4(W_parent, L_child).
func composeTransform(qx, qy, qz, qw, px, py, pz, scale float32) mat4 {
	// Quaternion to rotation matrix (scaled).
	x2 := qx + qx
	y2 := qy + qy
	z2 := qz + qz
	xx := qx * x2
	xy := qx * y2
	xz := qx * z2
	yy := qy * y2
	yz := qy * z2
	zz := qz * z2
	wx := qw * x2
	wy := qw * y2
	wz := qw * z2

	return mat4{
		(1 - (yy + zz)) * scale, (xy - wz) * scale, (xz + wy) * scale, px,
		(xy + wz) * scale, (1 - (xx + zz)) * scale, (yz - wx) * scale, py,
		(xz - wy) * scale, (yz + wx) * scale, (1 - (xx + yy)) * scale, pz,
		0, 0, 0, 1,
	}
}

func mulMat4(a, b mat4) mat4 {
	return mat4{
		a[0]*b[0] + a[1]*b[4] + a[2]*b[8] + a[3]*b[12],
		a[0]*b[1] + a[1]*b[5] + a[2]*b[9] + a[3]*b[13],
		a[0]*b[2] + a[1]*b[6] + a[2]*b[10] + a[3]*b[14],
		a[0]*b[3] + a[1]*b[7] + a[2]*b[11] + a[3]*b[15],

		a[4]*b[0] + a[5]*b[4] + a[6]*b[8] + a[7]*b[12],
		a[4]*b[1] + a[5]*b[5] + a[6]*b[9] + a[7]*b[13],
		a[4]*b[2] + a[5]*b[6] + a[6]*b[10] + a[7]*b[14],
		a[4]*b[3] + a[5]*b[7] + a[6]*b[11] + a[7]*b[15],

		a[8]*b[0] + a[9]*b[4] + a[10]*b[8] + a[11]*b[12],
		a[8]*b[1] + a[9]*b[5] + a[10]*b[9] + a[11]*b[13],
		a[8]*b[2] + a[9]*b[6] + a[10]*b[10] + a[11]*b[14],
		a[8]*b[3] + a[9]*b[7] + a[10]*b[11] + a[11]*b[15],

		a[12]*b[0] + a[13]*b[4] + a[14]*b[8] + a[15]*b[12],
		a[12]*b[1] + a[13]*b[5] + a[14]*b[9] + a[15]*b[13],
		a[12]*b[2] + a[13]*b[6] + a[14]*b[10] + a[15]*b[14],
		a[12]*b[3] + a[13]*b[7] + a[14]*b[11] + a[15]*b[15],
	}
}

// invertMat4 computes the inverse of a 4x4 matrix using cofactor expansion.
func invertMat4(m mat4) mat4 {
	var inv mat4

	inv[0] = m[5]*m[10]*m[15] - m[5]*m[11]*m[14] - m[9]*m[6]*m[15] + m[9]*m[7]*m[14] + m[13]*m[6]*m[11] - m[13]*m[7]*m[10]
	inv[4] = -m[4]*m[10]*m[15] + m[4]*m[11]*m[14] + m[8]*m[6]*m[15] - m[8]*m[7]*m[14] - m[12]*m[6]*m[11] + m[12]*m[7]*m[10]
	inv[8] = m[4]*m[9]*m[15] - m[4]*m[11]*m[13] - m[8]*m[5]*m[15] + m[8]*m[7]*m[13] + m[12]*m[5]*m[11] - m[12]*m[7]*m[9]
	inv[12] = -m[4]*m[9]*m[14] + m[4]*m[10]*m[13] + m[8]*m[5]*m[14] - m[8]*m[6]*m[13] - m[12]*m[5]*m[10] + m[12]*m[6]*m[9]

	inv[1] = -m[1]*m[10]*m[15] + m[1]*m[11]*m[14] + m[9]*m[2]*m[15] - m[9]*m[3]*m[14] - m[13]*m[2]*m[11] + m[13]*m[3]*m[10]
	inv[5] = m[0]*m[10]*m[15] - m[0]*m[11]*m[14] - m[8]*m[2]*m[15] + m[8]*m[3]*m[14] + m[12]*m[2]*m[11] - m[12]*m[3]*m[10]
	inv[9] = -m[0]*m[9]*m[15] + m[0]*m[11]*m[13] + m[8]*m[1]*m[15] - m[8]*m[3]*m[13] - m[12]*m[1]*m[11] + m[12]*m[3]*m[9]
	inv[13] = m[0]*m[9]*m[14] - m[0]*m[10]*m[13] - m[8]*m[1]*m[14] + m[8]*m[2]*m[13] + m[12]*m[1]*m[10] - m[12]*m[2]*m[9]

	inv[2] = m[1]*m[6]*m[15] - m[1]*m[7]*m[14] - m[5]*m[2]*m[15] + m[5]*m[3]*m[14] + m[13]*m[2]*m[7] - m[13]*m[3]*m[6]
	inv[6] = -m[0]*m[6]*m[15] + m[0]*m[7]*m[14] + m[4]*m[2]*m[15] - m[4]*m[3]*m[14] - m[12]*m[2]*m[7] + m[12]*m[3]*m[6]
	inv[10] = m[0]*m[5]*m[15] - m[0]*m[7]*m[13] - m[4]*m[1]*m[15] + m[4]*m[3]*m[13] + m[12]*m[1]*m[7] - m[12]*m[3]*m[5]
	inv[14] = -m[0]*m[5]*m[14] + m[0]*m[6]*m[13] + m[4]*m[1]*m[14] - m[4]*m[2]*m[13] - m[12]*m[1]*m[6] + m[12]*m[2]*m[5]

	inv[3] = -m[1]*m[6]*m[11] + m[1]*m[7]*m[10] + m[5]*m[2]*m[11] - m[5]*m[3]*m[10] - m[9]*m[2]*m[7] + m[9]*m[3]*m[6]
	inv[7] = m[0]*m[6]*m[11] - m[0]*m[7]*m[10] - m[4]*m[2]*m[11] + m[4]*m[3]*m[10] + m[8]*m[2]*m[7] - m[8]*m[3]*m[6]
	inv[11] = -m[0]*m[5]*m[11] + m[0]*m[7]*m[9] + m[4]*m[1]*m[11] - m[4]*m[3]*m[9] - m[8]*m[1]*m[7] + m[8]*m[3]*m[5]
	inv[15] = m[0]*m[5]*m[10] - m[0]*m[6]*m[9] - m[4]*m[1]*m[10] + m[4]*m[2]*m[9] + m[8]*m[1]*m[6] - m[8]*m[2]*m[5]

	det := m[0]*inv[0] + m[1]*inv[4] + m[2]*inv[8] + m[3]*inv[12]
	if det == 0 {
		return mat4Identity
	}
	invDet := 1.0 / det
	for i := range inv {
		inv[i] *= invDet
	}
	return inv
}

// centerTransformMat builds the PS2 centerTransform matrix.
// PS2 row-vector: centerTransform = Scale(s) * T(-c) * R(quat, pos) * T(+c)
// Column-vector equivalent: T(+c) * R(facing, pos) * T(-c) * Scale(s)
//
// For a point p in raw model space:
//   p' = R*(s*p - c) + pos + c
//
// With Y-axis rotation only (facing):
//   X: cosF*s*px + sinF*s*pz - cosF*cx - sinF*cz + posX + cx
//   Y: s*py - cy + posY + cy  (= s*py + posY)
//   Z: -sinF*s*px + cosF*s*pz + sinF*cx - cosF*cz + posZ + cz
func centerTransformMat(sinF, cosF, s, cx, cy, cz, posX, posY, posZ float32) mat4 {
	// Translation = R*(-c) + pos + c
	tx := cosF*(-cx) + sinF*(-cz) + posX + cx
	ty := -cy + posY + cy // simplifies to posY
	tz := -sinF*(-cx) + cosF*(-cz) + posZ + cz
	return mat4{
		cosF * s, 0, sinF * s, tx,
		0, s, 0, ty,
		-sinF * s, 0, cosF * s, tz,
		0, 0, 0, 1,
	}
}

// transformPoint applies a 4x4 matrix to a 3D point (column-vector: p' = M * p).
func transformPoint(m mat4, x, y, z float32) (float32, float32, float32) {
	rx := m[0]*x + m[1]*y + m[2]*z + m[3]
	ry := m[4]*x + m[5]*y + m[6]*z + m[7]
	rz := m[8]*x + m[9]*y + m[10]*z + m[11]
	return rx, ry, rz
}
