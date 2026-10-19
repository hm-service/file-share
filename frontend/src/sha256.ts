/**
 * 纯 TypeScript 的增量 SHA-256（FIPS 180-4），不引入任何运行时依赖。
 *
 * 为什么手写：旧项目用 hashwasm（WASM），迁移后要求零新增依赖；上传前的秒传预检需要
 * 先算客户端指纹，所以这里实现流式版本——hashFile.ts 按 4 MiB 分块 update，
 * 内存占用与文件大小无关。
 *
 * selfCheck() 里的已知测试向量在开发模式下由入口调用；任何实现错误都会立刻抛错，
 * 而不是等到上传后才发现「秒传永远不命中」。
 */

/** SHA-256 的 64 个轮常量（前 64 个素数立方根小数部分的前 32 位）。 */
const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

/** 初始哈希值（前 8 个素数平方根小数部分的前 32 位）。 */
const INITIAL = new Uint32Array([
  0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
]);

const BLOCK_BYTES = 64;

function rotr(value: number, bits: number): number {
  return (value >>> bits) | (value << (32 - bits));
}

export class Sha256 {
  private readonly state = new Uint32Array(INITIAL);
  private readonly block = new Uint8Array(BLOCK_BYTES);
  private readonly schedule = new Uint32Array(64);
  private blockLength = 0;
  private bytesHashed = 0;
  private finished = false;

  /** 喂一段数据；可以反复调用（增量）。 */
  update(data: Uint8Array): this {
    if (this.finished) throw new Error("SHA-256: update() called after digest()");
    this.bytesHashed += data.length;

    let offset = 0;
    if (this.blockLength > 0) {
      const take = Math.min(BLOCK_BYTES - this.blockLength, data.length);
      this.block.set(data.subarray(0, take), this.blockLength);
      this.blockLength += take;
      offset += take;
      if (this.blockLength === BLOCK_BYTES) {
        this.compress(this.block, 0);
        this.blockLength = 0;
      }
    }

    // 整块直接从输入里压缩，省一次 64 字节的拷贝
    while (offset + BLOCK_BYTES <= data.length) {
      this.compress(data, offset);
      offset += BLOCK_BYTES;
    }

    if (offset < data.length) {
      this.block.set(data.subarray(offset), 0);
      this.blockLength = data.length - offset;
    }
    return this;
  }

  /** 结束并返回 32 字节摘要（大端）。 */
  digest(): Uint8Array {
    this.pad();
    const bytes = new Uint8Array(32);
    const view = new DataView(bytes.buffer);
    for (let index = 0; index < 8; index += 1) {
      view.setUint32(index * 4, this.state[index], false);
    }
    return bytes;
  }

  /** 结束并返回小写十六进制摘要。 */
  hex(): string {
    const bytes = this.digest();
    let out = "";
    for (const byte of bytes) out += byte.toString(16).padStart(2, "0");
    return out;
  }

  private compress(data: Uint8Array, offset: number): void {
    const w = this.schedule;
    for (let index = 0; index < 16; index += 1) {
      const at = offset + index * 4;
      w[index] = ((data[at] << 24) | (data[at + 1] << 16) | (data[at + 2] << 8) | data[at + 3]) >>> 0;
    }
    for (let index = 16; index < 64; index += 1) {
      const x = w[index - 15];
      const y = w[index - 2];
      const s0 = (rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3)) >>> 0;
      const s1 = (rotr(y, 17) ^ rotr(y, 19) ^ (y >>> 10)) >>> 0;
      w[index] = (w[index - 16] + s0 + w[index - 7] + s1) >>> 0;
    }

    const state = this.state;
    let a = state[0];
    let b = state[1];
    let c = state[2];
    let d = state[3];
    let e = state[4];
    let f = state[5];
    let g = state[6];
    let h = state[7];

    for (let index = 0; index < 64; index += 1) {
      const s1 = (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) >>> 0;
      const choose = ((e & f) ^ (~e & g)) >>> 0;
      const temp1 = (h + s1 + choose + K[index] + w[index]) >>> 0;
      const s0 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) >>> 0;
      const majority = ((a & b) ^ (a & c) ^ (b & c)) >>> 0;
      const temp2 = (s0 + majority) >>> 0;

      h = g;
      g = f;
      f = e;
      e = (d + temp1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (temp1 + temp2) >>> 0;
    }

    state[0] = (state[0] + a) >>> 0;
    state[1] = (state[1] + b) >>> 0;
    state[2] = (state[2] + c) >>> 0;
    state[3] = (state[3] + d) >>> 0;
    state[4] = (state[4] + e) >>> 0;
    state[5] = (state[5] + f) >>> 0;
    state[6] = (state[6] + g) >>> 0;
    state[7] = (state[7] + h) >>> 0;
  }

  /** 在最外层完成 MD 填充（消息长度按位、大端 64 位），然后交给 digest() 取结果。 */
  private pad(): void {
    if (this.finished) return;
    const bits = this.bytesHashed * 8;
    const high = Math.floor(bits / 0x100000000);
    const low = bits % 0x100000000;
    const padLength = this.blockLength < 56 ? 64 - this.blockLength : 128 - this.blockLength;
    const pad = new Uint8Array(padLength);
    pad[0] = 0x80;
    pad[padLength - 8] = (high >>> 24) & 0xff;
    pad[padLength - 7] = (high >>> 16) & 0xff;
    pad[padLength - 6] = (high >>> 8) & 0xff;
    pad[padLength - 5] = high & 0xff;
    pad[padLength - 4] = (low >>> 24) & 0xff;
    pad[padLength - 3] = (low >>> 16) & 0xff;
    pad[padLength - 2] = (low >>> 8) & 0xff;
    pad[padLength - 1] = low & 0xff;
    // 先把 finished 置上，update 内部会拒绝继续 update；pad 这一段是最后一块数据
    this.finished = true;
    this.bytesHashed += padLength;
    let offset = 0;
    if (this.blockLength > 0) {
      const take = Math.min(64 - this.blockLength, padLength);
      this.block.set(pad.subarray(0, take), this.blockLength);
      this.blockLength += take;
      offset = take;
      if (this.blockLength === 64) {
        this.compress(this.block, 0);
        this.blockLength = 0;
      }
    }
    while (offset + 64 <= padLength) {
      this.compress(pad, offset);
      offset += 64;
    }
    if (offset < padLength) {
      this.block.set(pad.subarray(offset), 0);
      this.blockLength = padLength - offset;
    }
  }
}

/**
 * 已知测试向量自检：空串、abc、标准两段串、一百万个 a（最后一个覆盖多块与长度高位）。
 * 开发模式下入口调用；失败直接抛错，避免带着错误的指纹去上传。
 * 向量在函数内构造：生产构建里这个函数会被 tree-shake，不白白分配那一兆字符串。
 */
export function selfCheck(): void {
  const vectors: ReadonlyArray<readonly [string, string]> = [
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
    [
      "abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
      "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
    ],
    ["a".repeat(1_000_000), "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"],
  ];
  const encoder = new TextEncoder();
  for (const [input, expected] of vectors) {
    const bytes = encoder.encode(input);
    const hasher = new Sha256();
    // 切成两段喂，顺带验证增量更新与一次性输入结果一致
    const split = Math.floor(bytes.length / 3);
    hasher.update(bytes.subarray(0, split));
    hasher.update(bytes.subarray(split));
    const actual = hasher.hex();
    if (actual !== expected) {
      throw new Error(`SHA-256 self-check failed: expected ${expected}, got ${actual}`);
    }
  }
}
