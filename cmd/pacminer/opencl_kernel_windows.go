//go:build windows

package main

const openCLKernelSource = `
__constant uint blake_c[16] = {
  0x243f6a88U, 0x85a308d3U, 0x13198a2eU, 0x03707344U,
  0xa4093822U, 0x299f31d0U, 0x082efa98U, 0xec4e6c89U,
  0x452821e6U, 0x38d01377U, 0xbe5466cfU, 0x34e90c6cU,
  0xc0ac29b7U, 0xc97c50ddU, 0x3f84d5b5U, 0xb5470917U
};

__constant uchar sigma[14][16] = {
  { 0, 1, 2, 3, 4, 5, 6, 7, 8, 9,10,11,12,13,14,15},
  {14,10, 4, 8, 9,15,13, 6, 1,12, 0, 2,11, 7, 5, 3},
  {11, 8,12, 0, 5, 2,15,13,10,14, 3, 6, 7, 1, 9, 4},
  { 7, 9, 3, 1,13,12,11,14, 2, 6, 5,10, 4, 0,15, 8},
  { 9, 0, 5, 7, 2, 4,10,15,14, 1,11,12, 6, 8, 3,13},
  { 2,12, 6,10, 0,11, 8, 3, 4,13, 7, 5,15,14, 1, 9},
  {12, 5, 1,15,14,13, 4,10, 0, 7, 6, 3, 9, 2, 8,11},
  {13,11, 7,14,12, 1, 3, 9, 5, 0,15, 4, 8, 6, 2,10},
  { 6,15,14, 9,11, 3, 0, 8,12, 2,13, 7, 1, 4,10, 5},
  {10, 2, 8, 4, 7, 6, 1, 5,15,11, 9,14, 3,12,13, 0},
  { 0, 1, 2, 3, 4, 5, 6, 7, 8, 9,10,11,12,13,14,15},
  {14,10, 4, 8, 9,15,13, 6, 1,12, 0, 2,11, 7, 5, 3},
  {11, 8,12, 0, 5, 2,15,13,10,14, 3, 6, 7, 1, 9, 4},
  { 7, 9, 3, 1,13,12,11,14, 2, 6, 5,10, 4, 0,15, 8}
};

uint be32(__global const uchar *p) {
  return ((uint)p[0] << 24) | ((uint)p[1] << 16) | ((uint)p[2] << 8) | (uint)p[3];
}

uint be32p(__private const uchar *p) {
  return ((uint)p[0] << 24) | ((uint)p[1] << 16) | ((uint)p[2] << 8) | (uint)p[3];
}

void put_be32(__private uchar *p, uint v) {
  p[0] = (uchar)(v >> 24);
  p[1] = (uchar)(v >> 16);
  p[2] = (uchar)(v >> 8);
  p[3] = (uchar)v;
}

void put_le32(__private uchar *p, uint v) {
  p[0] = (uchar)v;
  p[1] = (uchar)(v >> 8);
  p[2] = (uchar)(v >> 16);
  p[3] = (uchar)(v >> 24);
}

void gmix(__private uint *v, int a, int b, int c, int d, uint mx, uint my, uint cx, uint cy) {
  v[a] += v[b] + (mx ^ cx);
  v[d] = rotate(v[d] ^ v[a], (uint)16);
  v[c] += v[d];
  v[b] = rotate(v[b] ^ v[c], (uint)20);
  v[a] += v[b] + (my ^ cy);
  v[d] = rotate(v[d] ^ v[a], (uint)24);
  v[c] += v[d];
  v[b] = rotate(v[b] ^ v[c], (uint)25);
}

void compress(__private uint *h, __private const uchar *block, ulong counter) {
  uint m[16];
  for (int i = 0; i < 16; i++) {
    m[i] = be32p(block + i * 4);
  }

  uint v[16];
  v[0] = h[0]; v[1] = h[1]; v[2] = h[2]; v[3] = h[3];
  v[4] = h[4]; v[5] = h[5]; v[6] = h[6]; v[7] = h[7];
  v[8] = blake_c[0]; v[9] = blake_c[1]; v[10] = blake_c[2]; v[11] = blake_c[3];
  uint t0 = (uint)counter;
  uint t1 = (uint)(counter >> 32);
  v[12] = t0 ^ blake_c[4];
  v[13] = t0 ^ blake_c[5];
  v[14] = t1 ^ blake_c[6];
  v[15] = t1 ^ blake_c[7];

  for (int r = 0; r < 14; r++) {
    gmix(v, 0, 4, 8,12, m[sigma[r][0]],  m[sigma[r][1]],  blake_c[sigma[r][1]],  blake_c[sigma[r][0]]);
    gmix(v, 1, 5, 9,13, m[sigma[r][2]],  m[sigma[r][3]],  blake_c[sigma[r][3]],  blake_c[sigma[r][2]]);
    gmix(v, 2, 6,10,14, m[sigma[r][4]],  m[sigma[r][5]],  blake_c[sigma[r][5]],  blake_c[sigma[r][4]]);
    gmix(v, 3, 7,11,15, m[sigma[r][6]],  m[sigma[r][7]],  blake_c[sigma[r][7]],  blake_c[sigma[r][6]]);
    gmix(v, 0, 5,10,15, m[sigma[r][8]],  m[sigma[r][9]],  blake_c[sigma[r][9]],  blake_c[sigma[r][8]]);
    gmix(v, 1, 6,11,12, m[sigma[r][10]], m[sigma[r][11]], blake_c[sigma[r][11]], blake_c[sigma[r][10]]);
    gmix(v, 2, 7, 8,13, m[sigma[r][12]], m[sigma[r][13]], blake_c[sigma[r][13]], blake_c[sigma[r][12]]);
    gmix(v, 3, 4, 9,14, m[sigma[r][14]], m[sigma[r][15]], blake_c[sigma[r][15]], blake_c[sigma[r][14]]);
  }

  h[0] ^= v[0] ^ v[8];
  h[1] ^= v[1] ^ v[9];
  h[2] ^= v[2] ^ v[10];
  h[3] ^= v[3] ^ v[11];
  h[4] ^= v[4] ^ v[12];
  h[5] ^= v[5] ^ v[13];
  h[6] ^= v[6] ^ v[14];
  h[7] ^= v[7] ^ v[15];
}

int hash_le_target(__private uint *h, __global const uchar *target) {
  uchar out[32];
  for (int i = 0; i < 8; i++) {
    put_be32(out + i * 4, h[i]);
  }
  for (int i = 0; i < 32; i++) {
    if (out[i] < target[i]) return 1;
    if (out[i] > target[i]) return 0;
  }
  return 1;
}

__kernel void mine_blake256(
  __global const uchar *header,
  __global const uchar *target,
  uint start_nonce,
  uint count,
  volatile __global uint *found,
  __global uint *found_nonce,
  __global uchar *found_hash
) {
  uint gid = get_global_id(0);
  if (gid >= count || *found != 0) return;

  uint nonce = start_nonce + gid;
  uchar b0[64];
  uchar b1[64];
  uchar b2[64];
  for (int i = 0; i < 64; i++) b0[i] = header[i];
  for (int i = 0; i < 64; i++) b1[i] = header[64 + i];
  for (int i = 0; i < 64; i++) b2[i] = (uchar)0;
  for (int i = 0; i < 52; i++) b2[i] = header[128 + i];
  put_le32(b2 + 12, nonce);
  b2[52] = (uchar)0x80;
  b2[55] = (uchar)0x01;
  b2[62] = (uchar)0x05;
  b2[63] = (uchar)0xa0;

  uint h[8] = {
    0x6a09e667U, 0xbb67ae85U, 0x3c6ef372U, 0xa54ff53aU,
    0x510e527fU, 0x9b05688cU, 0x1f83d9abU, 0x5be0cd19U
  };
  compress(h, b0, (ulong)512);
  compress(h, b1, (ulong)1024);
  compress(h, b2, (ulong)1440);

  if (hash_le_target(h, target)) {
    if (atomic_cmpxchg(found, 0U, 1U) == 0U) {
      *found_nonce = nonce;
      for (int i = 0; i < 8; i++) {
        uchar tmp[4];
        put_be32(tmp, h[i]);
        found_hash[i*4 + 0] = tmp[0];
        found_hash[i*4 + 1] = tmp[1];
        found_hash[i*4 + 2] = tmp[2];
        found_hash[i*4 + 3] = tmp[3];
      }
    }
  }
}
`
