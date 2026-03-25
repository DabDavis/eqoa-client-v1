#!/usr/bin/env python3
"""
Extract NPC scale values from eqoa.live Freeport EE memory dump.

Searches for known NPC model IDs in 201-byte entity state buffers and
extracts the scale (float32 LE at offset 36) and position data.

State buffer layout:
  Offset 0:     isActive (byte)
  Offset 1-4:   objectID (uint32 LE)
  Offset 6-8:   X position (3-byte BE signed medium, value/128.0)
  Offset 9-11:  Y position (3-byte BE signed medium, value/128.0)
  Offset 12-14: Z position (3-byte BE signed medium, value/128.0)
  Offset 28-31: ModelID (uint32 LE)
  Offset 36-39: Scale (float32 LE)
"""

import struct
import sys

DUMP_PATH = "/home/sdg/claude-eqoa/eqoa.live-freeport.EEmemory"

# Known Freeport NPC model IDs from the DB
KNOWN_MODEL_IDS = {
    1893243078: "Guard",
    2845600533: "Craigwold (gnome)",
}


def read_medium_be_signed(data, offset):
    """Read a 3-byte big-endian signed integer."""
    b0 = data[offset]
    b1 = data[offset + 1]
    b2 = data[offset + 2]
    val = (b0 << 16) | (b1 << 8) | b2
    if val & 0x800000:
        val -= 0x1000000
    return val / 128.0


def search_for_model_ids(data):
    """Search for known model IDs and extract state buffer data."""
    results = []
    
    for model_id, name in KNOWN_MODEL_IDS.items():
        pattern = struct.pack('<I', model_id)
        pos = 0
        while True:
            idx = data.find(pattern, pos)
            if idx == -1:
                break
            
            buf_start = idx - 28
            
            if buf_start < 0 or buf_start + 201 > len(data):
                pos = idx + 1
                continue
            
            is_active = data[buf_start]
            object_id = struct.unpack_from('<I', data, buf_start + 1)[0]
            
            x = read_medium_be_signed(data, buf_start + 6)
            y = read_medium_be_signed(data, buf_start + 9)
            z = read_medium_be_signed(data, buf_start + 12)
            
            scale = struct.unpack_from('<f', data, buf_start + 36)[0]
            
            byte5 = data[buf_start + 5]
            field_32 = struct.unpack_from('<I', data, buf_start + 32)[0]
            
            plausible = True
            reasons = []
            
            if is_active not in (0, 1):
                plausible = False
                reasons.append(f"isActive={is_active}")
            
            if scale < 0.01 or scale > 100.0 or scale != scale:
                plausible = False
                reasons.append(f"scale={scale}")
            
            if abs(x) > 50000 or abs(y) > 5000 or abs(z) > 50000:
                plausible = False
                reasons.append(f"pos=({x:.1f}, {y:.1f}, {z:.1f})")
            
            results.append({
                'name': name,
                'model_id': model_id,
                'buf_start': buf_start,
                'is_active': is_active,
                'object_id': object_id,
                'x': x, 'y': y, 'z': z,
                'scale': scale,
                'field_32': field_32,
                'byte5': byte5,
                'plausible': plausible,
                'reasons': reasons,
                'match_offset': idx,
            })
            
            pos = idx + 1
    
    return results


def search_for_string(data, search_str):
    """Search for a string in the dump."""
    results = []
    
    pattern_ascii = search_str.encode('ascii')
    pos = 0
    while True:
        idx = data.find(pattern_ascii, pos)
        if idx == -1:
            break
        ctx_start = max(0, idx - 32)
        ctx_end = min(len(data), idx + len(pattern_ascii) + 64)
        context = data[ctx_start:ctx_end]
        results.append(('ASCII', idx, context, ctx_start))
        pos = idx + 1
    
    pattern_utf16 = search_str.encode('utf-16-le')
    pos = 0
    while True:
        idx = data.find(pattern_utf16, pos)
        if idx == -1:
            break
        ctx_start = max(0, idx - 32)
        ctx_end = min(len(data), idx + len(pattern_utf16) + 64)
        context = data[ctx_start:ctx_end]
        results.append(('UTF-16LE', idx, context, ctx_start))
        pos = idx + 1
    
    return results


