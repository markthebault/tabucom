import { mkdir, readFile, rename, writeFile, chmod } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { homedir } from 'node:os';
import { randomUUID } from 'node:crypto';
import { normalizeBaseUrl } from './validate.js';

export function configPath(env = process.env) {
  const configHome = env.XDG_CONFIG_HOME || join(env.HOME || homedir(), '.config');
  return join(configHome, 'tabucom', 'config.json');
}

export async function readConfig(path = configPath()) {
  let text;
  try {
    text = await readFile(path, 'utf8');
  } catch (error) {
    if (error.code === 'ENOENT') return null;
    throw error;
  }

  let config;
  try {
    config = JSON.parse(text);
  } catch {
    throw new Error(`Invalid JSON in ${path}. Run \`tabucom-skill configure --base-url <url>\` to repair it.`);
  }
  if (!config || typeof config !== 'object' || Array.isArray(config) || typeof config.baseUrl !== 'string') {
    throw new Error(`Invalid Tabucom configuration in ${path}. Expected a baseUrl string.`);
  }

  return { baseUrl: normalizeBaseUrl(config.baseUrl) };
}

export async function writeConfig(baseUrl, path = configPath()) {
  const config = { baseUrl: normalizeBaseUrl(baseUrl) };
  const directory = dirname(path);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await chmod(directory, 0o700).catch(() => {});

  const temporaryPath = join(directory, `.config-${randomUUID()}.tmp`);
  await writeFile(temporaryPath, `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 });
  await chmod(temporaryPath, 0o600).catch(() => {});
  await rename(temporaryPath, path);
  await chmod(path, 0o600).catch(() => {});
  return config;
}
