import { rmSync } from 'node:fs';

rmSync(new URL('../../web/dist/assets', import.meta.url), { recursive: true, force: true });