def broader_scale_search(data):
    """Search broadly for model IDs and check nearby floats."""
    print("\n" + "="*80)
    print("BROADER SEARCH: All occurrences of known model IDs")
    print("="*80)
    
    for model_id, name in KNOWN_MODEL_IDS.items():
        pattern = struct.pack('<I', model_id)
        count = 0
        pos = 0
        while True:
            idx = data.find(pattern, pos)
            if idx == -1:
                break
            count += 1
            
            if count <= 20:
                print(f"\n  {name} (0x{model_id:08X}) found at dump offset 0x{idx:08X}")
                
                ctx_start = max(0, idx - 32)
                ctx_end = min(len(data), idx + 40)
                ctx = data[ctx_start:ctx_end]
                hex_str = ' '.join(f'{b:02X}' for b in ctx)
                print(f"  Context [{ctx_start:08X}]: {hex_str}")
                
                if idx >= 28 and idx + 12 <= len(data):
                    buf_start = idx - 28
                    is_active = data[buf_start]
                    obj_id = struct.unpack_from('<I', data, buf_start + 1)[0]
                    x = read_medium_be_signed(data, buf_start + 6)
                    y = read_medium_be_signed(data, buf_start + 9)
                    z = read_medium_be_signed(data, buf_start + 12)
                    scale = struct.unpack_from('<f', data, buf_start + 36)[0]
                    print(f"    If offset 28: active={is_active}, objID={obj_id}, "
                          f"pos=({x:.1f}, {y:.1f}, {z:.1f}), scale={scale:.4f}")
                
                if idx >= 4 and idx + 40 <= len(data):
                    for delta in range(-16, 48, 4):
                        if idx + delta >= 0 and idx + delta + 4 <= len(data):
                            val = struct.unpack_from('<f', data, idx + delta)[0]
                            if 0.1 <= val <= 10.0 and val == val:
                                ival = struct.unpack_from('<I', data, idx + delta)[0]
                                print(f"    Float at modelID+{delta}: {val:.4f} (0x{ival:08X})")
            
            pos = idx + 1
        
        print(f"\n  Total occurrences of {name} model ID: {count}")


def search_state_channel_arrays(data):
    """Look for state channel buffer arrays with 201-byte spacing."""
    print("\n" + "="*80)
    print("SEARCHING FOR STATE CHANNEL BUFFER ARRAYS (201-byte spacing)")
    print("="*80)
    
    guard_pattern = struct.pack('<I', 1893243078)
    guard_locations = []
    pos = 0
    while True:
        idx = data.find(guard_pattern, pos)
        if idx == -1:
            break
        guard_locations.append(idx)
        pos = idx + 1
    
    print(f"\nGuard model ID locations ({len(guard_locations)} total):")
    for loc in guard_locations[:50]:
        print(f"  0x{loc:08X} ({loc})")
    
    print("\nChecking for 201-byte spacing between guard model IDs:")
    found_spacing = False
    for i in range(len(guard_locations)):
        for j in range(i+1, min(i+20, len(guard_locations))):
            diff = guard_locations[j] - guard_locations[i]
            if diff % 201 == 0 and 1 <= diff // 201 <= 30:
                n = diff // 201
                found_spacing = True
                print(f"  0x{guard_locations[i]:08X} -> 0x{guard_locations[j]:08X}: "
                      f"diff={diff}, {n} x 201 bytes apart")
                
                base = guard_locations[i] - 28
                for k in range(n + 1):
                    buf_off = base + k * 201
                    if buf_off >= 0 and buf_off + 201 <= len(data):
                        is_active = data[buf_off]
                        obj_id = struct.unpack_from('<I', data, buf_off + 1)[0]
                        mid = struct.unpack_from('<I', data, buf_off + 28)[0]
                        x = read_medium_be_signed(data, buf_off + 6)
                        y = read_medium_be_signed(data, buf_off + 9)
                        z = read_medium_be_signed(data, buf_off + 12)
                        scale = struct.unpack_from('<f', data, buf_off + 36)[0]
                        mname = KNOWN_MODEL_IDS.get(mid, f"unknown(0x{mid:08X})")
                        print(f"    Buffer[{k}] @0x{buf_off:08X}: active={is_active}, "
                              f"objID={obj_id}, model={mname}, "
                              f"pos=({x:.1f}, {y:.1f}, {z:.1f}), scale={scale:.4f}")
    
    if not found_spacing:
        print("  No 201-byte spacing found between guard model IDs")

    craig_pattern = struct.pack('<I', 2845600533)
    craig_locations = []
    pos = 0
    while True:
        idx = data.find(craig_pattern, pos)
        if idx == -1:
            break
        craig_locations.append(idx)
        pos = idx + 1
    
    print(f"\nCraigwold model ID locations ({len(craig_locations)} total):")
    for loc in craig_locations[:50]:
        print(f"  0x{loc:08X} ({loc})")
    
    all_locs = sorted(guard_locations + craig_locations)
    print(f"\nAll model ID locations sorted ({len(all_locs)} total):")
    print("Checking all pairs for 201-byte spacing:")
    found_any = False
    for i in range(len(all_locs)):
        for j in range(i+1, min(i+10, len(all_locs))):
            diff = all_locs[j] - all_locs[i]
            if diff % 201 == 0 and 1 <= diff // 201 <= 30:
                n = diff // 201
                found_any = True
                print(f"  0x{all_locs[i]:08X} -> 0x{all_locs[j]:08X}: {n} x 201 apart")
    if not found_any:
        print("  No 201-byte spacing found between any model ID pairs")


