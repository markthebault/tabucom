import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { configPath, readConfig, writeConfig } from '../src/config.js';

test('uses XDG config and writes an origin-only, private config file', async () => {
  const root = await mkdtemp(join(tmpdir(), 'tabucom-skill-'));
  const path = configPath({ XDG_CONFIG_HOME: root });
  await writeConfig('https://tabucom.example.com/', path);
  assert.deepEqual(await readConfig(path), { baseUrl: 'https://tabucom.example.com' });
  assert.deepEqual(JSON.parse(await readFile(path, 'utf8')), { baseUrl: 'https://tabucom.example.com' });
  assert.equal((await stat(path)).mode & 0o077, 0);
});

test('uses HOME when XDG_CONFIG_HOME is not set', () => {
  assert.equal(configPath({ HOME: '/operator/home' }), '/operator/home/.config/tabucom/config.json');
});
