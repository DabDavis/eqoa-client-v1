#!/usr/bin/env python3
"""
MIPS32 Disassembler for EQOA PS2 Entity Movement Interpolation/Extrapolation Analysis.
"""

import struct
import sys
import os

GPR_NAMES = [
    "$zero", "$at", "$v0", "$v1", "$a0", "$a1", "$a2", "$a3",
    "$t0", "$t1", "$t2", "$t3", "$t4", "$t5", "$t6", "$t7",
    "$s0", "$s1", "$s2", "$s3", "$s4", "$s5", "$s6", "$s7",
    "$t8", "$t9", "$k0", "$k1", "$gp", "$sp", "$fp", "$ra"
]
FPR_NAMES = [f"$f{i}" for i in range(32)]

def sign_extend_16(val):
    return val - 0x10000 if val & 0x8000 else val

def u32_to_float(u32):
    return struct.unpack('f', struct.pack('I', u32 & 0xFFFFFFFF))[0]

def decode_mips(word, addr):
    op = (word >> 26) & 0x3F
    rs = (word >> 21) & 0x1F
    rt = (word >> 16) & 0x1F
    rd = (word >> 11) & 0x1F
    sa = (word >> 6) & 0x1F
    funct = word & 0x3F
    imm = word & 0xFFFF
    simm = sign_extend_16(imm)
    target = word & 0x3FFFFFF
    comment = ""

    if word == 0:
        return "nop", "", ""

    if op == 0:
        r_ops = {
            0x00: ("sll", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {sa}"),
            0x02: ("srl", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {sa}"),
            0x03: ("sra", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {sa}"),
            0x04: ("sllv", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {GPR_NAMES[rs]}"),
            0x06: ("srlv", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {GPR_NAMES[rs]}"),
            0x07: ("srav", f"{GPR_NAMES[rd]}, {GPR_NAMES[rt]}, {GPR_NAMES[rs]}"),
            0x08: ("jr", f"{GPR_NAMES[rs]}"),
            0x0A: ("movz", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x0B: ("movn", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x0C: ("syscall", f"0x{(word>>6)&0xFFFFF:05X}"),
            0x0D: ("break", f"0x{(word>>6)&0xFFFFF:05X}"),
            0x0F: ("sync", ""),
            0x10: ("mfhi", f"{GPR_NAMES[rd]}"),
            0x11: ("mthi", f"{GPR_NAMES[rs]}"),
            0x12: ("mflo", f"{GPR_NAMES[rd]}"),
            0x13: ("mtlo", f"{GPR_NAMES[rs]}"),
            0x18: ("mult", f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x19: ("multu", f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x1A: ("div", f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x1B: ("divu", f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x22: ("sub", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x23: ("subu", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x24: ("and", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x26: ("xor", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x27: ("nor", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x2A: ("slt", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
            0x2B: ("sltu", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}"),
        }
        if funct in r_ops:
            return r_ops[funct][0], r_ops[funct][1], ""
        if funct == 0x09:
            if rd == 31:
                return "jalr", f"{GPR_NAMES[rs]}", ""
            return "jalr", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}", ""
        if funct == 0x20:
            return "add", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}", ""
        if funct == 0x21:
            if rt == 0:
                return "move", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}", ""
            return "addu", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}", ""
        if funct == 0x25:
            if rt == 0:
                return "move", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}", ""
            return "or", f"{GPR_NAMES[rd]}, {GPR_NAMES[rs]}, {GPR_NAMES[rt]}", ""
        return f"special_0x{funct:02X}", f"0x{word:08X}", ""

    if op == 0x01:
        bt = addr + 4 + (simm << 2)
        names = {0: "bltz", 1: "bgez", 0x11: "bgezal"}
        n = names.get(rt, f"regimm_{rt}")
        return n, f"{GPR_NAMES[rs]}, 0x{bt:08X}", ""

    if op == 0x02:
        return "j", f"0x{(addr & 0xF0000000) | (target << 2):08X}", ""
    if op == 0x03:
        return "jal", f"0x{(addr & 0xF0000000) | (target << 2):08X}", ""

    branch_ops = {0x04: "beq", 0x05: "bne", 0x06: "blez", 0x07: "bgtz"}
    if op in branch_ops:
        bt = addr + 4 + (simm << 2)
        if op in (0x06, 0x07):
            return branch_ops[op], f"{GPR_NAMES[rs]}, 0x{bt:08X}", ""
        return branch_ops[op], f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}, 0x{bt:08X}", ""

    if op == 0x08:
        return "addi", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, {simm}", ""
    if op == 0x09:
        if rs == 0:
            return "li", f"{GPR_NAMES[rt]}, {simm}", ""
        return "addiu", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, {simm}", ""
    if op == 0x0A:
        return "slti", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, {simm}", ""
    if op == 0x0B:
        return "sltiu", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, {imm}", ""
    if op == 0x0C:
        return "andi", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, 0x{imm:04X}", ""
    if op == 0x0D:
        return "ori", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, 0x{imm:04X}", ""
    if op == 0x0E:
        return "xori", f"{GPR_NAMES[rt]}, {GPR_NAMES[rs]}, 0x{imm:04X}", ""
    if op == 0x0F:
        fv = u32_to_float(imm << 16)
        c = f"upper=0x{imm:04X}0000"
        if 1e-10 < abs(fv) < 1e10:
            c += f" (float={fv})"
        return "lui", f"{GPR_NAMES[rt]}, 0x{imm:04X}", c

    mem_ops = {
        0x20: "lb", 0x21: "lh", 0x22: "lwl", 0x23: "lw",
        0x24: "lbu", 0x25: "lhu", 0x26: "lwr",
        0x28: "sb", 0x29: "sh", 0x2A: "swl", 0x2B: "sw", 0x2E: "swr"
    }
    if op in mem_ops:
        return mem_ops[op], f"{GPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", ""

    if op == 0x31:
        return "lwc1", f"{FPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", ""
    if op == 0x39:
        return "swc1", f"{FPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", ""

    if op == 0x37:
        return "ld", f"{GPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", "64-bit load"
    if op == 0x3F:
        return "sd", f"{GPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", "64-bit store"
    if op == 0x1E:
        return "lq", f"{GPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", "R5900 128-bit"
    if op == 0x1F:
        return "sq", f"{GPR_NAMES[rt]}, {simm}({GPR_NAMES[rs]})", "R5900 128-bit"

    if op == 0x11:
        if rs == 0:
            return "mfc1", f"{GPR_NAMES[rt]}, {FPR_NAMES[rd]}", ""
        if rs == 2:
            return "cfc1", f"{GPR_NAMES[rt]}, ${rd}", ""
        if rs == 4:
            return "mtc1", f"{GPR_NAMES[rt]}, {FPR_NAMES[rd]}", ""
        if rs == 6:
            return "ctc1", f"{GPR_NAMES[rt]}, ${rd}", ""
        if rs == 8:
            bt = addr + 4 + (simm << 2)
            return ("bc1t" if rt & 1 else "bc1f"), f"0x{bt:08X}", ""
        if rs in (0x10, 0x11, 0x14):
            fmt_c = {0x10: "s", 0x11: "d", 0x14: "w"}.get(rs, "?")
            ft, fs, fd = rt, rd, sa
            fpu_3 = {0x00: "add", 0x01: "sub", 0x02: "mul", 0x03: "div"}
            fpu_2 = {
                0x04: "sqrt", 0x05: "abs", 0x06: "mov", 0x07: "neg",
                0x0C: "round.w", 0x0D: "trunc.w", 0x0E: "ceil.w", 0x0F: "floor.w",
                0x20: "cvt.s", 0x21: "cvt.d", 0x24: "cvt.w"
            }
            fpu_cmp = {
                0x30: "c.f", 0x31: "c.un", 0x32: "c.eq", 0x33: "c.ueq",
                0x34: "c.olt", 0x35: "c.ult", 0x36: "c.ole", 0x37: "c.ule",
                0x38: "c.sf", 0x3C: "c.lt", 0x3E: "c.le"
            }
            if funct in fpu_3:
                return f"{fpu_3[funct]}.{fmt_c}", f"{FPR_NAMES[fd]}, {FPR_NAMES[fs]}, {FPR_NAMES[ft]}", ""
            if funct in fpu_2:
                return f"{fpu_2[funct]}.{fmt_c}", f"{FPR_NAMES[fd]}, {FPR_NAMES[fs]}", ""
            if funct in fpu_cmp:
                return f"{fpu_cmp[funct]}.{fmt_c}", f"{FPR_NAMES[fs]}, {FPR_NAMES[ft]}", ""
            return f"cop1_{fmt_c}_0x{funct:02X}", f"{FPR_NAMES[fd]}, {FPR_NAMES[fs]}, {FPR_NAMES[ft]}", ""
        return f"cop1_rs{rs:02X}", f"0x{word:08X}", ""

    if op == 0x10:
        if rs == 0:
            return "mfc0", f"{GPR_NAMES[rt]}, ${rd}", ""
        if rs == 4:
            return "mtc0", f"{GPR_NAMES[rt]}, ${rd}", ""
        return f"cop0_0x{rs:02X}", f"0x{word:08X}", ""

    if op == 0x1C:
        mmi = {0x18: "mult1", 0x19: "multu1", 0x1A: "div1", 0x1B: "divu1",
               0x10: "mfhi1", 0x12: "mflo1"}
        if funct in mmi:
            if funct in (0x10, 0x12):
                return mmi[funct], f"{GPR_NAMES[rd]}", "R5900"
            return mmi[funct], f"{GPR_NAMES[rs]}, {GPR_NAMES[rt]}", "R5900"
        return f"mmi_0x{funct:02X}", f"0x{word:08X}", "R5900"

    return f"op_0x{op:02X}", f"0x{word:08X}", ""


class BinaryReader:
    def __init__(self, filepath, base_addr=0):
        with open(filepath, 'rb') as f:
            self.data = f.read()
        self.base_addr = base_addr

    def read_u32(self, addr):
        off = addr - self.base_addr
        if off < 0 or off + 4 > len(self.data):
            return None
        return struct.unpack_from('<I', self.data, off)[0]

    def read_string(self, addr, max_len=256):
        off = addr - self.base_addr
        if off < 0 or off >= len(self.data):
            return None
        end = self.data.find(b'\x00', off, min(off + max_len, len(self.data)))
        if end == -1:
            end = min(off + max_len, len(self.data))
        return self.data[off:end].decode('ascii', errors='replace')

    def search_u32(self, value, start=None, end=None):
        if start is None:
            start = self.base_addr
        if end is None:
            end = self.base_addr + len(self.data)
        results = []
        packed = struct.pack('<I', value & 0xFFFFFFFF)
        o = start - self.base_addr
        e = end - self.base_addr
        while o < e - 3:
            pos = self.data.find(packed, o, e)
            if pos == -1:
                break
            results.append(pos + self.base_addr)
            o = pos + 4
        return results

    def search_bytes(self, pattern, start=None, end=None):
        if start is None:
            start = self.base_addr
        if end is None:
            end = self.base_addr + len(self.data)
        results = []
        o = start - self.base_addr
        e = end - self.base_addr
        while o < e - len(pattern):
            pos = self.data.find(pattern, o, e)
            if pos == -1:
                break
            results.append(pos + self.base_addr)
            o = pos + 1
        return results


def disasm(reader, start_addr, count=80, label=""):
    lines = []
    if label:
        lines.append(f"\n{'='*90}")
        lines.append(f"  {label}")
        lines.append(f"  Start: 0x{start_addr:08X}  ({count} instructions)")
        lines.append(f"{'='*90}")

    lui_vals = {}

    for i in range(count):
        addr = start_addr + i * 4
        word = reader.read_u32(addr)
        if word is None:
            lines.append(f"  0x{addr:08X}:  ????????  (out of bounds)")
            continue

        mnem, operands, comment = decode_mips(word, addr)

        # Track LUI
        if mnem == "lui":
            rt_idx = (word >> 16) & 0x1F
            lui_vals[rt_idx] = word & 0xFFFF

        # MTC1 after LUI = float constant
        if mnem == "mtc1":
            rt_idx = (word >> 16) & 0x1F
            if rt_idx in lui_vals:
                full = lui_vals[rt_idx] << 16
                try:
                    fv = u32_to_float(full)
                    if 1e-30 < abs(fv) < 1e30:
                        comment += f" ** FLOAT={fv} **"
                except:
                    pass

        # ORI after LUI = full constant
        if mnem == "ori":
            rs_idx = (word >> 21) & 0x1F
            if rs_idx in lui_vals:
                full = (lui_vals[rs_idx] << 16) | (word & 0xFFFF)
                try:
                    fv = u32_to_float(full)
                    if 1e-30 < abs(fv) < 1e30:
                        comment += f" =0x{full:08X} (float={fv})"
                    else:
                        comment += f" =0x{full:08X}"
                except:
                    comment += f" =0x{full:08X}"

        # ADDIU after LUI = address
        if mnem == "addiu":
            rs_idx = (word >> 21) & 0x1F
            if rs_idx in lui_vals:
                full = (lui_vals[rs_idx] << 16) + sign_extend_16(word & 0xFFFF)
                comment += f" addr=0x{full & 0xFFFFFFFF:08X}"
                # Try to resolve string at that address
                s = reader.read_string(full & 0xFFFFFFFF, 64)
                if s and len(s) > 3 and all(32 <= ord(c) < 127 for c in s[:20]):
                    comment += f' "{s[:40]}"'

        # LWC1 from GP-relative = float constant from .rodata
        if mnem == "lwc1":
            rs_idx = (word >> 21) & 0x1F
            off_val = sign_extend_16(word & 0xFFFF)
            # If loading from a known data address, try to read the float
            if rs_idx in lui_vals:
                data_addr = (lui_vals[rs_idx] << 16) + off_val
                fword = reader.read_u32(data_addr & 0xFFFFFFFF)
                if fword is not None:
                    try:
                        fv = u32_to_float(fword)
                        comment += f" [0x{data_addr & 0xFFFFFFFF:08X}]=0x{fword:08X} ({fv})"
                    except:
                        pass

        # Struct field annotations
        if mnem in ("lw", "sw", "lwc1", "swc1", "lh", "lhu", "lb", "lbu", "sh", "sb", "ld", "sd"):
            rs_idx = (word >> 21) & 0x1F
            iv = sign_extend_16(word & 0xFFFF)
            rn = GPR_NAMES[rs_idx]
            if rn in ("$a0", "$s0", "$s1", "$s2", "$s3", "$a1", "$a2", "$v0", "$v1", "$t0", "$t1", "$sp"):
                if iv >= 0:
                    comment += f" [{rn}+0x{iv:X}]"
                else:
                    comment += f" [{rn}-0x{-iv:X}]"

        line = f"  0x{addr:08X}:  {word:08X}  {mnem:16s} {operands:48s}"
        if comment:
            line += f"  ; {comment}"
        lines.append(line)

    return "\n".join(lines)


def main():
    out = []
    def p(s=""):
        out.append(s)

    p("=" * 100)
    p("  EQOA PS2 Entity Movement Interpolation / Extrapolation Disassembly")
    p("  Date: 2026-02-20")
    p("=" * 100)

    ee = BinaryReader("/home/sdg/claude-eqoa/claude-eqoa-iso/decrypted_modules/ee_memory_patched.bin", 0)
    sup = BinaryReader("/home/sdg/claude-eqoa/claude-eqoa-iso/decrypted_modules/SUPPORT_code.bin", 0x004C1D00)

    # ================================================================
    # 1. GetSmoothLocation (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 1. VIWalkPhysics::GetSmoothLocation (SUPPORT)")
    p("# Symbol: 0x0051368D (bit0=SN flag) -> code at 0x0051368C")
    p("# Returns VIVect3: smoothed/interpolated entity position")
    p("#"*100)
    p(disasm(sup, 0x0051368C, 100, "GetSmoothLocation (SUPPORT @ 0x0051368C)"))

    # ================================================================
    # 1b. GetSmoothLocation (CLIENT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 1b. VIWalkPhysics::GetSmoothLocation (CLIENT, EE memory)")
    p("# Symbol: 0x00CF776B -> code at 0x00CF7768")
    p("#"*100)
    p(disasm(ee, 0x00CF7768, 100, "GetSmoothLocation (CLIENT @ 0x00CF7768)"))

    # ================================================================
    # 2. SetUpdatePhysics (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 2. VIWalkControl::SetUpdatePhysics(VIWalkPhysics&, VIScene&) (SUPPORT)")
    p("# Symbol: 0x00513576 -> code at 0x00513574")
    p("#"*100)
    p(disasm(sup, 0x00513574, 100, "SetUpdatePhysics (SUPPORT @ 0x00513574)"))

    # ================================================================
    # 2b. SetUpdatePhysics (CLIENT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 2b. VIWalkControl::SetUpdatePhysics (CLIENT, EE memory)")
    p("# Symbol: 0x00CFA0E5 -> code at 0x00CFA0E4")
    p("#"*100)
    p(disasm(ee, 0x00CFA0E4, 120, "SetUpdatePhysics (CLIENT @ 0x00CFA0E4)"))

    # ================================================================
    # 3. VIWalkPhysics operator= (struct layout)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 3. VIWalkPhysics::operator= - struct copy reveals field layout")
    p("# Symbol: 0x0051328A -> code at 0x00513288")
    p("#"*100)
    p(disasm(sup, 0x00513288, 80, "VIWalkPhysics::operator= (SUPPORT @ 0x00513288)"))

    # ================================================================
    # 4. UpdatePhysics(float dt) (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 4. VIWalkControl::UpdatePhysics(float dt) (SUPPORT)")
    p("# Symbol: 0x005132F4")
    p("#"*100)
    p(disasm(sup, 0x005132F4, 80, "UpdatePhysics (SUPPORT @ 0x005132F4)"))

    # ================================================================
    # 5. Process(int, VIScene&) (SUPPORT + CLIENT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 5. VIWalkControl::Process(int, VIScene&) (SUPPORT)")
    p("# Symbol: 0x005131E4")
    p("#"*100)
    p(disasm(sup, 0x005131E4, 80, "Process (SUPPORT @ 0x005131E4)"))

    p("\n\n" + "#"*100)
    p("# 5b. VIWalkControl::Process (CLIENT)")
    p("# Symbol: 0x00CFA120")
    p("#"*100)
    p(disasm(ee, 0x00CFA120, 150, "Process (CLIENT @ 0x00CFA120)"))

    # ================================================================
    # 6. Move(int, VIScene&) (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 6. VIWalkControl::Move(int, VIScene&) (SUPPORT)")
    p("# Symbol: 0x00513330")
    p("#"*100)
    p(disasm(sup, 0x00513330, 80, "Move (SUPPORT @ 0x00513330)"))

    # ================================================================
    # 7. InitIntermediate (SUPPORT + CLIENT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 7. VIWalkControl::InitIntermediate (SUPPORT)")
    p("# Symbol: 0x005132B1 -> 0x005132B0")
    p("#"*100)
    p(disasm(sup, 0x005132B0, 60, "InitIntermediate (SUPPORT @ 0x005132B0)"))

    p("\n\n" + "#"*100)
    p("# 7b. VIWalkControl::InitIntermediate (CLIENT)")
    p("# Symbol: 0x00CF7662 -> 0x00CF7660")
    p("#"*100)
    p(disasm(ee, 0x00CF7660, 80, "InitIntermediate (CLIENT @ 0x00CF7660)"))

    # ================================================================
    # 8. VIAIExtrapolateFilter
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 8. VIAIExtrapolateFilter — RTTI search + vtable")
    p("# RTTI at 0x00CF3210: '21VIAIExtrapolateFilter'")
    p("#"*100)

    rtti_addr = 0x00CF3210
    rtti_str = ee.read_string(rtti_addr, 64)
    p(f"\n  RTTI: '{rtti_str}'")

    # Find typeinfo (contains pointer to RTTI name)
    p(f"\n  Searching for refs to 0x{rtti_addr:08X}...")
    refs = ee.search_u32(rtti_addr, 0x00100000, 0x02000000)
    p(f"  Found {len(refs)} refs:")

    vtable_methods = []
    for ref in refs:
        typeinfo_start = ref - 4
        p(f"\n  Typeinfo candidate at 0x{typeinfo_start:08X}:")
        for d in range(-8, 24, 4):
            a = ref + d
            w = ee.read_u32(a)
            if w is not None:
                m = ""
                if a == ref: m = " <-- name ptr"
                if a == ref-4: m = " <-- typeinfo vptr"
                if 0x00CC0000 <= w <= 0x00D10000: m += " [CLIENT]"
                elif 0x004C0000 <= w <= 0x00560000: m += " [SUPPORT]"
                p(f"    0x{a:08X}: 0x{w:08X}{m}")

        # Search for vtable pointing to this typeinfo
        ti_refs = ee.search_u32(typeinfo_start, 0x00100000, 0x02000000)
        p(f"  Refs to typeinfo 0x{typeinfo_start:08X}: {len(ti_refs)}")
        for tref in ti_refs:
            p(f"\n  Vtable area around 0x{tref:08X}:")
            for d in range(-12, 64, 4):
                a = tref + d
                w = ee.read_u32(a)
                if w is not None:
                    m = ""
                    if a == tref: m = " <-- typeinfo ptr"
                    if 0x00CC0000 <= w <= 0x00D10000:
                        m += " [CLIENT]"
                        if d > 0:
                            vtable_methods.append((d//4 - 1, w))
                    elif 0x004C0000 <= w <= 0x00560000:
                        m += " [SUPPORT]"
                        if d > 0:
                            vtable_methods.append((d//4 - 1, w))
                    p(f"    0x{a:08X}: 0x{w:08X}{m}")

    # Also search for related RTTI strings
    p("\n  Other AI filter RTTI strings in EE memory:")
    for term in [b"VIAIFilter", b"VIAIMoveFilter", b"VIAIExtrapolate",
                 b"AIInterpolate", b"AISmooth", b"MoveFilter"]:
        for addr_found in ee.search_bytes(term, 0x00CC0000, 0x00D10000):
            s = ee.read_string(addr_found, 64)
            p(f"    0x{addr_found:08X}: '{s}'")

    # ================================================================
    # 9. Disassemble VIAIExtrapolateFilter vtable methods
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 9. VIAIExtrapolateFilter vtable method disassembly")
    p("#"*100)

    if vtable_methods:
        seen = set()
        for idx, maddr in vtable_methods:
            aligned = maddr & ~3
            if aligned in seen:
                continue
            seen.add(aligned)
            p(disasm(ee, aligned, 80,
                f"ExtrapolateFilter vtable[{idx}] @ 0x{aligned:08X}"))
    else:
        p("\n  No vtable methods found via RTTI chain.")
        p("  Trying code xref search for RTTI address...")
        upper = (rtti_addr >> 16) & 0xFFFF
        lower = rtti_addr & 0xFFFF
        for rt_reg in range(32):
            lui_pat = (0x0F << 26) | (rt_reg << 16) | upper
            for la in ee.search_u32(lui_pat, 0x00CC0000, 0x00D10000):
                for delta in range(1, 8):
                    nw = ee.read_u32(la + delta * 4)
                    if nw is not None:
                        nop = (nw >> 26) & 0x3F
                        nim = sign_extend_16(nw & 0xFFFF)
                        if nop == 0x09 and nim == sign_extend_16(lower):
                            p(f"\n  Found LUI+ADDIU for RTTI at 0x{la:08X}")
                            p(disasm(ee, la - 32, 40,
                                f"Code near RTTI ref @ 0x{la:08X}"))

    # ================================================================
    # 10. Constructor (SUPPORT) - struct defaults
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 10. VIWalkControl::VIWalkControl() constructor (SUPPORT)")
    p("# Symbol: 0x0051323A -> 0x00513238")
    p("#"*100)
    p(disasm(sup, 0x00513238, 80, "VIWalkControl ctor (SUPPORT @ 0x00513238)"))

    # ================================================================
    # 11. Gravity/WGravity globals
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 11. VIWalkControl::$Gravity and $WGravity")
    p("#"*100)
    for name, sa in [("$Gravity", 0x00513204), ("$WGravity", 0x00513220)]:
        p(f"\n  {name}:")
        for d in range(6):
            a = sa + d*4
            v = sup.read_u32(a)
            if v is not None:
                try:
                    p(f"    0x{a:08X}: 0x{v:08X} = {u32_to_float(v)}")
                except:
                    p(f"    0x{a:08X}: 0x{v:08X}")

    # ================================================================
    # 12. UpdateLocomotion (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 12. VIWalkControl::UpdateLocomotion(float dt) (SUPPORT)")
    p("# Symbol: 0x00513372 -> 0x00513370")
    p("#"*100)
    p(disasm(sup, 0x00513370, 80, "UpdateLocomotion (SUPPORT @ 0x00513370)"))

    # ================================================================
    # 13. UpdateDirections (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 13. VIWalkControl::UpdateDirections (SUPPORT)")
    p("# Symbol: 0x00513350")
    p("#"*100)
    p(disasm(sup, 0x00513350, 80, "UpdateDirections (SUPPORT @ 0x00513350)"))

    # ================================================================
    # 14. UpdateSprite (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 14. VIWalkControl::UpdateSprite (SUPPORT)")
    p("# Symbol: 0x005135E0")
    p("#"*100)
    p(disasm(sup, 0x005135E0, 80, "UpdateSprite (SUPPORT @ 0x005135E0)"))

    # ================================================================
    # 15. SetLocation (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 15. VIWalkControl::SetLocation (SUPPORT)")
    p("# Symbol: 0x005131B2 -> 0x005131B0")
    p("#"*100)
    p(disasm(sup, 0x005131B0, 80, "SetLocation (SUPPORT @ 0x005131B0)"))

    # ================================================================
    # 16. Walk / Run / SetTargetHeading
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 16. Walk/Run/Move/SetTargetHeading (SUPPORT)")
    p("#"*100)
    for name, sym in [("Walk", 0x005133B0), ("Run", 0x005133C8),
                       ("Move_ff", 0x005133E0), ("SetTargetHeading", 0x00513424)]:
        p(disasm(sup, sym, 40, f"VIWalkControl::{name} (SUPPORT @ 0x{sym:08X})"))

    # ================================================================
    # 17. CalcFloor (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 17. VIWalkControl::CalcFloor (SUPPORT)")
    p("# Symbol: 0x005135B1 -> 0x005135B0")
    p("#"*100)
    p(disasm(sup, 0x005135B0, 80, "CalcFloor (SUPPORT @ 0x005135B0)"))

    # ================================================================
    # 18. CalcPitchRollToLandscape (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 18. CalcPitchRollToLandscape (SUPPORT)")
    p("# Symbol: 0x00513612 -> 0x00513610")
    p("#"*100)
    p(disasm(sup, 0x00513610, 60, "CalcPitchRollToLandscape (SUPPORT @ 0x00513610)"))

    # ================================================================
    # 19. CalcFit (SUPPORT)
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 19. VIWalkControl::CalcFit (SUPPORT)")
    p("# Symbol: 0x00513646 -> 0x00513644")
    p("#"*100)
    p(disasm(sup, 0x00513644, 80, "CalcFit (SUPPORT @ 0x00513644)"))

    # ================================================================
    # 20. Init (CLIENT) - full init
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 20. VIWalkControl::Init (CLIENT)")
    p("# Symbol: 0x00CFA16F -> 0x00CFA16C")
    p("#"*100)
    p(disasm(ee, 0x00CFA16C, 100, "Init (CLIENT @ 0x00CFA16C)"))

    # ================================================================
    # 21. Float constants reference
    # ================================================================
    p("\n\n" + "#"*100)
    p("# 21. Float Constants Reference Table")
    p("#"*100)
    known = {
        0.0: "zero", 0.001: "millisec", 0.01: "1/100",
        1.0/60: "1/60 (60fps dt)", 1.0/30: "1/30 (30fps dt)",
        0.1: "10%", 0.15: "15%", 0.2: "20%", 0.25: "25%", 0.3: "30%",
        1.0/3: "1/3", 0.5: "50%", 2.0/3: "2/3", 0.75: "75%", 0.9: "90%",
        1.0: "one", 2.0: "two", 3.0: "three", 4.0: "four", 5.0: "five",
        10.0: "ten", 30.0: "thirty", 60.0: "sixty", 100.0: "hundred",
        1000.0: "thousand",
        -9.8: "gravity", -9.81: "gravity_precise", -32.0: "-32",
        3.14159265: "pi", 6.28318530: "2pi",
        0.017453293: "deg2rad", 57.29578: "rad2deg",
        255.0: "byte_max", 256.0: "256", 1.0/256: "1/256",
        128.0: "128", 127.0: "127",
    }
    p("\n  Value               Name                      IEEE754     LUI_upper")
    p("  " + "-"*80)
    for fv, name in sorted(known.items()):
        u = struct.unpack('I', struct.pack('f', fv))[0]
        p(f"  {fv:18.8f}  {name:25s}  0x{u:08X}  0x{(u>>16):04X}")

    # Write output
    text = "\n".join(out)
    with open("/tmp/entity_interp_disasm.txt", 'w') as f:
        f.write(text)
    print(f"Written {len(out)} lines to /tmp/entity_interp_disasm.txt")
    # Print just the key sections to stdout
    print(text)

if __name__ == "__main__":
    main()