def dump_model_id_contexts(data):
    """Detailed hex dump around each model ID occurrence."""
    print("\n" + "="*80)
    print("DETAILED CONTEXT DUMP FOR MODEL ID OCCURRENCES")
    print("="*80)
    
    for model_id, name in KNOWN_MODEL_IDS.items():
        pattern = struct.pack('<I', model_id)
        pos = 0
        count = 0
        
        while True:
            idx = data.find(pattern, pos)
            if idx == -1:
                break
            count += 1
            
            if count <= 10:
                print(f"\n{'='*60}")
                print(f"{name} occurrence #{count} at 0x{idx:08X}")
                print(f"{'='*60}")
                
                start = max(0, idx - 64)
                end = min(len(data), idx + 68)
                
                for row_start in range(start, end, 16):
                    row_end = min(row_start + 16, end)
                    row_data = data[row_start:row_end]
                    hex_part = ' '.join(f'{b:02X}' for b in row_data)
                    ascii_part = ''.join(chr(b) if 32 <= b < 127 else '.' for b in row_data)
                    marker = " <<< MODEL ID" if row_start <= idx < row_start + 16 else ""
                    print(f"  {row_start:08X}: {hex_part:<48s} {ascii_part}{marker}")
                
                print(f"  Float32 values near model ID:")
                for delta in range(-32, 48, 4):
                    addr = idx + delta
                    if addr >= 0 and addr + 4 <= len(data):
                        fval = struct.unpack_from('<f', data, addr)[0]
                        ival = struct.unpack_from('<I', data, addr)[0]
                        ival_signed = struct.unpack_from('<i', data, addr)[0]
                        if fval == fval and abs(fval) < 100000 and fval != 0:
                            print(f"    [{delta:+3d}] float={fval:12.4f}  uint=0x{ival:08X}  int={ival_signed}")
            
            pos = idx + 1
        
        print(f"\n  Total {name} occurrences: {count}")


def main():
    print(f"Reading EE memory dump from {DUMP_PATH}...")
    with open(DUMP_PATH, 'rb') as f:
        data = f.read()
    print(f"Read {len(data)} bytes ({len(data)/1024/1024:.1f} MB)")
    
    # 1. Search for "Craigwold" string
    print("\n" + "="*80)
    print("SEARCHING FOR 'Craigwold' STRING")
    print("="*80)
    
    string_results = search_for_string(data, "Craigwold")
    if string_results:
        for encoding, offset, context, ctx_start in string_results:
            print(f"\n  Found ({encoding}) at offset 0x{offset:08X} ({offset}):")
            for row in range(0, len(context), 16):
                row_data = context[row:row+16]
                hex_str = ' '.join(f'{b:02X}' for b in row_data)
                ascii_str = ''.join(chr(b) if 32 <= b < 127 else '.' for b in row_data)
                print(f"    {ctx_start+row:08X}: {hex_str:<48s} {ascii_str}")
    else:
        print("  Not found!")
    
    for npc_name in ["Freeport", "Guard", "Merchant", "Banker"]:
        results = search_for_string(data, npc_name)
        if results:
            print(f"\n  '{npc_name}' found {len(results)} times")
            for encoding, offset, context, ctx_start in results[:3]:
                print(f"    ({encoding}) at 0x{offset:08X}")
    
    # 2. Search for model IDs at state buffer offset 28
    print("\n" + "="*80)
    print("SEARCHING FOR MODEL IDs AT STATE BUFFER OFFSET 28")
    print("="*80)
    
    results = search_for_model_ids(data)
    
    plausible = [r for r in results if r['plausible']]
    implausible = [r for r in results if not r['plausible']]
    
    print(f"\nFound {len(results)} total matches ({len(plausible)} plausible, {len(implausible)} implausible)")
    
    print("\n--- PLAUSIBLE MATCHES ---")
    for r in plausible:
        print(f"\n  {r['name']} (model 0x{r['model_id']:08X})")
        print(f"    Buffer at: 0x{r['buf_start']:08X}")
        print(f"    isActive: {r['is_active']}")
        print(f"    objectID: {r['object_id']}")
        print(f"    Position: ({r['x']:.2f}, {r['y']:.2f}, {r['z']:.2f})")
        print(f"    Scale: {r['scale']:.4f}")
        print(f"    Field@32: 0x{r['field_32']:08X}")
    
    if implausible:
        print(f"\n--- IMPLAUSIBLE MATCHES (first 20) ---")
        for r in implausible[:20]:
            print(f"  0x{r['buf_start']:08X}: {r['name']} - rejected: {', '.join(r['reasons'])}")
    
    # 3. Broader search
    broader_scale_search(data)
    
    # 4. State channel buffer arrays
    search_state_channel_arrays(data)
    
    # 5. Detailed context dump
    dump_model_id_contexts(data)


if __name__ == '__main__':
    main()
