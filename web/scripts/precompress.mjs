import { readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const immutable = fileURLToPath(new URL('../build/_app/immutable/', import.meta.url));

for (const entry of await readdir(immutable, { recursive: true, withFileTypes: true })) {
  if (!entry.isFile() || !/\.(js|css)$/.test(entry.name)) continue;
  const path = join(entry.parentPath, entry.name);
  const source = await readFile(path);
  if (source.length < 1024) continue;
  const gzip = gzipSync(source, { level: 9 });
  if (gzip.length < source.length) await writeFile(path + '.gz', gzip);
}
