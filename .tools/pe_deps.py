import os
import struct

def get_pe_imports(file_path):
    try:
        with open(file_path, "rb") as f:
            data = f.read()
    except Exception:
        return []

    if len(data) < 64 or data[:2] != b"MZ":
        return []

    pe_offset = struct.unpack("<I", data[60:64])[0]
    if pe_offset + 24 > len(data) or data[pe_offset:pe_offset+4] != b"PE\x00\x00":
        return []

    # Optional header
    magic = struct.unpack("<H", data[pe_offset+24:pe_offset+26])[0]
    if magic == 0x10b: # PE32
        import_table_rva_offset = pe_offset + 24 + 96 + 8
    elif magic == 0x20b: # PE32+
        import_table_rva_offset = pe_offset + 24 + 112 + 8
    else:
        return []

    if import_table_rva_offset + 8 > len(data):
        return []

    import_rva, import_size = struct.unpack("<II", data[import_table_rva_offset:import_table_rva_offset+8])
    if import_rva == 0 or import_size == 0:
        return []

    # Number of sections
    num_sections = struct.unpack("<H", data[pe_offset+6:pe_offset+8])[0]
    opt_header_size = struct.unpack("<H", data[pe_offset+20:pe_offset+22])[0]
    sec_header_start = pe_offset + 24 + opt_header_size

    sections = []
    for i in range(num_sections):
        sec_start = sec_header_start + i * 40
        if sec_start + 40 > len(data):
            break
        vsize, vaddr, raw_size, raw_offset = struct.unpack("<IIII", data[sec_start+8:sec_start+24])
        sections.append((vaddr, vsize, raw_offset, raw_size))

    def rva_to_offset(rva):
        for vaddr, vsize, raw_offset, raw_size in sections:
            if vaddr <= rva < vaddr + max(vsize, raw_size):
                return raw_offset + (rva - vaddr)
        return None

    import_offset = rva_to_offset(import_rva)
    if import_offset is None or import_offset >= len(data):
        return []

    dlls = []
    curr = import_offset
    while curr + 20 <= len(data):
        original_first_thunk, time_stamp, forwarder_chain, name_rva, first_thunk = struct.unpack("<IIIII", data[curr:curr+20])
        if original_first_thunk == 0 and name_rva == 0:
            break
        name_offset = rva_to_offset(name_rva)
        if name_offset and name_offset < len(data):
            # Read null-terminated string
            name_bytes = bytearray()
            while name_offset < len(data) and data[name_offset] != 0:
                name_bytes.append(data[name_offset])
                name_offset += 1
            dlls.append(name_bytes.decode('ascii', errors='ignore'))
        curr += 20

    return dlls

if __name__ == "__main__":
    exe = "dist/windows-amd64/GoDeck.exe"
    print("GoDeck.exe direct imports:")
    for d in get_pe_imports(exe):
        print("  -", d)
