// A small ZIP writer: entries stored as they are (no compression), which
// every unzip, Excel and Taboola's bulk upload read. Images are already
// compressed, so storing them costs nothing.

const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

export function crc32(bytes) {
  let c = 0xffffffff;
  for (let i = 0; i < bytes.length; i++) c = CRC_TABLE[(c ^ bytes[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

const encoder = new TextEncoder();

function dosTime(d) {
  return ((d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1)) & 0xffff;
}

function dosDate(d) {
  return (((d.getFullYear() - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate()) & 0xffff;
}

// zip packs files ([{name, data: Uint8Array}]) and returns the archive as a
// list of byte chunks, ready for new Blob(chunks) or concat. A file copied
// from another archive can keep its compressed bytes: {name, raw, method,
// crc, size} (see unzip).
export function zip(files, when = new Date()) {
  const chunks = [];
  const central = [];
  let offset = 0;
  const time = dosTime(when);
  const date = dosDate(when);
  for (const f of files) {
    const name = encoder.encode(f.name);
    const data = f.raw || f.data;
    const method = f.raw ? f.method : 0;
    const crc = f.raw ? f.crc : crc32(data);
    const size = f.raw ? f.size : data.length;
    const local = new DataView(new ArrayBuffer(30));
    local.setUint32(0, 0x04034b50, true);
    local.setUint16(4, 20, true); // version needed
    local.setUint16(6, 0x0800, true); // names are UTF-8
    local.setUint16(8, method, true); // 0 stored, 8 deflated
    local.setUint16(10, time, true);
    local.setUint16(12, date, true);
    local.setUint32(14, crc, true);
    local.setUint32(18, data.length, true);
    local.setUint32(22, size, true);
    local.setUint16(26, name.length, true);
    local.setUint16(28, 0, true);
    chunks.push(new Uint8Array(local.buffer), name, data);

    const entry = new DataView(new ArrayBuffer(46));
    entry.setUint32(0, 0x02014b50, true);
    entry.setUint16(4, 20, true); // made by
    entry.setUint16(6, 20, true);
    entry.setUint16(8, 0x0800, true);
    entry.setUint16(10, method, true);
    entry.setUint16(12, time, true);
    entry.setUint16(14, date, true);
    entry.setUint32(16, crc, true);
    entry.setUint32(20, data.length, true);
    entry.setUint32(24, size, true);
    entry.setUint16(28, name.length, true);
    entry.setUint32(42, offset, true);
    central.push(new Uint8Array(entry.buffer), name);
    offset += 30 + name.length + data.length;
  }
  let size = 0;
  for (const c of central) size += c.length;
  const end = new DataView(new ArrayBuffer(22));
  end.setUint32(0, 0x06054b50, true);
  end.setUint16(8, files.length, true);
  end.setUint16(10, files.length, true);
  end.setUint32(12, size, true);
  end.setUint32(16, offset, true);
  return [...chunks, ...central, new Uint8Array(end.buffer)];
}

// unzip lists an archive's files with their bytes still compressed:
// [{name, raw, method, crc, size}]. Only stored (0) and deflated (8) files,
// no ZIP64: what Excel and Realize write.
export function unzip(bytes) {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let end = -1;
  for (let i = bytes.length - 22; i >= Math.max(0, bytes.length - 22 - 65535); i--) {
    if (view.getUint32(i, true) === 0x06054b50) {
      end = i;
      break;
    }
  }
  if (end < 0) throw new Error("não é um arquivo .xlsx ou .zip");
  const count = view.getUint16(end + 10, true);
  let at = view.getUint32(end + 16, true);
  const files = [];
  const decoder = new TextDecoder();
  for (let n = 0; n < count; n++) {
    if (view.getUint32(at, true) !== 0x02014b50) throw new Error("arquivo corrompido");
    const method = view.getUint16(at + 10, true);
    const crc = view.getUint32(at + 16, true);
    const packed = view.getUint32(at + 20, true);
    const size = view.getUint32(at + 24, true);
    const nameLen = view.getUint16(at + 28, true);
    const extraLen = view.getUint16(at + 30, true);
    const commentLen = view.getUint16(at + 32, true);
    const offset = view.getUint32(at + 42, true);
    const name = decoder.decode(bytes.subarray(at + 46, at + 46 + nameLen));
    if (method !== 0 && method !== 8) throw new Error(`compressão ${method} não suportada em ${name}`);
    const start = offset + 30 + view.getUint16(offset + 26, true) + view.getUint16(offset + 28, true);
    files.push({ name, raw: bytes.subarray(start, start + packed), method, crc, size });
    at += 46 + nameLen + extraLen + commentLen;
  }
  return files;
}

// inflate returns one unzip entry's bytes.
export async function inflate(file) {
  if (file.method === 0) return file.raw;
  const stream = new Blob([file.raw]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

// concat joins byte chunks into one array.
export function concat(chunks) {
  let n = 0;
  for (const c of chunks) n += c.length;
  const out = new Uint8Array(n);
  let at = 0;
  for (const c of chunks) {
    out.set(c, at);
    at += c.length;
  }
  return out;
}
