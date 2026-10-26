// Rasterises the app icon (same shapes as public/icon.svg) into the PNG sizes
// the PWA manifest needs, with no image library: a tiny PNG encoder over zlib.
import { writeFileSync } from 'node:fs'
import { deflateSync } from 'node:zlib'

const hex = (h) => [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)]

// Shapes in a 512 coordinate space.
const shapes = [
  { kind: 'rect', x: 112, y: 128, w: 288, h: 88, r: 20, c: hex('#008771') },
  { kind: 'rect', x: 112, y: 240, w: 288, h: 88, r: 20, c: hex('#00a88c') },
  { kind: 'rect', x: 112, y: 352, w: 288, h: 40, r: 20, c: hex('#2c3e5c') },
  { kind: 'circle', x: 160, y: 172, r: 14, c: hex('#e6fffa') },
  { kind: 'circle', x: 160, y: 284, r: 14, c: hex('#e6fffa') },
]
const bg = hex('#0a1222')

function inRoundRect(px, py, x, y, w, h, r) {
  if (px < x || px > x + w || py < y || py > y + h) return false
  const cx = Math.min(Math.max(px, x + r), x + w - r)
  const cy = Math.min(Math.max(py, y + r), y + h - r)
  return (px - cx) ** 2 + (py - cy) ** 2 <= r * r
}

function render(size, { maskable }) {
  // Maskable icons keep content inside the central 80% safe zone.
  const scale = maskable ? 0.78 : 1
  const off = (512 - 512 * scale) / 2
  const px = Buffer.alloc(size * size * 4)
  const ss = 3 // supersampling per axis
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let acc = [0, 0, 0, 0]
      for (let sy = 0; sy < ss; sy++) {
        for (let sx = 0; sx < ss; sx++) {
          const u = ((x + (sx + 0.5) / ss) / size) * 512
          const v = ((y + (sy + 0.5) / ss) / size) * 512
          let col = null
          if (maskable || inRoundRect(u, v, 0, 0, 512, 512, 112)) col = bg
          const lu = (u - off) / scale
          const lv = (v - off) / scale
          for (const s of shapes) {
            const hit = s.kind === 'rect'
              ? inRoundRect(lu, lv, s.x, s.y, s.w, s.h, s.r)
              : (lu - s.x) ** 2 + (lv - s.y) ** 2 <= s.r * s.r
            if (hit) col = s.c
          }
          if (col) { acc[0] += col[0]; acc[1] += col[1]; acc[2] += col[2]; acc[3] += 255 }
        }
      }
      const n = ss * ss
      const a = acc[3] / n
      const i = (y * size + x) * 4
      if (a > 0) {
        px[i] = Math.round(acc[0] / (acc[3] / 255))
        px[i + 1] = Math.round(acc[1] / (acc[3] / 255))
        px[i + 2] = Math.round(acc[2] / (acc[3] / 255))
      }
      px[i + 3] = Math.round(a)
    }
  }
  return png(size, size, px)
}

function crc32(buf) {
  let c, crc = 0xffffffff
  for (let n = 0; n < buf.length; n++) {
    c = (crc ^ buf[n]) & 0xff
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    crc = (crc >>> 8) ^ c
  }
  return (crc ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length)
  const td = Buffer.concat([Buffer.from(type), data])
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td))
  return Buffer.concat([len, td, crc])
}

function png(w, h, rgba) {
  const raw = Buffer.alloc((w * 4 + 1) * h)
  for (let y = 0; y < h; y++) {
    raw[y * (w * 4 + 1)] = 0
    rgba.copy(raw, y * (w * 4 + 1) + 1, y * w * 4, (y + 1) * w * 4)
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4)
  ihdr[8] = 8; ihdr[9] = 6; ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw, { level: 9 })), chunk('IEND', Buffer.alloc(0)),
  ])
}

writeFileSync(new URL('../public/icon-192.png', import.meta.url), render(192, { maskable: false }))
writeFileSync(new URL('../public/icon-512.png', import.meta.url), render(512, { maskable: false }))
writeFileSync(new URL('../public/icon-maskable-512.png', import.meta.url), render(512, { maskable: true }))
console.log('icons written')
